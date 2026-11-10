package install

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"gtr-manager/internal/gomodules"
	"gtr-manager/internal/manifest"
	"gtr-manager/internal/npm"
	"gtr-manager/internal/semver"
	"gtr-manager/internal/spec"
)

// Add adds packages to gtr.json and installs. Accepted forms:
//
//	github:owner/repo[/subdir][#range]   name read from the package's gtr.json
//	gitlab:group/project[//subdir][#range]
//	git+https://host/repo.git[//subdir][#range]   (also git+ssh://, git+file://)
//	file:../path                         a local package
//	<name>=<source>                      explicit name, e.g. orm=github:shibisty/orm.go
//	npm:<name>[@range]                   an npm package for the frontend: npm:preact, npm:@preact/signals@^1
//
// Without a range, a GitHub package is added as ^<installed version>.
func (in *Installer) Add(ctx context.Context, args []string, dev bool) error {
	if len(args) == 0 {
		return fmt.Errorf("specify a package, e.g. `gtr add github:shibisty/orm.go`")
	}
	m, err := in.loadTarget()
	if err != nil {
		return err
	}
	target, other := m.Dependencies, m.DevDependencies
	if dev {
		target, other = m.DevDependencies, m.Dependencies
	}
	for _, arg := range args {
		name, value, err := in.parseAdd(ctx, arg)
		if err != nil {
			return err
		}
		if _, exists := other[name]; exists {
			delete(other, name) // moving between dependencies and devDependencies
		}
		if name == m.Name {
			return fmt.Errorf("%s cannot depend on itself", name)
		}
		target[name] = value
		fmt.Fprintf(in.log(), "Adding %s: %q\n", name, value)
	}
	return in.installTarget(ctx, m)
}

// loadTarget loads the gtr.json add and remove change: the member's in a
// workspace member, otherwise the project's.
func (in *Installer) loadTarget() (*manifest.Manifest, error) {
	if in.Member == "" {
		return in.Load()
	}
	m, err := manifest.Load(filepath.Join(in.Dir, filepath.FromSlash(in.Member), manifest.FileName))
	if err != nil {
		return nil, err
	}
	for _, w := range m.Warnings {
		fmt.Fprintf(in.log(), "warning: %s/gtr.json: %s\n", in.Member, w)
	}
	return m, nil
}

// installTarget installs with the changed manifest and saves it on success.
func (in *Installer) installTarget(ctx context.Context, m *manifest.Manifest) error {
	if in.Member == "" {
		return in.Install(ctx, Options{Manifest: m, SaveAfter: true})
	}
	return in.Install(ctx, Options{MemberManifests: map[string]*manifest.Manifest{in.Member: m}})
}

func (in *Installer) parseAdd(ctx context.Context, arg string) (name, value string, err error) {
	explicit := ""
	if n, v, ok := strings.Cut(arg, "="); ok && !strings.Contains(n, ":") {
		explicit, arg = n, v
	}
	switch {
	case strings.HasPrefix(arg, "npm:"):
		return in.parseAddNPM(ctx, strings.TrimPrefix(arg, "npm:"))
	case strings.HasPrefix(arg, "github:"), strings.HasPrefix(arg, "gitlab:"), strings.HasPrefix(arg, "git+"), strings.HasPrefix(arg, "file:"):
	case spec.IsGoModule(strings.SplitN(arg, "@", 2)[0]):
		return in.parseAddModule(ctx, arg, explicit)
	case explicit == "" && in.isMember(arg):
		return arg, "workspace:*", nil
	default:
		if explicit != "" {
			return "", "", fmt.Errorf("%s: a registry package is added by its name, e.g. `gtr add %s`", arg, explicit)
		}
		return in.parseAddRegistry(ctx, arg)
	}
	s, err := spec.Parse("package", arg)
	if err != nil {
		return "", "", err
	}
	probe := s
	if s.Kind == spec.File && in.Member != "" && !filepath.IsAbs(filepath.FromSlash(s.Path)) {
		probe.Path = in.Member + "/" + s.Path // file: paths in a member's gtr.json are relative to it
	}
	found, c, err := in.Discover(ctx, probe)
	if err != nil {
		return "", "", err
	}
	if explicit != "" && explicit != found {
		return "", "", fmt.Errorf("%s is named %q in its gtr.json, not %q", arg, found, explicit)
	}
	if s.IsGit() && s.Range == "*" && !c.Head {
		s.Range = caretRange(semver.MustParse(c.Version))
	}
	return found, s.String(), nil
}

