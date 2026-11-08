package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gtr-manager/internal/gomodules"
	"gtr-manager/internal/lock"
	"gtr-manager/internal/manifest"
	"gtr-manager/internal/semver"
	"gtr-manager/internal/source"
	"gtr-manager/internal/spec"
)

// State is an installed project as gtr.json, gtr.lock and the workspace
// members describe it; reading it needs no network (gtr list, gtr why).
type State struct {
	Manifest *manifest.Manifest
	Members  map[string]*Member
	Lock     *lock.Lock
	dir      string
}

// State reads the installed project (the whole workspace in a workspace).
func (in *Installer) State() (*State, error) {
	m, err := in.Load()
	if err != nil {
		return nil, err
	}
	members, err := Members(in.Dir, m, nil)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(in.path(lock.FileName)); err != nil {
		return nil, errors.New("gtr.lock not found: run `gtr install` first")
	}
	l, err := lock.Load(in.path(lock.FileName))
	if err != nil {
		return nil, err
	}
	return &State{Manifest: m, Members: members, Lock: l, dir: in.Dir}, nil
}

// Root is a starting point of the dependency graph: the project or a
// workspace member.
type Root struct {
	Name, Version string
	Dir           string // relative to the project, "." for the project itself
	Deps          map[string]string
	DevDeps       map[string]string
}

// Roots returns the project followed by the members, by directory.
func (s *State) Roots() []Root {
	roots := []Root{{Name: s.Manifest.Name, Version: s.Manifest.Version, Dir: ".", Deps: s.Manifest.Dependencies, DevDeps: s.Manifest.DevDependencies}}
	for _, name := range sortedMembers(s.Members) {
		mem := s.Members[name]
		roots = append(roots, Root{Name: name, Version: mem.Version, Dir: mem.Dir, Deps: mem.Manifest.Dependencies, DevDeps: mem.Manifest.DevDependencies})
	}
	return roots
}

func sortedMembers(members map[string]*Member) []string {
	names := sortedKeys(members)
	sort.SliceStable(names, func(i, j int) bool { return members[names[i]].Dir < members[names[j]].Dir })
	return names
}

// Node is a package in the tree `gtr list` prints.
type Node struct {
	Name     string  `json:"name"`
	Version  string  `json:"version,omitempty"`
	Source   string  `json:"source,omitempty"` // "github:o/r", "go", "workspace:core"
	Dev      bool    `json:"dev,omitempty"`
	Missing  bool    `json:"missing,omitempty"` // in gtr.json, not in gtr.lock
	Deduped  bool    `json:"deduped,omitempty"` // children shown earlier
	Children []*Node `json:"dependencies,omitempty"`
}

// Tree builds the dependency tree of every root to the given depth (0: the
// direct dependencies only; negative: unlimited). A package's dependencies
// are expanded once; later occurrences are marked Deduped.
func (s *State) Tree(depth int) []*Node {
	var out []*Node
	expanded := map[string]bool{}
	for _, r := range s.Roots() {
		root := &Node{Name: r.Name, Version: r.Version}
		if r.Dir != "." {
			root.Source = "workspace:" + r.Dir
		}
		for _, deps := range []struct {
			m   map[string]string
			dev bool
		}{{r.Deps, false}, {r.DevDeps, true}} {
			for _, name := range sortedKeys(deps.m) {
				n := s.node(name)
				n.Dev = deps.dev
				root.Children = append(root.Children, n)
				s.expand(n, depth, 1, expanded)
			}
		}
		out = append(out, root)
	}
	return out
}

func (s *State) node(name string) *Node {
	if mem, ok := s.Members[name]; ok {
		return &Node{Name: name, Version: mem.Version, Source: "workspace:" + mem.Dir}
	}
	e, ok := s.Lock.Packages[name]
	if !ok {
		return &Node{Name: name, Missing: true}
	}
	return &Node{Name: name, Version: e.Version, Source: e.Source}
}

func (s *State) expand(n *Node, depth, level int, expanded map[string]bool) {
	if n.Missing || strings.HasPrefix(n.Source, "workspace:") || depth >= 0 && level > depth {
		return
	}
	deps := s.Lock.Packages[n.Name].Dependencies
	if len(deps) == 0 {
		return
	}
	if expanded[n.Name] {
		n.Deduped = true
		return
	}
	expanded[n.Name] = true
	for _, dn := range sortedKeys(deps) {
		if _, ok := s.Lock.Packages[dn]; !ok && s.Members[dn] == nil {
			continue // a module requirement outside the build list
		}
		c := s.node(dn)
		n.Children = append(n.Children, c)
		s.expand(c, depth, level+1, expanded)
	}
}

