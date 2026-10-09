package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gtr-manager/internal/gomod"
	"gtr-manager/internal/gomodules"
	"gtr-manager/internal/manifest"
	"gtr-manager/internal/resolve"
	"gtr-manager/internal/semver"
	"gtr-manager/internal/source"
	"gtr-manager/internal/spec"
)

// Member is a workspace member (ADR-0009).
type Member struct {
	Dir      string // relative to the workspace root, slash-separated: "drivers/mysql"
	Abs      string
	Manifest *manifest.Manifest
	Version  string // normalized SemVer of the member ("0.0.0" if unset)
}

// Spec is the resolver's source for the member.
func (m *Member) Spec() spec.Spec {
	return spec.Spec{Kind: spec.Workspace, Path: m.Dir, Range: "*", Raw: "workspace:*"}
}

// Members expands the "workspaces" patterns of the root manifest into the
// members, keyed by package name. overrides replaces members' manifests
// (by Dir) with versions not yet saved (gtr add in a member).
func Members(root string, m *manifest.Manifest, overrides map[string]*manifest.Manifest) (map[string]*Member, error) {
	patterns, err := m.Workspaces()
	if err != nil || len(patterns) == 0 {
		return nil, err
	}
	include, exclude := map[string]bool{}, map[string]bool{}
	fsys := os.DirFS(root)
	for _, p := range patterns {
		neg := strings.HasPrefix(p, "!")
		p = strings.TrimPrefix(p, "!")
		clean := path.Clean(strings.ReplaceAll(p, `\`, "/"))
		switch {
		case p == "" || path.IsAbs(clean) || filepath.IsAbs(p) || filepath.VolumeName(p) != "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../"):
			return nil, fmt.Errorf(`gtr.json: workspaces: %q must be a directory inside the workspace`, p)
		case strings.Contains(p, "**"):
			return nil, fmt.Errorf(`gtr.json: workspaces: %q: "**" is not supported; list each level ("drivers/*")`, p)
		}
		// Relative to the root, so brackets in the root path are not a pattern.
		matches, err := fs.Glob(fsys, clean)
		if err != nil {
			return nil, fmt.Errorf("gtr.json: workspaces: %q: %w", p, err)
		}
		literal := !strings.ContainsAny(clean, "*?[")
		if neg {
			for _, rel := range matches {
				exclude[rel] = true
			}
			continue
		}
		if literal && len(matches) == 0 {
			return nil, fmt.Errorf("gtr.json: workspaces: directory %q not found", p)
		}
		for _, rel := range matches {
			abs := filepath.Join(root, filepath.FromSlash(rel))
			st, err := os.Lstat(abs)
			switch {
			case err != nil || !st.IsDir() && st.Mode()&os.ModeSymlink == 0:
				if literal {
					return nil, fmt.Errorf("gtr.json: workspaces: %q is not a directory", p)
				}
				continue
			case st.Mode()&os.ModeSymlink != 0 || skipDir(rel):
				// gtr_modules links into the store, hidden directories and
				// links are never members.
				if literal {
					return nil, fmt.Errorf("gtr.json: workspaces: %q cannot be a member (a link, a hidden directory or inside %s)", p, ModulesDir)
				}
				continue
			}
			if _, err := os.Stat(filepath.Join(abs, manifest.FileName)); err != nil {
				if literal {
					return nil, fmt.Errorf("gtr.json: workspaces: %q has no gtr.json", p)
				}
				continue // a glob may match other directories
			}
			include[rel] = true
		}
	}
	dirs := map[string]bool{}
	for rel := range include {
		if !exclude[rel] {
			dirs[rel] = true
		}
	}
	members := map[string]*Member{}
	for _, dir := range sortedKeys(dirs) {
		mm := overrides[dir]
		if mm == nil {
			if mm, err = manifest.Load(filepath.Join(root, filepath.FromSlash(dir), manifest.FileName)); err != nil {
				return nil, err
			}
		}
		if err := spec.ValidateName(mm.Name); err != nil {
			return nil, fmt.Errorf("%s/gtr.json: %w", dir, err)
		}
		if nested, _ := mm.Workspaces(); len(nested) > 0 {
			return nil, fmt.Errorf("%s/gtr.json: a workspace member cannot be a workspace itself", dir)
		}
		if mm.Name == m.Name {
			return nil, fmt.Errorf("%s/gtr.json: the member is named %q like the workspace root", dir, mm.Name)
		}
		if other, dup := members[mm.Name]; dup {
			return nil, fmt.Errorf("two workspace members are named %q: %s and %s", mm.Name, other.Dir, dir)
		}
		v := "0.0.0"
		if mm.Version != "" {
			pv, err := semver.Parse(mm.Version)
			if err != nil {
				return nil, fmt.Errorf("%s/gtr.json: invalid version %q", dir, mm.Version)
			}
			v = pv.String()
		}
		members[mm.Name] = &Member{Dir: dir, Abs: filepath.Join(root, filepath.FromSlash(dir)), Manifest: mm, Version: v}
	}
	return members, nil
}

// skipDir reports whether a workspace-relative directory is under
// gtr_modules or hidden.
func skipDir(rel string) bool {
	for _, el := range strings.Split(rel, "/") {
		if el == ModulesDir || strings.HasPrefix(el, ".") {
			return true
		}
	}
	return false
}

// mayMatch reports whether a directory (relative to the root) is named by
// an including pattern, without reading any member.
func mayMatch(patterns []string, rel string) bool {
	for _, p := range patterns {
		if strings.HasPrefix(p, "!") {
			continue
		}
		if ok, _ := path.Match(path.Clean(strings.ReplaceAll(p, `\`, "/")), rel); ok {
			return true
		}
	}
	return false
}

// FindRoot returns the workspace root that dir belongs to and dir's member
// directory relative to it ("" for the root itself). Outside a workspace it
// returns dir and "".
func FindRoot(dir string) (root, member string, err error) {
	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	if m, err := manifest.Load(filepath.Join(dir, manifest.FileName)); err == nil {
		if ws, _ := m.Workspaces(); len(ws) > 0 {
			return dir, "", nil
		}
	}
	for up := filepath.Dir(dir); ; up = filepath.Dir(up) {
		// A broken gtr.json above the project matters only if the project
		// is one of its members.
		if m, err := manifest.Load(filepath.Join(up, manifest.FileName)); err == nil {
			if ws, _ := m.Workspaces(); len(ws) > 0 {
				rel, _ := filepath.Rel(up, dir)
				rel = filepath.ToSlash(rel)
				members, err := Members(up, m, nil)
				if err != nil {
					if mayMatch(ws, rel) {
						return "", "", fmt.Errorf("workspace %s: %w", up, err)
					}
				} else {
					for _, mem := range members {
						if mem.Dir == rel {
							return up, mem.Dir, nil
						}
					}
				}
			}
		}
		if filepath.Dir(up) == up {
			return dir, "", nil
		}
	}
}

// workspaceSource serves members to the resolver.
type workspaceSource struct {
	byDir map[string]*Member
}

func newWorkspaceSource(members map[string]*Member) *workspaceSource {
	w := &workspaceSource{byDir: map[string]*Member{}}
	for _, m := range members {
		w.byDir[m.Dir] = m
	}
	return w
}

func (w *workspaceSource) member(s spec.Spec) (*Member, error) {
	if m, ok := w.byDir[s.Path]; ok {
		return m, nil
	}
	return nil, fmt.Errorf("no workspace member in %q", s.Path)
}

func (w *workspaceSource) Candidates(_ context.Context, _ string, s spec.Spec) ([]source.Candidate, error) {
	m, err := w.member(s)
	if err != nil {
		return nil, err
	}
	return []source.Candidate{{Version: m.Version}}, nil
}

func (w *workspaceSource) Manifest(_ context.Context, s spec.Spec, _ source.Candidate) (*manifest.Manifest, error) {
	m, err := w.member(s)
	if err != nil {
		return nil, err
	}
	return m.Manifest, nil
}

func (w *workspaceSource) Fetch(context.Context, spec.Spec, source.Candidate, string) error {
	return errors.New("workspace members are used in place")
}

// memberDirs lists members' directories, sorted.
func memberDirs(members map[string]*Member) []string {
	var out []string
	for _, m := range members {
		out = append(out, m.Dir)
	}
	sort.Strings(out)
	return out
}

// checkGenerated refuses to overwrite a go.mod (or go.work) that gtr did not
// generate, or that was edited since, unless force is set.
func checkGenerated(root string, members map[string]*Member, force bool) error {
	if force {
		return nil
	}
	state, err := gomod.Check(filepath.Join(root, "go.mod"))
	switch {
	case err != nil:
		return err
	case state == gomod.Foreign:
		// A file: package that gtr gave a go.mod (gomod.PackageMod) is fine to replace.
		return fmt.Errorf("go.mod exists and was not generated by gtr; run `gtr sync` to import its requirements into gtr.json, or `gtr sync --force` to replace it")
	case state == gomod.Edited:
		return fmt.Errorf("go.mod was edited by hand since gtr generated it; run `gtr sync` to carry the edits over to gtr.json, or `gtr sync --force` to discard them")
	}
	if len(members) == 0 {
		return nil
	}
	var problems []string
	files := []string{"go.work"}
	for _, dir := range memberDirs(members) {
		files = append(files, dir+"/go.mod")
	}
	for _, f := range files {
		state, err := gomod.Check(filepath.Join(root, filepath.FromSlash(f)))
		if err != nil {
			return err
		}
		switch state {
		case gomod.Foreign:
			problems = append(problems, f+" was not generated by gtr")
		case gomod.Edited:
			problems = append(problems, f+" was edited by hand since gtr generated it")
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s; in a workspace gtr generates these files (ADR-0009): move them away, or run `gtr sync --force` to replace them", strings.Join(problems, "; "))
	}
	return nil
}

// installed drops workspace members: they are used in place, not stored,
// linked or locked.
func installed(nodes map[string]*resolve.Node) map[string]*resolve.Node {
	out := make(map[string]*resolve.Node, len(nodes))
	for name, n := range nodes {
		if n.Spec.Kind != spec.Workspace {
			out[name] = n
		}
	}
	return out
}

// cleanFormerMembers removes the go.mod generated for directories that were
// members at the last install and are not any more (a go.mod rewritten for
// a file: package, or edited by hand, is kept).
func (in *Installer) cleanFormerMembers(before []string, members map[string]*Member) error {
	now := map[string]bool{}
	for _, m := range members {
		now[m.Dir] = true
	}
	for _, dir := range before {
		if now[dir] || skipDir(dir) || strings.HasPrefix(path.Clean(dir), "..") {
			continue
		}
		p := filepath.Join(in.Dir, filepath.FromSlash(dir), "go.mod")
		if st, _ := gomod.Check(p); st == gomod.Generated {
			if err := os.Remove(p); err != nil {
				return err
			}
			fmt.Fprintf(in.log(), "Removed %s/go.mod (no longer a workspace member)\n", dir)
		}
	}
	return nil
}

// writeWorkspace generates go.work and the go.mod of the root and of every
// member (ADR-0009, item 4). In workspace mode replacements live in go.work.
func (in *Installer) writeWorkspace(m *manifest.Manifest, rootDeps map[string]spec.Spec, nodes map[string]*resolve.Node,
	mods *gomodules.Result, members map[string]*Member) error {
	goVer := goDirective(m, nodes, mods)
	work := gomod.Work{Go: goVer, Use: []string{"."}, Replace: map[string]string{}}
	for _, dir := range memberDirs(members) {
		work.Use = append(work.Use, "./"+dir)
	}
	for name := range installed(nodes) {
		work.Replace[name] = "./" + ModulesDir + "/" + name
	}
	for _, mp := range sortedKeys(mods.Versions) {
		dn, err := gomodules.DirName(mp, mods.Versions[mp])
		if err != nil {
			return err
		}
		work.Replace[mp] = "./" + ModulesDir + "/" + GoDir + "/" + dn
	}
	// closure is everything a module needs: its dependencies and theirs. A
	// member lists only that (not the whole workspace build list), so its
	// go.mod stays valid where the same directory is used as a file:
	// package by another workspace.
	closure := func(start map[string]bool) map[string]bool {
		seen := map[string]bool{}
		var visit func(n string)
		visit = func(n string) {
			if seen[n] {
				return
			}
			seen[n] = true
			if node := nodes[n]; node != nil {
				for _, d := range sortedKeys(node.Deps) {
					visit(d)
				}
				for _, d := range sortedKeys(node.GoDeps) {
					visit(d)
				}
				for _, d := range sortedKeys(node.Manifest.PeerDependencies) {
					if nodes[d] != nil {
						visit(d)
					}
				}
			} else if mf, ok := mods.Mods[n]; ok {
				for _, rp := range sortedKeys(mf.Require) {
					if _, selected := mods.Versions[rp]; selected {
						visit(rp)
					}
				}
			}
		}
		for _, n := range sortedKeys(start) {
			visit(n)
		}
		return seen
	}
	module := func(dir, name string, direct map[string]bool) error {
		need := closure(direct)
		p := gomod.Project{Module: name, Go: goVer, Replace: map[string]string{}}
		for _, dn := range sortedNames(nodes) {
			n := nodes[dn]
			r := gomod.Require{Path: dn, Version: "v" + n.Candidate.Version}
			switch {
			case n.Spec.Kind == spec.Workspace || !need[dn]:
				// Members come from go.work's use; a versioned requirement on
				// one makes go look the version up in the module proxy.
			case direct[dn]:
				p.Direct = append(p.Direct, r)
			default:
				p.Indirect = append(p.Indirect, r)
			}
		}
		for _, mp := range sortedKeys(mods.Versions) {
			r := gomod.Require{Path: mp, Version: mods.Versions[mp]}
			switch {
			case !need[mp]:
			case direct[mp]:
				p.Direct = append(p.Direct, r)
			default:
				p.Indirect = append(p.Indirect, r)
			}
		}
		return writeIfDifferent(filepath.Join(dir, "go.mod"), p.Render())
	}
	direct := map[string]bool{}
	for name := range rootDeps {
		direct[name] = true
	}
	if err := module(in.Dir, m.Name, direct); err != nil {
		return err
	}
	for _, name := range sortedKeys(members) {
		n := nodes[name]
		direct := map[string]bool{}
		for dn := range n.Deps {
			direct[dn] = true
		}
		for dn := range n.GoDeps {
			direct[dn] = true
		}
		if err := module(members[name].Abs, name, direct); err != nil {
			return err
		}
	}
	return writeIfDifferent(in.path("go.work"), work.Render())
}

func writeIfDifferent(path string, content []byte) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, content) {
		return nil
	}
	return manifest.WriteFileAtomic(path, content)
}