// parseAddRegistry handles `gtr add orm[@range]`: a package of the gtr
// registry, added as ^<latest version> without a range.
func (in *Installer) parseAddRegistry(ctx context.Context, arg string) (string, string, error) {
	name, rng, _ := strings.Cut(arg, "@")
	if err := spec.ValidateName(name); err != nil {
		return "", "", fmt.Errorf("%s: %w; add other packages by their source, e.g. `gtr add github:owner/repo`", arg, err)
	}
	if rng != "" && rng != "latest" {
		if _, err := semver.ParseRange(rng); err != nil {
			return "", "", fmt.Errorf("%s: invalid version range %q", name, rng)
		}
		return name, rng, nil
	}
	p, err := in.Providers(spec.Registry)
	if err != nil {
		return "", "", err
	}
	cands, err := p.Candidates(ctx, name, spec.Spec{Kind: spec.Registry, Range: "*"})
	if err != nil {
		return "", "", err
	}
	for _, c := range cands { // highest first
		v := semver.MustParse(c.Version)
		if c.Yanked || v.Pre != "" {
			continue
		}
		return name, caretRange(v), nil
	}
	return "", "", fmt.Errorf("%s has no published version in the registry (only prereleases or yanked ones)", name)
}

// caretRange is the range `gtr add` writes for a version: ^1.2.3, and for
// 0.x the minor (^0.2 = >=0.2.0 <0.3.0).
func caretRange(v semver.Version) string {
	if v.Major == 0 && v.Minor != 0 {
		return fmt.Sprintf("^%d.%d", v.Major, v.Minor)
	}
	return "^" + v.String()
}

// parseAddNPM handles `gtr add npm:preact[@range]`: without a range the
// package is added as ^<latest version>.
func (in *Installer) parseAddNPM(ctx context.Context, arg string) (string, string, error) {
	name, rng := arg, ""
	if i := strings.LastIndex(arg, "@"); i > 0 { // "@scope/name@^1": the scope's @ is at 0
		name, rng = arg[:i], arg[i+1:]
	}
	if err := npm.ValidName(name); err != nil {
		return "", "", err
	}
	if rng != "" && rng != "latest" {
		if _, err := npm.ParseRange(rng); err != nil {
			return "", "", fmt.Errorf("%s: %w", name, err)
		}
		return name, "npm:" + rng, nil
	}
	p, err := in.npmRegistry().Packument(ctx, name)
	if err != nil {
		return "", "", err
	}
	r, _ := npm.ParseRange("*")
	rel, ok := p.Best(r)
	if !ok {
		return "", "", fmt.Errorf("npm: %s has no stable version", name)
	}
	return name, "npm:^" + rel.Version, nil
}

// parseAddModule handles `gtr add github.com/x/y[@range]`.
func (in *Installer) parseAddModule(ctx context.Context, arg, explicit string) (string, string, error) {
	path, rng, hasRange := strings.Cut(arg, "@")
	if explicit != "" && explicit != path {
		return "", "", fmt.Errorf("%s: the key of an external module is its module path", arg)
	}
	if err := spec.ValidateKey(path); err != nil {
		return "", "", err
	}
	if !hasRange || rng == "" || rng == "latest" {
		rng = "*"
	}
	if _, err := semver.ParseRange(strings.TrimPrefix(rng, "v")); err != nil {
		return "", "", fmt.Errorf("%s: invalid version range %q", path, rng)
	}
	r := &gomodules.Resolver{Proxies: in.proxies(), SumDB: in.sumDB()}
	res, err := r.Resolve(ctx, []gomodules.Requirement{{Path: path, Range: rng, By: "gtr add"}})
	if err != nil {
		return "", "", err
	}
	if rng == "*" {
		rng = ModuleRange(res.Versions[path])
	}
	return path, rng, nil
}

// Remove removes packages from gtr.json and reinstalls.
func (in *Installer) Remove(ctx context.Context, names []string) error {
	if len(names) == 0 {
		return fmt.Errorf("specify a package name")
	}
	m, err := in.loadTarget()
	if err != nil {
		return err
	}
	for _, name := range names {
		_, inDeps := m.Dependencies[name]
		_, inDev := m.DevDependencies[name]
		if !inDeps && !inDev {
			return fmt.Errorf("%s is not in gtr.json", name)
		}
		delete(m.Dependencies, name)
		delete(m.DevDependencies, name)
		fmt.Fprintf(in.log(), "Removing %s\n", name)
	}
	return in.installTarget(ctx, m)
}

// isMember reports whether name is a member of the workspace at in.Dir.
func (in *Installer) isMember(name string) bool {
	root, err := in.Load()
	if err != nil {
		return false
	}
	members, err := Members(in.Dir, root, nil)
	_, ok := members[name]
	return err == nil && ok
}
