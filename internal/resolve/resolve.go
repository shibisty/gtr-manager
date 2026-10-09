// Package resolve selects one version of every package in the dependency
// graph (ADR-0004).
//
// It is a backtracking search: packages are decided one at a time, the
// highest version satisfying every range collected so far is tried first
// (the locked version, if it still fits, before that), and a choice whose
// dependencies conflict with earlier choices is undone. When nothing fits,
// the error names every range that applies to the failing package and who
// required it.
//
// peerDependencies are not installed: they only constrain a package that is
// already in the graph, and a missing peer is an error after resolution.
package resolve

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"gtr-manager/internal/lock"
	"gtr-manager/internal/manifest"
	"gtr-manager/internal/semver"
	"gtr-manager/internal/source"
	"gtr-manager/internal/spec"
)

// MaxSteps bounds the search.
const MaxSteps = 20000

// Providers returns the provider for a spec kind, or an error explaining
// why the kind cannot be installed.
type Providers func(spec.Kind) (source.Provider, error)

// Node is a resolved package.
type Node struct {
	Name      string
	Spec      spec.Spec
	Candidate source.Candidate
	Manifest  *manifest.Manifest
	Deps      map[string]spec.Spec // its gtr dependencies (not devDependencies)
	GoDeps    map[string]spec.Spec // its external Go module dependencies
	Root      bool                 // a direct dependency of the project
}

// Request is one input of the resolver.
type Request struct {
	Root   map[string]spec.Spec // the project's dependencies and devDependencies
	Lock   *lock.Lock           // previous resolution, may be nil
	Update map[string]bool      // names whose locked version is ignored; "*" = all
	// Members are the workspace members (ADR-0009): name → a Workspace spec
	// whose Path is the member directory relative to the root. A member
	// replaces every other source of its name; ranges still apply.
	Members map[string]spec.Spec
}

type requirement struct {
	rng  semver.Range
	text string
	by   string // "the project" or "orm-mysql@0.3.0"
	spec spec.Spec
}

type resolver struct {
	ctx       context.Context
	providers Providers
	req       Request
	chosen    map[string]*Node
	reqs      map[string][]requirement
	cands     map[string][]source.Candidate
	manifests map[string]*manifest.Manifest
	steps     int
	conflict  error
	skipped   []string // versions without gtr.json
}

// Resolve resolves the graph.
func Resolve(ctx context.Context, providers Providers, req Request) (map[string]*Node, error) {
	r := &resolver{ctx: ctx, providers: providers, req: req, chosen: map[string]*Node{},
		reqs: map[string][]requirement{}, cands: map[string][]source.Candidate{}, manifests: map[string]*manifest.Manifest{}}
	var names []string
	for name, s := range req.Root {
		by := "the project"
		if _, member := req.Members[name]; member && s.Kind == spec.Workspace {
			by = "the workspace"
		}
		r.reqs[name] = append(r.reqs[name], requirement{rng: mustRange(s.Range), text: s.Range, by: by, spec: s})
		names = append(names, name)
	}
	sort.Strings(names)
	ok, err := r.solve(names)
	if err != nil {
		return nil, err
	}
	if !ok {
		if r.conflict != nil {
			return nil, r.conflict
		}
		return nil, errors.New("no solution")
	}
	for name := range req.Root {
		r.chosen[name].Root = true
	}
	if err := checkPeers(r.chosen); err != nil {
		return nil, err
	}
	return r.chosen, nil
}

func mustRange(s string) semver.Range {
	rng, err := semver.ParseRange(s)
	if err != nil {
		rng, _ = semver.ParseRange("*")
	}
	return rng
}

