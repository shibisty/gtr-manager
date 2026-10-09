// Package install turns gtr.json into an installed project: it resolves the
// dependency graph, fills the store, links gtr_modules/, writes gtr.lock and
// generates go.mod (ADR-0001, ADR-0003, ADR-0004, ADR-0005).
package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gtr-manager/internal/gomod"
	"gtr-manager/internal/gomodules"
	"gtr-manager/internal/home"
	"gtr-manager/internal/link"
	"gtr-manager/internal/lock"
	"gtr-manager/internal/manifest"
	"gtr-manager/internal/resolve"
	"gtr-manager/internal/semver"
	"gtr-manager/internal/source"
	"gtr-manager/internal/spec"
	"gtr-manager/internal/store"
)

// ModulesDir is the directory of package links in a project.
const ModulesDir = "gtr_modules"

// DefaultGo is the go directive when engines.go names no version (ADR-0007).
const DefaultGo = "1.22"

// placeholder is the version gtr writes into package go.mod requirements:
// lower than any real version, so the project's go.mod always decides.
const placeholder = "v0.0.0-0"

// Installer installs one project.
type Installer struct {
	Dir string // project directory (the workspace root in a workspace)
	// Member is the member directory (relative to Dir, slash-separated)
	// whose gtr.json add and remove change; "" for the root.
	Member  string
	Home    home.Layout // ~/.gtr
	GitHub  source.Provider
	GoProxy *gomodules.Proxies // external Go modules; nil: from the environment
	SumDB   *gomodules.SumDB   // checksum database; nil: no checks (tests)
	Log     io.Writer          // progress and warnings (stderr)
	// CheckName rejects names taken by the standard library (ADR-0002);
	// nil skips the check.
	CheckName func(name string) error

	ws *workspaceSource // members of the workspace being installed
}

// GoDir is the directory of external module links inside gtr_modules.
const GoDir = ".go"

func (in *Installer) proxies() *gomodules.Proxies {
	if in.GoProxy == nil {
		in.GoProxy = gomodules.NewProxies(filepath.Join(in.Home.Root, "cache", "download", "go"))
	}
	return in.GoProxy
}

func (in *Installer) goStore() *gomodules.Store {
	return &gomodules.Store{Root: filepath.Join(in.Home.Root, "store", "go"), Tmp: in.Home.Tmp()}
}

// Options change Install.
type Options struct {
	Update    map[string]bool // ignore the lock for these names ("*" = all)
	Frozen    bool            // gtr ci: the lock must already match gtr.json
	ForceMod  bool            // overwrite a go.mod edited by hand
	Manifest  *manifest.Manifest
	SaveAfter bool // write gtr.json (after add/remove)
	// Prefer selects these module versions over gtr.lock when they fit the
	// ranges (gtr sync keeps the versions written in go.mod).
	Prefer map[string]string
	// Raise adds a dependency for each module gtr would select below the
	// given version (indirect requirements raised in go.mod, gtr sync).
	Raise map[string]string
	// MinGo raises engines.go when the generated go directive would be
	// lower (the go directive of go.mod, gtr sync).
	MinGo string
	// OnAdjust is told about every change Raise and MinGo make to gtr.json.
	OnAdjust func(change string)
	// MemberManifests are members' gtr.json not saved yet (gtr add in a
	// member), by member directory; saved after a successful install.
	MemberManifests map[string]*manifest.Manifest
}

func (in *Installer) log() io.Writer {
	if in.Log == nil {
		return io.Discard
	}
	return in.Log
}

func (in *Installer) path(name string) string { return filepath.Join(in.Dir, name) }

func (in *Installer) store() *store.Store {
	return &store.Store{Root: filepath.Join(in.Home.Root, "store"), Tmp: in.Home.Tmp()}
}

func (in *Installer) file() *source.File { return &source.File{ProjectDir: in.Dir} }

// Providers returns the provider for a kind of source.
func (in *Installer) Providers(k spec.Kind) (source.Provider, error) {
	switch k {
	case spec.GitHub:
		return in.GitHub, nil
	case spec.File:
		return in.file(), nil
	case spec.Registry:
		return nil, errors.New(`the gtr registry is not available yet (stage 6); use a GitHub source: "github:owner/repo#^0.1"`)
	case spec.GoModule:
		return nil, errors.New("external Go modules are selected by minimal version selection, not by this resolver")
	case spec.Workspace:
		if in.ws == nil {
			return nil, errors.New(`workspace: dependencies need a workspace: a parent gtr.json with "workspaces" (ADR-0009)`)
		}
		return in.ws, nil
	}
	return nil, fmt.Errorf("unknown source kind %q", k)
}

