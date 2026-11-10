package install

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gtr-manager/internal/lock"
	"gtr-manager/internal/npm"
	"gtr-manager/internal/resolve"
	"gtr-manager/internal/spec"
)

// npmRegistry is the registry of npm: dependencies: Installer.NPM, or the
// public one (GTR_NPM_REGISTRY and GTR_NPM_TOKEN change it).
func (in *Installer) npmRegistry() *npm.Registry {
	if in.NPM != nil {
		return in.NPM
	}
	in.NPM = &npm.Registry{URL: os.Getenv("GTR_NPM_REGISTRY"), Token: os.Getenv("GTR_NPM_TOKEN")}
	return in.NPM
}

// npmDeps collects the npm packages the project and its gtr packages need:
// the project's own range wins; ranges of several packages are combined.
func npmDeps(root map[string]spec.Spec, nodes map[string]*resolve.Node) map[string]string {
	out := map[string]string{}
	fromRoot := map[string]bool{}
	for name, s := range root {
		if s.Kind == spec.NPM {
			out[name], fromRoot[name] = s.Range, true
		}
	}
	for _, n := range sortedKeys(nodes) {
		for name, s := range nodes[n].NPMDeps {
			switch have, ok := out[name]; {
			case !ok:
				out[name] = s.Range
			case fromRoot[name] || have == s.Range || s.Range == "*":
			case have == "*":
				out[name] = s.Range
			case !strings.Contains(have, "||") && !strings.Contains(s.Range, "||"):
				out[name] = have + " " + s.Range // both must hold
			}
		}
	}
	return out
}

// installNPM resolves and installs the npm packages into node_modules and
// records them in the new lock.
func (in *Installer) installNPM(ctx context.Context, deps map[string]string, old, cur *lock.Lock, opts Options) error {
	if len(deps) == 0 && len(old.NPM) == 0 {
		return nil
	}
	locked := map[string]*npm.Package{}
	for p, e := range old.NPM {
		locked[p] = fromLock(p, e)
	}
	update := map[string]bool{}
	for name := range opts.Update {
		update[name] = true
	}
	r := in.npmRegistry()
	tree, err := r.Resolve(ctx, npm.Request{Deps: deps, Locked: locked, Update: update, Frozen: opts.Frozen})
	if err != nil {
		return err
	}
	if len(deps) == 0 {
		tree = map[string]*npm.Package{}
	}
	if err := r.Install(ctx, in.Dir, filepath.Join(in.Home.Root, "store", "npm"), tree, in.Offline); err != nil {
		return err
	}
	if len(tree) > 0 {
		cur.NPM = map[string]lock.Entry{}
		for p, pkg := range tree {
			cur.NPM[p] = toLock(pkg)
		}
		fmt.Fprintf(in.log(), "%d npm %s in node_modules\n", len(tree), plural(len(tree), "package", "packages"))
	}
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func toLock(p *npm.Package) lock.Entry {
	e := lock.Entry{Version: p.Version, Source: "npm:" + p.Name, Tarball: p.Tarball, Integrity: p.Integrity}
	if len(p.Dependencies) > 0 {
		e.Dependencies = p.Dependencies
	}
	for name := range p.Optional {
		e.Optional = append(e.Optional, name)
	}
	sort.Strings(e.Optional)
	return e
}

func fromLock(path string, e lock.Entry) *npm.Package {
	name := strings.TrimPrefix(e.Source, "npm:")
	if name == e.Source || name == "" { // older entries: the name from the path
		i := strings.LastIndex(path, "node_modules/")
		name = path[i+len("node_modules/"):]
	}
	p := &npm.Package{Path: path, Name: name, Version: e.Version, Tarball: e.Tarball, Integrity: e.Integrity,
		Dependencies: map[string]string{}, Optional: map[string]bool{}}
	for k, v := range e.Dependencies {
		p.Dependencies[k] = v
	}
	for _, o := range e.Optional {
		p.Optional[o] = true
	}
	return p
}
