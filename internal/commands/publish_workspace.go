package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gtr-manager/internal/install"
	"gtr-manager/internal/manifest"
	"gtr-manager/internal/registry"
	"gtr-manager/internal/semver"
)

// workspaceMembers returns the members of the workspace dir belongs to (nil
// outside a workspace).
func workspaceMembers(dir string) (map[string]*install.Member, error) {
	root, _, err := install.FindRoot(dir)
	if err != nil {
		return nil, nil
	}
	rm, err := manifest.Load(filepath.Join(root, manifest.FileName))
	if err != nil {
		return nil, err
	}
	return install.Members(root, rm, nil)
}

func private(m *manifest.Manifest) bool { return string(m.Raw("private")) == "true" }

// releaseManifest is the gtr.json that goes into the archive: dependencies
// on workspace members become ranges of the registry, the way pnpm
// publishes (ADR-0012):
//
//	"workspace:*"  → "1.2.3"     (the member's version)
//	"workspace:^"  → "^1.2.3"
//	"workspace:~"  → "~1.2.3"
//	"workspace:^1" → "^1"        (checked against the member's version)
//	"file:../core" → "^1.2.3"    (a path to a member)
//
// data is nil when nothing changes. bad lists sources nobody else could
// install: file: paths outside the workspace, private members.
// devDependencies are only installed in the package's own checkout, so they
// stay as they are.
func releaseManifest(p pkgDir, members map[string]*install.Member) (data []byte, changed, bad []string, err error) {
	m, err := manifest.Load(filepath.Join(p.Dir, manifest.FileName))
	if err != nil {
		return nil, nil, nil, err
	}
	for _, f := range []struct {
		field string
		deps  map[string]string
	}{{"dependencies", m.Dependencies}, {"peerDependencies", m.PeerDependencies}} {
		for _, name := range sortedKeys(f.deps) {
			v := f.deps[name]
			local := strings.HasPrefix(v, "workspace:") || strings.HasPrefix(v, "file:")
			if !local {
				continue
			}
			mem := members[name]
			if mem != nil && strings.HasPrefix(v, "file:") {
				target := filepath.Clean(filepath.Join(p.Dir, filepath.FromSlash(strings.TrimPrefix(v, "file:"))))
				if target != filepath.Clean(mem.Abs) {
					mem = nil // a path to somewhere else
				}
			}
			switch {
			case mem == nil:
				bad = append(bad, fmt.Sprintf("%s.%s: %q (not a member of this workspace)", f.field, name, v))
				continue
			case private(mem.Manifest):
				bad = append(bad, fmt.Sprintf("%s.%s: %q (the member %s is private)", f.field, name, v, mem.Dir))
				continue
			}
			rng := strings.TrimSpace(strings.TrimPrefix(v, "workspace:"))
			switch {
			case strings.HasPrefix(v, "file:"), rng == "^":
				rng = "^" + mem.Version
			case rng == "" || rng == "*":
				rng = mem.Version
			case rng == "~":
				rng = "~" + mem.Version
			default:
				r, err := semver.ParseRange(rng)
				if err != nil {
					return nil, nil, nil, fmt.Errorf("%s.%s: %q: invalid range", f.field, name, v)
				}
				if !r.Match(semver.MustParse(mem.Version)) {
					bad = append(bad, fmt.Sprintf("%s.%s: %q does not match the member's version %s", f.field, name, v, mem.Version))
					continue
				}
			}
			f.deps[name] = rng
			changed = append(changed, fmt.Sprintf("%s: %s → %s", name, v, rng))
		}
	}
	sort.Strings(bad)
	if len(changed) == 0 || len(bad) > 0 {
		return nil, changed, bad, nil
	}
	data, err = manifest.Marshal(m)
	return data, changed, bad, err
}

// publishWorkspace is `gtr publish -r`: every member that is not private and
// whose version the registry does not have yet, dependencies first.
func publishWorkspace(c *registry.Client, dryRun, skipTests, allowDirty bool) error {
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	dir := findManifestDir(wd)
	if dir == "" {
		return fmt.Errorf("no gtr.json in %s or above", wd)
	}
	root, _, err := install.FindRoot(dir)
	if err != nil {
		return err
	}
	members, err := workspaceMembers(root)
	if err != nil {
		return err
	}
	if len(members) == 0 {
		return errors.New("gtr publish -r publishes the members of a workspace; this is not one (run gtr publish)")
	}
	if c.Token == "" && !dryRun {
		return fmt.Errorf("not logged in to %s: run `gtr login --registry %s`", c.URL, c.URL)
	}

	var todo []*install.Member
	for _, mem := range memberOrder(members) {
		m := mem.Manifest
		switch {
		case private(m):
			fmt.Fprintf(Stderr, "- %s (%s): private\n", m.Name, mem.Dir)
			continue
		case !versionRe.MatchString(m.Version):
			return fmt.Errorf("%s/gtr.json: version %q is not a SemVer version such as 1.2.0", mem.Dir, m.Version)
		}
		if !dryRun || c.Token != "" {
			ctx, stop := signalContext()
			ix, err := c.Package(ctx, m.Name)
			stop()
			if err != nil && !registry.IsNotFound(err) {
				return err
			}
			published := false
			if ix != nil {
				for _, v := range ix.Versions {
					published = published || v.Version == m.Version
				}
			}
			if published {
				fmt.Fprintf(Stderr, "- %s %s: in the registry already\n", m.Name, m.Version)
				continue
			}
		}
		todo = append(todo, mem)
	}
	if len(todo) == 0 {
		fmt.Fprintln(Stdout, "Nothing to publish: every member's version is in the registry (bump them with `gtr version patch`)")
		return nil
	}
	// The repositories of what goes up must be clean: one for a monorepo,
	// or each member's own (members as git submodules of the workspace).
	if !allowDirty {
		checked := map[string]bool{}
		for _, mem := range todo {
			repo, err := findRepo(mem.Abs)
			if err != nil || checked[repo.top] {
				continue
			}
			checked[repo.top] = true
			d, err := repo.dirty()
			if err != nil {
				return err
			}
			if len(d) > 0 {
				rel, _ := filepath.Rel(root, repo.top)
				return fmt.Errorf("the repository %s has uncommitted changes (--allow-dirty to publish them anyway):\n  %s", filepath.ToSlash(rel), strings.Join(firstN(d, 8), "\n  "))
			}
		}
		if len(checked) > 0 {
			fmt.Fprintf(Stderr, "✓ %d repositories clean\n", len(checked))
		}
	}
	var names []string
	for _, mem := range todo {
		names = append(names, mem.Manifest.Name+" "+mem.Manifest.Version)
	}
	fmt.Fprintf(Stderr, "✓ to publish, in this order: %s\n", strings.Join(names, ", "))

	for _, mem := range todo {
		p := pkgDir{Dir: mem.Abs, Label: mem.Dir, M: mem.Manifest}
		fmt.Fprintf(Stderr, "\n%s %s (%s)\n", p.M.Name, p.M.Version, mem.Dir)
		if err := publishOne(p, c, members, dryRun, skipTests); err != nil {
			return fmt.Errorf("%s: %w", p.M.Name, err)
		}
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