// solve decides the pending names. It returns false (without error) when
// this branch has no solution; errors are fatal (network, invalid data).
func (r *resolver) solve(pending []string) (bool, error) {
	// Pick the first undecided name.
	var name string
	rest := pending
	for len(rest) > 0 {
		if _, done := r.chosen[rest[0]]; !done {
			name = rest[0]
			rest = rest[1:]
			break
		}
		rest = rest[1:]
	}
	if name == "" {
		return true, nil
	}
	r.steps++
	if r.steps > MaxSteps {
		return false, fmt.Errorf("dependency resolution gave up after %d steps; pin versions in gtr.json to narrow the search", MaxSteps)
	}

	s, err := r.sourceOf(name)
	if err != nil {
		return false, err
	}
	cands, err := r.candidates(name, s)
	if err != nil {
		return false, err
	}
	// A moved locked tag is an error even if another version would fit:
	// silently switching versions would hide the change.
	for _, c := range cands {
		if err := r.checkMovedTag(name, c); err != nil {
			return false, err
		}
	}
	for _, c := range r.order(name, s, cands) {
		if !r.fits(name, c) {
			continue
		}
		m, err := r.manifest(name, s, c)
		if errors.Is(err, source.ErrNoManifest) {
			// An old tag from before the repository became a gtr package.
			r.skipped = append(r.skipped, name+"@"+c.Version)
			continue
		}
		if err != nil {
			return false, err
		}
		if m.Name != name {
			return false, fmt.Errorf("%s: the package at %s is named %q in its gtr.json; the key in dependencies must match the package name (ADR-0003)", name, s.String(), m.Name)
		}
		deps, err := packageDeps(name, c.Version, s, m)
		if err != nil {
			return false, err
		}
		goDeps := map[string]spec.Spec{}
		for dn, ds := range deps {
			if ds.Kind == spec.GoModule { // selected later by minimal version selection
				goDeps[dn] = ds
				delete(deps, dn)
			}
		}
		node := &Node{Name: name, Spec: s, Candidate: c, Manifest: m, Deps: deps, GoDeps: goDeps}
		by := name + "@" + c.Version
		// Tentatively add the dependencies' ranges; fail fast if a decided
		// package no longer fits.
		var added []string
		consistent := true
		for _, dn := range sortedKeys(deps) {
			ds := deps[dn]
			_, member := r.req.Members[dn]
			if ch, ok := r.chosen[dn]; ok && !member && !ch.Spec.SameSource(ds) {
				if _, root := r.req.Root[dn]; !root {
					return false, fmt.Errorf("%s is required from two different sources: %s (by %s) and %s (by %s); add %s to your gtr.json to choose one",
						dn, ch.Spec.String(), r.firstBy(dn), ds.String(), by, dn)
				}
			}
			r.reqs[dn] = append(r.reqs[dn], requirement{rng: mustRange(ds.Range), text: ds.Range, by: by, spec: ds})
			added = append(added, dn)
			if ch, ok := r.chosen[dn]; ok && !r.fits(dn, ch.Candidate) {
				consistent = false
			}
		}
		// Peers only constrain: they do not add a package to the graph, but
		// if it is (or later becomes) part of it, its version must fit.
		for _, pn := range sortedKeys(m.PeerDependencies) {
			if _, isDep := deps[pn]; isDep {
				continue
			}
			r.reqs[pn] = append(r.reqs[pn], requirement{rng: mustRange(m.PeerDependencies[pn]), text: m.PeerDependencies[pn], by: by + " (peer)"})
			added = append(added, pn)
			if ch, ok := r.chosen[pn]; ok && !r.fits(pn, ch.Candidate) {
				consistent = false
			}
		}
		if consistent {
			r.chosen[name] = node
			ok, err := r.solve(append(append([]string(nil), rest...), sortedKeys(deps)...))
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
			delete(r.chosen, name)
		} else if r.conflict == nil {
			r.conflict = r.explainChosen(by, added)
		}
		for i := len(added) - 1; i >= 0; i-- { // undo
			dn := added[i]
			r.reqs[dn] = r.reqs[dn][:len(r.reqs[dn])-1]
			if len(r.reqs[dn]) == 0 {
				delete(r.reqs, dn)
			}
		}
	}
	if r.conflict == nil || !r.anyFits(name, cands) {
		r.conflict = r.explain(name, cands)
	}
	return false, nil
}

// sourceOf formats a spec without its range, as gtr.lock stores it.
func sourceOf(s spec.Spec) string {
	s.Range = ""
	return s.String()
}