// Load reads gtr.json and prints migration warnings.
func (in *Installer) Load() (*manifest.Manifest, error) {
	m, err := manifest.Load(in.path(manifest.FileName))
	if err != nil {
		return nil, err
	}
	for _, w := range m.Warnings {
		fmt.Fprintf(in.log(), "warning: gtr.json: %s\n", w)
	}
	return m, nil
}

// Install installs everything gtr.json asks for.
func (in *Installer) Install(ctx context.Context, opts Options) error {
	m := opts.Manifest
	var err error
	if m == nil {
		if m, err = in.Load(); err != nil {
			return err
		}
	}
	members, err := Members(in.Dir, m, opts.MemberManifests)
	if err != nil {
		return err
	}
	in.ws = nil
	if len(members) > 0 {
		in.ws = newWorkspaceSource(members)
	}
	if err := checkGenerated(in.Dir, members, opts.ForceMod); err != nil {
		return err
	}

	if err := spec.ValidateName(m.Name); err != nil {
		return fmt.Errorf("gtr.json: %w; the project name becomes the module path in go.mod", err)
	}
	migrated, err := in.migrateLegacy(ctx, m)
	if err != nil {
		return err
	}
	root, err := rootSpecs(m)
	if err != nil {
		return err
	}
	if _, self := root[m.Name]; self {
		return fmt.Errorf("gtr.json: the project %q depends on a package with its own name", m.Name)
	}
	lockPath := in.path(lock.FileName)
	old, err := lock.Load(lockPath)
	if err != nil {
		return err
	}
	if opts.Frozen {
		if _, err := os.Stat(lockPath); err != nil {
			return errors.New("gtr ci needs gtr.lock; run `gtr install` and commit gtr.lock")
		}
	}
	gtrRoot, goRoot := map[string]spec.Spec{}, map[string]spec.Spec{}
	for name, s := range root {
		if s.Kind == spec.GoModule {
			goRoot[name] = s
		} else {
			gtrRoot[name] = s
		}
	}
	var memberSpecs map[string]spec.Spec
	if len(members) > 0 {
		memberSpecs = map[string]spec.Spec{}
		for name, mem := range members {
			memberSpecs[name] = mem.Spec()
			if _, dep := gtrRoot[name]; !dep {
				gtrRoot[name] = mem.Spec() // every member is a root of the graph
			}
		}
	}
	nodes, err := resolve.Resolve(ctx, in.Providers, resolve.Request{Root: gtrRoot, Lock: old, Update: opts.Update, Members: memberSpecs})
	if err != nil {
		return err
	}
	mods, err := in.resolveGo(ctx, goRoot, nodes, old, opts.Update, opts.Prefer)
	if err != nil {
		return err
	}
	adjusted, err := in.adjust(ctx, m, opts, goRoot, nodes, old, &mods)
	if err != nil {
		return err
	}
	if in.CheckName != nil {
		names := append([]string{m.Name}, sortedNames(nodes)...)
		for _, n := range names {
			if err := in.CheckName(n); err != nil {
				return fmt.Errorf("%s: %w", n, err)
			}
		}
	}
	if opts.Frozen {
		if err := sameAsLock(installed(nodes), mods, old); err != nil {
			return err
		}
	}

	newLock := &lock.Lock{LockfileVersion: lock.Version, Packages: map[string]lock.Entry{}, Workspace: memberDirs(members)}
	for _, name := range sortedKeys(opts.Update) {
		if _, member := members[name]; member {
			fmt.Fprintf(in.log(), "warning: %s is a workspace member; it is used in place, not updated\n", name)
		} else if _, mod := mods.Versions[name]; name != "*" && !mod && nodes[name] == nil {
			fmt.Fprintf(in.log(), "warning: %s is not installed\n", name)
		}
	}
	dirs := map[string]string{}
	for _, name := range sortedNames(installed(nodes)) {
		n := nodes[name]
		dir, entry, err := in.materialize(ctx, n, old.Packages[name])
		if err != nil {
			return err
		}
		dirs[name] = dir
		newLock.Packages[name] = entry
	}
	goDirs := map[string]string{}
	for _, p := range sortedKeys(mods.Versions) {
		dir, entry, err := in.fetchGo(ctx, mods, p, old.Packages[p], opts.Update)
		if err != nil {
			return err
		}
		goDirs[p] = dir
		newLock.Packages[p] = entry
	}
	if opts.Frozen {
		if err := in.verifyStore(nodes, newLock, dirs); err != nil {
			return err
		}
		if err := in.verifyGoStore(mods, newLock, goDirs); err != nil {
			return err
		}
	}
	if err := in.linkAll(dirs, old); err != nil {
		return err
	}
	if err := in.linkGo(mods, goDirs); err != nil {
		return err
	}
	// gtr.json first: if writing go.mod fails, a rerun regenerates it, while
	// a go.mod written without its gtr.json changes would lose them.
	if migrated || opts.SaveAfter || adjusted {
		if err := manifest.Save(in.path(manifest.FileName), m); err != nil {
			return err
		}
	}
	for _, dir := range sortedKeys(opts.MemberManifests) {
		if err := manifest.Save(filepath.Join(in.Dir, filepath.FromSlash(dir), manifest.FileName), opts.MemberManifests[dir]); err != nil {
			return err
		}
	}
	if len(members) > 0 {
		if err := in.writeWorkspace(m, root, nodes, mods, members); err != nil {
			return err
		}
	} else {
		if err := in.writeGoMod(m, nodes, mods, goRoot); err != nil {
			return err
		}
		// A go.work gtr generated for a former workspace would put IDEs
		// into workspace mode.
		if st, _ := gomod.Check(in.path("go.work")); st == gomod.Generated {
			if err := os.Remove(in.path("go.work")); err != nil {
				return err
			}
			fmt.Fprintln(in.log(), "Removed go.work (no longer a workspace)")
		}
	}
	if err := in.cleanFormerMembers(old.Workspace, members); err != nil {
		return err
	}
	if err := writeIfChanged(lockPath, newLock); err != nil {
		return err
	}
	if err := ensureGitignore(in.Dir, m, len(members) > 0); err != nil {
		return err
	}
	for _, name := range sortedKeys(members) {
		if err := ensureGitignore(members[name].Abs, members[name].Manifest, false); err != nil {
			return err
		}
	}
	in.summary(old, newLock)
	if len(members) > 0 {
		fmt.Fprintf(in.log(), "Workspace: %d members (%s)\n", len(members), strings.Join(memberDirs(members), ", "))
	}
	return nil
}