// Link is one step of a path to a package: from requires to with spec.
type Link struct {
	From, To string
	Spec     string // the requirement as written: "github:o/r#^0.1", "^0.1", "v1.2.3"
	Peer     bool
}

// Why returns the paths from the roots to name (at most limit), shortest
// first.
func (s *State) Why(name string, limit int) ([][]Link, error) {
	if _, ok := s.Lock.Packages[name]; !ok && s.Members[name] == nil {
		return nil, fmt.Errorf("%s is not installed", name)
	}
	edges := s.edges()
	// Only nodes from which name can be reached are worth entering: module
	// graphs have dense cycles (golang.org/x/... require each other), and
	// walking all their paths would never end.
	reach := map[string]bool{name: true}
	for changed := true; changed; {
		changed = false
		for from, links := range edges {
			if reach[from] {
				continue
			}
			for _, l := range links {
				if reach[l.To] {
					reach[from], changed = true, true
					break
				}
			}
		}
	}
	var paths [][]Link
	budget := 200000 // steps; a pathological graph gives fewer chains, not a hang
	var walk func(at string, path []Link, seen map[string]bool)
	walk = func(at string, path []Link, seen map[string]bool) {
		for _, e := range edges[at] {
			if len(paths) >= limit*4 || budget <= 0 {
				return
			}
			budget--
			if seen[e.To] || !reach[e.To] {
				continue
			}
			p := append(append([]Link(nil), path...), e)
			if e.To == name {
				paths = append(paths, p)
				continue
			}
			seen[e.To] = true
			walk(e.To, p, seen)
			delete(seen, e.To)
		}
	}
	for _, r := range s.Roots() {
		walk("\x00"+r.Dir, nil, map[string]bool{})
	}
	sort.SliceStable(paths, func(i, j int) bool { return len(paths[i]) < len(paths[j]) })
	if len(paths) > limit {
		paths = paths[:limit]
	}
	return paths, nil
}

// edges returns the requirement graph; roots are keyed "\x00<dir>".
func (s *State) edges() map[string][]Link {
	out := map[string][]Link{}
	for _, r := range s.Roots() {
		from := "\x00" + r.Dir
		for _, deps := range []map[string]string{r.Deps, r.DevDeps} {
			for _, dn := range sortedKeys(deps) {
				out[from] = append(out[from], Link{From: r.Name, To: dn, Spec: deps[dn]})
			}
		}
		if mem, ok := s.Members[r.Name]; ok {
			for _, pn := range sortedKeys(mem.Manifest.PeerDependencies) {
				out[r.Name] = append(out[r.Name], Link{From: r.Name, To: pn, Spec: mem.Manifest.PeerDependencies[pn], Peer: true})
			}
			out[r.Name] = append(out[r.Name], out[from]...)
		}
	}
	for _, name := range sortedKeys(s.Lock.Packages) {
		e := s.Lock.Packages[name]
		for _, dn := range sortedKeys(e.Dependencies) {
			out[name] = append(out[name], Link{From: name, To: dn, Spec: e.Dependencies[dn]})
		}
		if e.Source == goSource {
			continue
		}
		// Peers are not installed by the package, but they explain why it
		// needs the version it gets.
		if m, err := manifest.Load(filepath.Join(s.dir, ModulesDir, name, manifest.FileName)); err == nil {
			for _, pn := range sortedKeys(m.PeerDependencies) {
				if _, dup := e.Dependencies[pn]; !dup {
					out[name] = append(out[name], Link{From: name, To: pn, Spec: m.PeerDependencies[pn], Peer: true})
				}
			}
		}
	}
	return out
}

// Outdated is a package with newer versions.
type Outdated struct {
	Name    string
	Current string
	Wanted  string // the newest version every range in the graph allows ("" if unknown)
	Latest  string // the newest version
	Source  string
	Dev     bool
	Direct  bool
}