// sourceOf returns where a package comes from. The project's own entry
// wins; otherwise all requirers must agree on the source.
func (r *resolver) sourceOf(name string) (spec.Spec, error) {
	if s, ok := r.req.Members[name]; ok {
		return s, nil
	}
	for _, q := range r.reqs[name] {
		if q.spec.Kind == spec.Workspace {
			return spec.Spec{}, fmt.Errorf("%s: %q (by %s) names a workspace member, but no member of the workspace is called %s", name, q.spec.String(), q.by, name)
		}
	}
	if s, ok := r.req.Root[name]; ok {
		return s, nil
	}
	var first *requirement
	for i := range r.reqs[name] {
		q := &r.reqs[name][i]
		if q.spec.Kind == "" { // peer requirement: range only
			continue
		}
		if first == nil {
			first = q
		} else if !first.spec.SameSource(q.spec) {
			return spec.Spec{}, fmt.Errorf("%s is required from two different sources: %s (by %s) and %s (by %s); add %s to your gtr.json to choose one",
				name, first.spec.String(), first.by, q.spec.String(), q.by, name)
		}
	}
	if first == nil {
		return spec.Spec{}, fmt.Errorf("%s: no source", name)
	}
	return first.spec, nil
}

func (r *resolver) candidates(name string, s spec.Spec) ([]source.Candidate, error) {
	key := name + "\x00" + s.String()
	if c, ok := r.cands[key]; ok {
		return c, nil
	}
	p, err := r.providers(s.Kind)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	c, err := p.Candidates(r.ctx, name, s)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	r.cands[key] = c
	return c, nil
}

func (r *resolver) manifest(name string, s spec.Spec, c source.Candidate) (*manifest.Manifest, error) {
	key := name + "\x00" + s.String() + "\x00" + c.Version
	if m, ok := r.manifests[key]; ok {
		return m, nil
	}
	p, err := r.providers(s.Kind)
	if err != nil {
		return nil, err
	}
	m, err := p.Manifest(r.ctx, s, c)
	if err != nil {
		return nil, fmt.Errorf("%s@%s: %w", name, c.Version, err)
	}
	r.manifests[key] = m
	return m, nil
}

func (r *resolver) firstBy(name string) string {
	for _, q := range r.reqs[name] {
		if q.spec.Kind != "" {
			return q.by
		}
	}
	return "?"
}

func (r *resolver) locked(name string) (lock.Entry, bool) {
	if r.req.Lock == nil || r.req.Update["*"] || r.req.Update[name] {
		return lock.Entry{}, false
	}
	e, ok := r.req.Lock.Packages[name]
	return e, ok
}