// rootSpecs parses dependencies and devDependencies of the project.
func rootSpecs(m *manifest.Manifest) (map[string]spec.Spec, error) {
	root := map[string]spec.Spec{}
	for _, deps := range []map[string]string{m.Dependencies, m.DevDependencies} {
		for name, value := range deps {
			if err := spec.ValidateKey(name); err != nil {
				return nil, fmt.Errorf("gtr.json: %w", err)
			}
			s, err := spec.Parse(name, value)
			if err != nil {
				return nil, fmt.Errorf("gtr.json: %w", err)
			}
			if _, dup := root[name]; dup {
				return nil, fmt.Errorf("gtr.json: %s is in both dependencies and devDependencies", name)
			}
			root[name] = s
		}
	}
	return root, nil
}

// migrateLegacy rewrites pre-ADR-0003 keys ("github:owner/repo": "*") to
// "<name>": "github:owner/repo#<range>", reading the name from the
// package's gtr.json.
func (in *Installer) migrateLegacy(ctx context.Context, m *manifest.Manifest) (bool, error) {
	changed := false
	for _, deps := range []map[string]string{m.Dependencies, m.DevDependencies} {
		for _, key := range sortedKeys(deps) {
			if !spec.IsLegacyKey(key) {
				continue
			}
			value := deps[key]
			if !strings.HasPrefix(key, "github:") {
				return false, fmt.Errorf("gtr.json: %q uses the old format and its source is not supported; remove it and add the package again", key)
			}
			rng := value
			if rng == "" {
				rng = "*"
			}
			s, err := spec.Parse("legacy", key+"#"+rng)
			if err != nil {
				return false, fmt.Errorf("gtr.json: %q: %w", key, err)
			}
			name, _, err := in.Discover(ctx, s)
			if err != nil {
				return false, fmt.Errorf("gtr.json: cannot migrate %q: %w", key, err)
			}
			if _, exists := deps[name]; exists {
				return false, fmt.Errorf("gtr.json: %q and %q are the same package; remove one", key, name)
			}
			delete(deps, key)
			deps[name] = s.String()
			fmt.Fprintf(in.log(), "warning: gtr.json: %q migrated to %q: %q (ADR-0003)\n", key, name, s.String())
			changed = true
		}
	}
	return changed, nil
}