// Outdated checks the packages for newer versions: the direct dependencies
// of the project and the members, and with all every installed package.
func (in *Installer) Outdated(ctx context.Context, s *State, all bool) ([]Outdated, error) {
	direct, dev := map[string]bool{}, map[string]bool{}
	for _, r := range s.Roots() {
		for dn := range r.Deps {
			direct[dn] = true
			dev[dn] = false
		}
		for dn := range r.DevDeps {
			if !direct[dn] {
				dev[dn] = true
			}
			direct[dn] = true
		}
	}
	// Ranges that apply to each package: from gtr.json files, the gtr
	// packages that depend on it and peer dependencies (go.mod minimums of
	// modules are not ranges and do not limit "wanted").
	ranges := map[string][]string{}
	for _, links := range s.edges() {
		for _, l := range links {
			if s.Lock.Packages[l.From].Source == goSource {
				continue
			}
			if sp, err := spec.Parse(l.To, l.Spec); err == nil {
				ranges[l.To] = append(ranges[l.To], sp.Range)
			}
		}
	}
	var out []Outdated
	for _, name := range sortedKeys(s.Lock.Packages) {
		if !all && !direct[name] {
			continue
		}
		e := s.Lock.Packages[name]
		row := Outdated{Name: name, Current: e.Version, Source: e.Source, Dev: dev[name], Direct: direct[name]}
		var err error
		if e.Source == goSource {
			row.Wanted, row.Latest, err = in.moduleVersions(ctx, name, ranges[name])
		} else {
			row.Wanted, row.Latest, err = in.packageVersions(ctx, name, e, ranges[name])
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if row.Latest == "" || row.Latest == row.Current && (row.Wanted == "" || row.Wanted == row.Current) {
			continue
		}
		out = append(out, row)
	}
	return out, nil
}

// packageVersions lists a gtr package's versions from its source.
func (in *Installer) packageVersions(ctx context.Context, name string, e lock.Entry, rngs []string) (wanted, latest string, err error) {
	s, err := spec.Parse(name, e.Source)
	if err != nil {
		return "", "", err
	}
	if s.Kind == spec.File || s.Kind == spec.Workspace {
		return "", "", nil // used in place
	}
	p, err := in.Providers(s.Kind)
	if err != nil {
		return "", "", err
	}
	cands, err := p.Candidates(ctx, name, s)
	if err != nil {
		return "", "", err
	}
	var all []source.Candidate
	for _, c := range cands {
		if v, err := semver.Parse(c.Version); err == nil && (v.Pre == "" || c.Head) {
			all = append(all, c)
		}
	}
	if len(all) == 0 {
		return "", "", nil
	}
	latest = all[0].Version // highest first
	if all[0].Head {
		if all[0].Commit != e.Commit {
			return all[0].Version, all[0].Version, nil
		}
		return e.Version, e.Version, nil
	}
	for _, c := range all {
		if fitsAll(c.Version, rngs) {
			return c.Version, latest, nil
		}
	}
	return "", latest, nil
}

// moduleVersions lists an external module's versions from the proxy.
func (in *Installer) moduleVersions(ctx context.Context, path string, rngs []string) (wanted, latest string, err error) {
	proxy, err := in.proxies().For(path)
	if err != nil {
		return "", "", err
	}
	versions, err := proxy.List(ctx, path)
	if err != nil {
		return "", "", err
	}
	var stable []string
	for _, v := range versions {
		if pv, err := gomodules.Parse(v); err == nil && pv.Pre == "" && !strings.HasSuffix(v, "+incompatible") {
			stable = append(stable, v)
		}
	}
	sort.Slice(stable, func(i, j int) bool { return gomodules.Compare(stable[i], stable[j]) > 0 })
	if len(stable) == 0 {
		return "", "", nil
	}
	if len(rngs) == 0 {
		return "", stable[0], nil // only go.mod requirements: nothing in gtr.json to move
	}
	for _, v := range stable {
		if fitsAll(strings.TrimPrefix(v, "v"), rngs) {
			return v, stable[0], nil
		}
	}
	return "", stable[0], nil
}

func fitsAll(version string, rngs []string) bool {
	v, err := semver.Parse(version)
	if err != nil {
		return false
	}
	for _, r := range rngs {
		rng, err := semver.ParseRange(r)
		if err != nil || !rng.Match(v) {
			return false
		}
	}
	return true
}