// checkMovedTag fails when a locked version now points at another commit:
// the tag was moved (force-pushed), and the code is no longer what was
// reviewed and locked.
func (r *resolver) checkMovedTag(name string, c source.Candidate) error {
	e, ok := r.locked(name)
	if !ok || c.Head || e.Commit == "" || c.Commit == "" || e.Version != c.Version || e.Commit == c.Commit {
		return nil
	}
	return fmt.Errorf("%s: the tag for %s was moved: gtr.lock has commit %s, the repository now has %s; "+
		"if the change is expected, run `gtr update %s`", name, c.Version, short(e.Commit), short(c.Commit), name)
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// order puts the locked version first if it may be kept. A locked commit
// of an untagged repository stays available even after the branch moved.
func (r *resolver) order(name string, s spec.Spec, cands []source.Candidate) []source.Candidate {
	e, ok := r.locked(name)
	if !ok {
		return cands
	}
	if strings.HasPrefix(e.Version, "0.0.0-") && e.Commit != "" && (len(cands) == 0 || cands[0].Head) && e.Source == sourceOf(s) {
		found := false
		for _, c := range cands {
			found = found || c.Commit == e.Commit
		}
		if !found {
			cands = append([]source.Candidate{{Version: e.Version, Commit: e.Commit, Head: true}}, cands...)
		}
	}
	out := make([]source.Candidate, 0, len(cands))
	for _, c := range cands {
		if c.Version == e.Version && (e.Commit == "" || c.Commit == e.Commit) {
			out = append([]source.Candidate{c}, out...)
		} else {
			out = append(out, c)
		}
	}
	return out
}

func (r *resolver) fits(name string, c source.Candidate) bool {
	v, err := semver.Parse(c.Version)
	if err != nil {
		return false
	}
	for _, q := range r.reqs[name] {
		if c.Head {
			// An untagged repository has no version to compare; a peer
			// range does not exclude it (as in checkPeers), a dependency
			// range does.
			if q.text != "*" && q.text != "" && q.spec.Kind != "" {
				return false
			}
			continue
		}
		if !q.rng.Match(v) {
			return false
		}
	}
	return true
}

func (r *resolver) anyFits(name string, cands []source.Candidate) bool {
	for _, c := range cands {
		if r.fits(name, c) {
			return true
		}
	}
	return false
}

func (r *resolver) explain(name string, cands []source.Candidate) error {
	var lines []string
	for _, q := range r.reqs[name] {
		lines = append(lines, fmt.Sprintf("  %s requires %s %s", q.by, name, q.text))
	}
	var versions []string
	for i, c := range cands {
		if i == 8 {
			versions = append(versions, "…")
			break
		}
		versions = append(versions, c.Version)
	}
	avail := "none"
	if len(versions) > 0 {
		avail = strings.Join(versions, ", ")
	}
	note := ""
	if len(r.skipped) > 0 {
		note = "\nskipped (no gtr.json): " + strings.Join(r.skipped, ", ")
	}
	return fmt.Errorf("no version of %s satisfies all requirements:\n%s\navailable versions: %s%s", name, strings.Join(lines, "\n"), avail, note)
}

func (r *resolver) explainChosen(by string, names []string) error {
	for _, n := range names {
		ch, ok := r.chosen[n]
		if !ok || r.fits(n, ch.Candidate) {
			continue
		}
		var lines []string
		for _, q := range r.reqs[n] {
			lines = append(lines, fmt.Sprintf("  %s requires %s %s", q.by, n, q.text))
		}
		return fmt.Errorf("%s conflicts with %s@%s already selected:\n%s", by, n, ch.Candidate.Version, strings.Join(lines, "\n"))
	}
	return nil
}

// packageDeps parses a package's dependencies. Their file: paths are
// relative to the package, which only makes sense for file: packages.
//
// A workspace member is developed in place, so its devDependencies count
// too (its tests must build).
func packageDeps(name, version string, from spec.Spec, m *manifest.Manifest) (map[string]spec.Spec, error) {
	out := map[string]spec.Spec{}
	all := m.Dependencies
	if from.Kind == spec.Workspace && len(m.DevDependencies) > 0 {
		all = map[string]string{}
		for k, v := range m.DevDependencies {
			all[k] = v
		}
		for k, v := range m.Dependencies {
			all[k] = v
		}
	}
	local := from.Kind == spec.File || from.Kind == spec.Workspace
	for dn, dv := range all {
		if err := spec.ValidateKey(dn); err != nil {
			return nil, fmt.Errorf("%s@%s: %w", name, version, err)
		}
		ds, err := spec.Parse(dn, dv)
		if err != nil {
			return nil, fmt.Errorf("%s@%s: %w", name, version, err)
		}
		if ds.Kind == spec.File && !local {
			return nil, fmt.Errorf("%s@%s depends on %s via file:%s; published packages cannot use file: dependencies", name, version, dn, ds.Path)
		}
		if ds.Kind == spec.Workspace && !local {
			return nil, fmt.Errorf("%s@%s depends on %s via %s; published packages cannot use workspace: dependencies", name, version, dn, ds.Raw)
		}
		if ds.Kind == spec.File {
			ds.Path = joinFile(from.Path, ds.Path)
		}
		out[dn] = ds
	}
	return out, nil
}

func joinFile(base, rel string) string {
	if strings.HasPrefix(rel, "/") || (len(rel) > 1 && rel[1] == ':') {
		return rel
	}
	return path.Clean(strings.TrimSuffix(base, "/") + "/" + rel)
}

func checkPeers(nodes map[string]*Node) error {
	var problems []string
	for _, name := range sortedNodeNames(nodes) {
		n := nodes[name]
		for _, pn := range sortedKeys(n.Manifest.PeerDependencies) {
			rng := n.Manifest.PeerDependencies[pn]
			peer, ok := nodes[pn]
			if !ok {
				problems = append(problems, fmt.Sprintf("  %s@%s needs %s %s: add it with `gtr add` (peer dependencies are not installed automatically)",
					name, n.Candidate.Version, pn, rng))
				continue
			}
			v, err := semver.Parse(peer.Candidate.Version)
			if err != nil || !peer.Candidate.Head && !mustRange(rng).Match(v) {
				problems = append(problems, fmt.Sprintf("  %s@%s needs %s %s, but %s is selected", name, n.Candidate.Version, pn, rng, peer.Candidate.Version))
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("peer dependencies are not satisfied:\n%s", strings.Join(problems, "\n"))
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedNodeNames(m map[string]*Node) []string { return sortedKeys(m) }