// Discover finds the name and best version of a package given only its
// source (for `gtr add github:owner/repo`).
func (in *Installer) Discover(ctx context.Context, s spec.Spec) (string, source.Candidate, error) {
	p, err := in.Providers(s.Kind)
	if err != nil {
		return "", source.Candidate{}, err
	}
	cands, err := p.Candidates(ctx, "", s)
	if err != nil {
		return "", source.Candidate{}, err
	}
	// Monorepo tags may carry the package name ("orm-mysql@0.1.0"), which is
	// not known yet: read it from the newest candidate and list again.
	if s.Subdir != "" && len(cands) > 0 {
		if m, err := p.Manifest(ctx, s, cands[0]); err == nil && spec.ValidateName(m.Name) == nil {
			if again, err := p.Candidates(ctx, m.Name, s); err == nil {
				cands = again
			}
		}
	}
	rng, err := semver.ParseRange(s.Range)
	if err != nil {
		return "", source.Candidate{}, err
	}
	for _, c := range cands {
		v, err := semver.Parse(c.Version)
		if err != nil || (c.Head && s.Range != "*") || (!c.Head && !rng.Match(v)) {
			continue
		}
		m, err := p.Manifest(ctx, s, c)
		if errors.Is(err, source.ErrNoManifest) {
			return "", c, fmt.Errorf("%s has no gtr.json, so it is not a gtr package; "+
				"if it is a Go module, add it by its module path (step 3.2)", s.String())
		}
		if err != nil {
			return "", c, err
		}
		if err := spec.ValidateName(m.Name); err != nil {
			return "", c, fmt.Errorf("%s: %w", s.String(), err)
		}
		return m.Name, c, nil
	}
	return "", source.Candidate{}, fmt.Errorf("%s: no version matches %s", s.String(), s.Range)
}

// materialize makes a package available on disk and returns its directory
// and lock entry.
func (in *Installer) materialize(ctx context.Context, n *resolve.Node, prev lock.Entry) (string, lock.Entry, error) {
	entry := lock.Entry{Version: n.Candidate.Version, Source: sourceString(n.Spec), Commit: n.Candidate.Commit}
	if len(n.Deps) > 0 {
		entry.Dependencies = map[string]string{}
		for dn, ds := range n.Deps {
			entry.Dependencies[dn] = ds.Raw
		}
	}
	goMod := gomod.Package(n.Name, gomod.GoDirective(n.Manifest.Engines.Go, DefaultGo), packageRequires(n))
	for dn, ds := range n.GoDeps {
		if entry.Dependencies == nil {
			entry.Dependencies = map[string]string{}
		}
		entry.Dependencies[dn] = ds.Raw
	}

	if n.Spec.Kind == spec.File {
		dir := in.file().Dir(n.Spec)
		return dir, entry, writePackageGoMod(dir, n.Name, goMod)
	}

	st := in.store()
	same := prev.Version == entry.Version && prev.Commit == entry.Commit && prev.Integrity != ""
	if same && st.Has(n.Name, entry.Version, prev.Integrity) {
		entry.Integrity = prev.Integrity
		return st.Dir(n.Name, entry.Version, prev.Integrity), entry, nil
	}
	tmp, err := st.TempDir()
	if err != nil {
		return "", entry, err
	}
	defer os.RemoveAll(tmp)
	fmt.Fprintf(in.log(), "Downloading %s@%s\n", n.Name, entry.Version)
	p, err := in.Providers(n.Spec.Kind)
	if err != nil {
		return "", entry, err
	}
	if err := p.Fetch(ctx, n.Spec, n.Candidate, tmp); err != nil {
		return "", entry, fmt.Errorf("%s@%s: %w", n.Name, entry.Version, err)
	}
	want := ""
	if same {
		want = prev.Integrity
	}
	dir, integrity, err := st.Put(tmp, n.Name, entry.Version, want, func(d string) error {
		return os.WriteFile(filepath.Join(d, "go.mod"), goMod, 0o644)
	})
	if err != nil {
		return "", entry, err
	}
	entry.Integrity = integrity
	return dir, entry, nil
}

func sourceString(s spec.Spec) string {
	s.Range = ""
	return s.String()
}

// packageRequires lists a package's dependencies for its go.mod.
func packageRequires(n *resolve.Node) []gomod.Require {
	var reqs []gomod.Require
	for _, dn := range sortedKeys(n.Deps) {
		reqs = append(reqs, gomod.Require{Path: dn, Version: placeholder})
	}
	for _, pn := range sortedKeys(n.Manifest.PeerDependencies) {
		if _, dup := n.Deps[pn]; !dup && !spec.IsGoModule(pn) {
			reqs = append(reqs, gomod.Require{Path: pn, Version: placeholder})
		}
	}
	// External modules: the lowest version the range admits (a /vN path
	// needs a vN version); the project go.mod has the selected one.
	for _, dn := range sortedKeys(n.GoDeps) {
		reqs = append(reqs, gomod.Require{Path: dn, Version: gomodules.Floor(dn, n.GoDeps[dn].Range)})
	}
	return reqs
}

// writePackageGoMod writes the generated go.mod into a file: package. A
// go.mod the author wrote is kept if it declares the right module path.
func writePackageGoMod(dir, name string, content []byte) error {
	path := filepath.Join(dir, "go.mod")
	// A go.mod a workspace generated for this directory (it is a member
	// there) is kept: rewriting it with versioned requirements on other
	// members would break that workspace, and it serves here as well.
	if st, _ := gomod.Check(path); st == gomod.Generated || st == gomod.Edited {
		if mod, err := gomod.ModulePath(path); err == nil && mod == name {
			return nil
		}
	}
	if _, err := os.Stat(path); err == nil && !gomod.IsPackageGenerated(path) {
		mod, err := gomod.ModulePath(path)
		if err != nil {
			return err
		}
		if mod != name {
			return fmt.Errorf("%s declares module %q, but the package is %q; delete that go.mod (gtr generates one) or rename the module", path, mod, name)
		}
		return nil
	}
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, content) {
		return nil
	}
	return os.WriteFile(path, content, 0o644)
}

// linkAll makes gtr_modules/<name> for every package and removes entries
// that are no longer needed.
func (in *Installer) linkAll(dirs map[string]string, old *lock.Lock) error {
	base := in.path(ModulesDir)
	for _, name := range sortedKeys(dirs) {
		l := filepath.Join(base, name)
		if t := link.Target(l); t != "" && filepath.Clean(t) == filepath.Clean(dirs[name]) {
			continue
		}
		kind, err := link.Make(dirs[name], l)
		if err != nil {
			return err
		}
		if kind == link.Copy {
			fmt.Fprintf(in.log(), "warning: %s is a copy (links are not available here)\n", l)
		}
	}
	entries, err := os.ReadDir(base)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Remove only what gtr made: links into the store or to a file: package
	// of the previous install, and marked copies. Anything else is left alone.
	storeRoot := in.store().Root
	known := map[string]bool{}
	for _, e := range old.Packages {
		if strings.HasPrefix(e.Source, "file:") {
			known[filepath.Clean(in.file().Dir(spec.Spec{Path: strings.TrimPrefix(e.Source, "file:")}))] = true
		}
	}
	for _, e := range entries {
		if _, keep := dirs[e.Name()]; keep || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(base, e.Name())
		t := link.Target(p)
		ours := link.IsCopy(p) || t != "" && (within(storeRoot, t) || known[filepath.Clean(t)])
		if !ours {
			fmt.Fprintf(in.log(), "warning: %s was not created by gtr; leaving it\n", p)
			continue
		}
		if err := link.Remove(p); err != nil {
			return err
		}
	}
	return nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// verifyStore re-hashes stored packages (gtr ci): a file changed through a
// gtr_modules link would otherwise go unnoticed.
func (in *Installer) verifyStore(nodes map[string]*resolve.Node, l *lock.Lock, dirs map[string]string) error {
	for _, name := range sortedNames(nodes) {
		e := l.Packages[name]
		if e.Integrity == "" {
			continue
		}
		got, err := store.Integrity(dirs[name], name, e.Version)
		if err != nil {
			return err
		}
		if got != e.Integrity {
			return fmt.Errorf("%s@%s in the store (%s) was modified: expected %s, found %s; delete that directory and run `gtr ci` again",
				name, e.Version, dirs[name], e.Integrity, got)
		}
	}
	return nil
}

// writeGoMod generates the project go.mod.
// goDirective is the go directive of the project go.mod: at least what every
// package needs; otherwise the go command raises it (and adds a toolchain
// line) on the next build.
func goDirective(m *manifest.Manifest, nodes map[string]*resolve.Node, mods *gomodules.Result) string {
	goVer := gomod.GoDirective(m.Engines.Go, DefaultGo)
	for _, n := range nodes {
		goVer = gomod.MaxGo(goVer, gomod.GoDirective(n.Manifest.Engines.Go, DefaultGo))
	}
	for _, mf := range mods.Mods {
		if mf.Go != "" {
			goVer = gomod.MaxGo(goVer, mf.Go)
		}
	}
	return goVer
}

var simpleGoRange = regexp.MustCompile(`^\s*(>=\s*)?\d+\.\d+(\.\d+)?\s*$`)

// adjust applies opts.Raise and opts.MinGo to the manifest (gtr sync) and
// reports whether it changed anything.
func (in *Installer) adjust(ctx context.Context, m *manifest.Manifest, opts Options, goRoot map[string]spec.Spec,
	nodes map[string]*resolve.Node, old *lock.Lock, mods **gomodules.Result) (bool, error) {
	changed := false
	note := func(format string, args ...any) {
		changed = true
		if opts.OnAdjust != nil {
			opts.OnAdjust(fmt.Sprintf(format, args...))
		}
	}
	prefer := map[string]string{}
	for p, v := range opts.Prefer {
		prefer[p] = v
	}
	for _, p := range sortedKeys(opts.Raise) {
		want := opts.Raise[p]
		got, selected := (*mods).Versions[p]
		if _, root := goRoot[p]; root || !selected || gomodules.Compare(got, want) >= 0 {
			continue
		}
		if _, dev := m.DevDependencies[p]; dev {
			continue
		}
		m.Dependencies[p] = ModuleRange(want)
		s, err := spec.Parse(p, m.Dependencies[p])
		if err != nil {
			return false, err
		}
		goRoot[p] = s
		prefer[p] = want
		note("added %s: %q (go.mod raises it from %s to %s; gtr.json needs a dependency for that)", p, m.Dependencies[p], got, want)
	}
	if changed {
		r, err := in.resolveGo(ctx, goRoot, nodes, old, opts.Update, prefer)
		if err != nil {
			return false, err
		}
		*mods = r
	}
	if opts.MinGo != "" && gomod.CompareGo(opts.MinGo, goDirective(m, nodes, *mods)) > 0 {
		lang := goLanguage(opts.MinGo)
		if lang != "" && lang != gomod.GoDirective(m.Engines.Go, "") {
			if m.Engines.Go != "" && !simpleGoRange.MatchString(m.Engines.Go) {
				return false, fmt.Errorf("go.mod sets go %s, which engines.go %q in gtr.json does not allow for; change engines.go", opts.MinGo, m.Engines.Go)
			}
			before := m.Engines.Go
			m.Engines.Go = ">=" + lang
			note("engines.go: %q → %q (from go.mod go %s)", before, m.Engines.Go, opts.MinGo)
		}
	}
	return changed, nil
}

// goLanguage returns the language version of a go directive:
// "1.25rc1" → "1.25", "1.23.4" → "1.23".
func goLanguage(v string) string {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return ""
	}
	minor := parts[1]
	for i, r := range minor {
		if r < '0' || r > '9' {
			minor = minor[:i]
			break
		}
	}
	if minor == "" {
		return ""
	}
	return parts[0] + "." + minor
}

func (in *Installer) writeGoMod(m *manifest.Manifest, nodes map[string]*resolve.Node, mods *gomodules.Result, goRoot map[string]spec.Spec) error {
	module := m.Name
	if module == "" {
		module = filepath.Base(in.Dir)
	}
	p := gomod.Project{Module: module, Go: goDirective(m, nodes, mods), Replace: map[string]string{}}
	for _, name := range sortedNames(nodes) {
		r := gomod.Require{Path: name, Version: "v" + nodes[name].Candidate.Version}
		if nodes[name].Root {
			p.Direct = append(p.Direct, r)
		} else {
			p.Indirect = append(p.Indirect, r)
		}
		p.Replace[name] = "./" + ModulesDir + "/" + name
	}
	for _, mp := range sortedKeys(mods.Versions) {
		r := gomod.Require{Path: mp, Version: mods.Versions[mp]}
		if _, direct := goRoot[mp]; direct {
			p.Direct = append(p.Direct, r)
		} else {
			p.Indirect = append(p.Indirect, r)
		}
		dn, err := gomodules.DirName(mp, mods.Versions[mp])
		if err != nil {
			return err
		}
		p.Replace[mp] = "./" + ModulesDir + "/" + GoDir + "/" + dn
	}
	content := p.Render()
	path := in.path("go.mod")
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, content) {
		return nil
	}
	return manifest.WriteFileAtomic(path, content)
}

func writeIfChanged(path string, l *lock.Lock) error {
	data, err := lock.Marshal(l)
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return nil
	}
	return manifest.WriteFileAtomic(path, data)
}

// ensureGitignore adds what gtr generates to dir/.gitignore (ADR-0001,
// item 7; ADR-0009 for a workspace root).
func ensureGitignore(dir string, m *manifest.Manifest, workspace bool) error {
	want := []string{ModulesDir + "/"}
	if string(m.Raw("gomod")) != `"commit"` {
		want = append(want, "go.mod", "go.work")
	}
	if workspace {
		want = append(want, "go.work.sum")
	}
	path := filepath.Join(dir, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	have := map[string]bool{}
	for _, l := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		have[strings.TrimSpace(l)] = true
	}
	var add []string
	for _, w := range want {
		if !have[w] && !have["/"+w] {
			add = append(add, w)
		}
	}
	if len(add) == 0 {
		return nil
	}
	out := string(data)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	if out == "" {
		out = "# generated by gtr\n"
	}
	out += strings.Join(add, "\n") + "\n"
	return os.WriteFile(path, []byte(out), 0o644)
}

// sameAsLock checks that a resolution did not change anything (gtr ci).
func sameAsLock(nodes map[string]*resolve.Node, mods *gomodules.Result, l *lock.Lock) error {
	var diff []string
	for p, v := range mods.Versions {
		if e, ok := l.Packages[p]; !ok {
			diff = append(diff, fmt.Sprintf("  + %s@%s", p, v))
		} else if e.Version != v {
			diff = append(diff, fmt.Sprintf("  ~ %s %s → %s", p, e.Version, v))
		}
	}
	for name, n := range nodes {
		e, ok := l.Packages[name]
		if !ok {
			diff = append(diff, fmt.Sprintf("  + %s@%s", name, n.Candidate.Version))
		} else if e.Version != n.Candidate.Version || e.Commit != n.Candidate.Commit {
			diff = append(diff, fmt.Sprintf("  ~ %s %s → %s", name, e.Version, n.Candidate.Version))
		}
	}
	for name, e := range l.Packages {
		_, isMod := mods.Versions[name]
		if _, ok := nodes[name]; !ok && !isMod {
			diff = append(diff, fmt.Sprintf("  - %s@%s", name, e.Version))
		}
	}
	if len(diff) > 0 {
		sort.Strings(diff)
		return fmt.Errorf("gtr.lock does not match gtr.json; run `gtr install` and commit gtr.lock:\n%s", strings.Join(diff, "\n"))
	}
	return nil
}

func (in *Installer) summary(old, cur *lock.Lock) {
	var added, removed, changed int
	for name, e := range cur.Packages {
		if p, ok := old.Packages[name]; !ok {
			added++
		} else if p.Version != e.Version || p.Commit != e.Commit {
			changed++
		}
	}
	for name := range old.Packages {
		if _, ok := cur.Packages[name]; !ok {
			removed++
		}
	}
	noun := "packages"
	if len(cur.Packages) == 1 {
		noun = "package"
	}
	fmt.Fprintf(in.log(), "%d %s installed (%d added, %d updated, %d removed)\n", len(cur.Packages), noun, added, changed, removed)
}

func sortedNames(m map[string]*resolve.Node) []string { return sortedKeys(m) }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
