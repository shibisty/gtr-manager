package npm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Package is one installed package: where it goes in node_modules and
// what it is.
type Package struct {
	Path         string            // "node_modules/preact", "node_modules/a/node_modules/b"
	Name         string            // "preact"
	Version      string            // "10.19.3"
	Tarball      string            // its URL
	Integrity    string            // "sha512-…"
	Dependencies map[string]string // its dependencies' ranges (optional ones included)
	Optional     map[string]bool   // which of them are optional
}

// Request is the input of Resolve.
type Request struct {
	// Deps are the ranges of the project's npm dependencies.
	Deps map[string]string
	// Locked is the previous resolution (gtr.lock); a locked package is
	// kept while it still fits its range, without asking the registry.
	Locked map[string]*Package
	// Update lists packages to resolve again ("*": all).
	Update map[string]bool
	// Frozen installs Locked exactly (gtr ci); it fails when the
	// dependencies no longer match it.
	Frozen bool
}

// Resolve builds the node_modules tree: every dependency is hoisted to the
// top-level node_modules unless a different version is already there, in
// which case it goes into its dependent's own node_modules (npm's layout).
func (r *Registry) Resolve(ctx context.Context, req Request) (map[string]*Package, error) {
	if req.Frozen {
		return frozen(req)
	}
	tree := map[string]*Package{}
	type want struct {
		parent   string // the dependent's path; "" for the project
		name     string
		rng      string
		optional bool
		by       string
	}
	var queue []want
	for _, n := range sortedKeys(req.Deps) {
		queue = append(queue, want{name: n, rng: req.Deps[n], by: "gtr.json"})
	}
	for len(queue) > 0 {
		w := queue[0]
		queue = queue[1:]
		rng, err := ParseRange(w.rng)
		if err != nil {
			return nil, fmt.Errorf("npm: %s requires %s %q: %w", w.by, w.name, w.rng, err)
		}
		// Node's lookup from the dependent: its own node_modules, then each
		// enclosing one up to the top.
		target := ""
		satisfied := false
		for _, dir := range lookupDirs(w.parent) {
			p := join(dir, w.name)
			if have, ok := tree[p]; ok {
				if v, err := ParseVersion(have.Version); err == nil && rng.Match(v) {
					satisfied = true
				} else if target == "" {
					// A different version is in the way: install below it.
					target = join(w.parent, w.name)
				}
				break
			}
			if dir == "" && target == "" {
				target = p // free at the top: hoist
			}
		}
		if satisfied {
			continue
		}
		if target == "" {
			target = join(w.parent, w.name)
		}
		pkg, err := r.pick(ctx, req, target, w.name, rng)
		if err != nil {
			if w.optional {
				continue // an optional dependency that cannot be installed is skipped, as npm does
			}
			return nil, fmt.Errorf("%s (required by %s)", err, w.by)
		}
		tree[target] = pkg
		for _, dn := range sortedKeys(pkg.Dependencies) {
			queue = append(queue, want{parent: target, name: dn, rng: pkg.Dependencies[dn], optional: pkg.Optional[dn], by: pkg.Name + "@" + pkg.Version})
		}
	}
	return tree, nil
}

// pick chooses the version for a path: the locked one while it fits, else
// the registry's best match.
func (r *Registry) pick(ctx context.Context, req Request, target, name string, rng Range) (*Package, error) {
	if l, ok := req.Locked[target]; ok && l.Name == name && !req.Update[name] && !req.Update["*"] {
		if v, err := ParseVersion(l.Version); err == nil && rng.Match(v) {
			cp := *l
			cp.Path = target
			return &cp, nil
		}
	}
	p, err := r.Packument(ctx, name)
	if err != nil {
		return nil, err
	}
	rel, ok := p.Best(rng)
	if !ok {
		return nil, fmt.Errorf("npm: no version of %s matches %q", name, rng)
	}
	if !platformOK(rel.OS, runtime.GOOS) || !platformOK(rel.CPU, runtime.GOARCH) {
		return nil, fmt.Errorf("npm: %s@%s is not for this platform", name, rel.Version)
	}
	sri := rel.Dist.Integrity
	if sri == "" && rel.Dist.Shasum != "" {
		sri = "sha1-" + hexToB64(rel.Dist.Shasum)
	}
	pkg := &Package{Path: target, Name: name, Version: rel.Version, Tarball: rel.Dist.Tarball, Integrity: sri,
		Dependencies: map[string]string{}, Optional: map[string]bool{}}
	for dn, dr := range rel.Dependencies {
		pkg.Dependencies[dn] = dr
	}
	for dn, dr := range rel.OptionalDependencies {
		pkg.Dependencies[dn] = dr
		pkg.Optional[dn] = true
	}
	return pkg, nil
}

func frozen(req Request) (map[string]*Package, error) {
	for _, n := range sortedKeys(req.Deps) {
		l, ok := req.Locked["node_modules/"+n]
		rng, err := ParseRange(req.Deps[n])
		if err != nil {
			return nil, err
		}
		v, verr := ParseVersion(versionOf(l))
		if !ok || verr != nil || !rng.Match(v) {
			return nil, fmt.Errorf("gtr.lock does not match gtr.json for npm package %s %q; run `gtr install` and commit gtr.lock", n, req.Deps[n])
		}
	}
	out := map[string]*Package{}
	for p, l := range req.Locked {
		cp := *l
		cp.Path = p
		out[p] = &cp
	}
	return out, nil
}

func versionOf(p *Package) string {
	if p == nil {
		return ""
	}
	return p.Version
}

// platformOK applies a package's os/cpu list ("win32", "!darwin").
func platformOK(list []string, goos string) bool {
	if len(list) == 0 {
		return true
	}
	name := map[string]string{"windows": "win32", "amd64": "x64", "386": "ia32"}[goos]
	if name == "" {
		name = goos
	}
	allowed, anyPositive := false, false
	for _, e := range list {
		if strings.HasPrefix(e, "!") {
			if e[1:] == name {
				return false
			}
			continue
		}
		anyPositive = true
		if e == name {
			allowed = true
		}
	}
	return allowed || !anyPositive
}

func hexToB64(h string) string {
	b := make([]byte, len(h)/2)
	for i := 0; i+1 < len(h); i += 2 {
		fmt.Sscanf(h[i:i+2], "%02x", &b[i/2])
	}
	return encodeB64(b)
}

// lookupDirs are the directories whose node_modules a package at parent
// searches, nearest first; "" is the project.
func lookupDirs(parent string) []string {
	out := []string{}
	for p := parent; p != ""; {
		out = append(out, p)
		i := strings.LastIndex(p, "/node_modules/")
		if i < 0 {
			break
		}
		p = p[:i]
	}
	return append(out, "")
}

func join(dir, name string) string {
	if dir == "" {
		return "node_modules/" + name
	}
	return dir + "/node_modules/" + name
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// StateFile records in node_modules what gtr installed there.
const StateFile = ".gtr-npm.json"

// Install writes the tree into dir/node_modules from the store (storeDir),
// downloading what is missing. A top-level package is rewritten only when it
// or something nested in it changed; packages gtr installed before and no
// longer needs are removed; anything else in node_modules is left alone.
func (r *Registry) Install(ctx context.Context, dir, storeDir string, tree map[string]*Package, offline bool) error {
	nm := filepath.Join(dir, "node_modules")
	oldState := map[string]string{}
	if data, err := os.ReadFile(filepath.Join(nm, StateFile)); err == nil {
		_ = json.Unmarshal(data, &oldState)
	}
	state := map[string]string{}
	groups := map[string][]*Package{} // top-level path → its subtree
	for _, p := range sortedKeys(tree) {
		pkg := tree[p]
		state[p] = pkg.Version + " " + pkg.Integrity
		groups[top(p)] = append(groups[top(p)], pkg)
	}
	// Remove what is gone, then rewrite changed groups.
	for p := range oldState {
		if _, keep := groups[top(p)]; !keep && top(p) == p {
			if err := os.RemoveAll(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
				return err
			}
		}
	}
	for _, g := range sortedKeys(groups) {
		changed := false
		for _, pkg := range groups[g] {
			if oldState[pkg.Path] != state[pkg.Path] {
				changed = true
			}
		}
		for p := range oldState {
			if top(p) == g && state[p] == "" {
				changed = true // a nested package went away
			}
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(g), "package.json")); err != nil {
			changed = true
		}
		if !changed {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, filepath.FromSlash(g))); err != nil {
			return err
		}
		pkgs := groups[g]
		sort.Slice(pkgs, func(i, j int) bool { return len(pkgs[i].Path) < len(pkgs[j].Path) }) // parents first
		for _, pkg := range pkgs {
			src, err := r.stored(ctx, storeDir, pkg, offline)
			if err != nil {
				return err
			}
			if err := copyTree(src, filepath.Join(dir, filepath.FromSlash(pkg.Path))); err != nil {
				return err
			}
		}
	}
	if len(tree) == 0 && len(oldState) == 0 {
		return nil
	}
	if err := os.MkdirAll(nm, 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(state, "", "  ")
	return os.WriteFile(filepath.Join(nm, StateFile), append(data, '\n'), 0o644)
}

// top is the top-level package of a path: node_modules/a or node_modules/@s/a.
func top(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) >= 3 && strings.HasPrefix(parts[1], "@") {
		return strings.Join(parts[:3], "/")
	}
	if len(parts) >= 2 {
		return strings.Join(parts[:2], "/")
	}
	return p
}

// stored returns the package's directory in the store, downloading it the
// first time.
func (r *Registry) stored(ctx context.Context, storeDir string, pkg *Package, offline bool) (string, error) {
	name := strings.ReplaceAll(pkg.Name, "/", "+") + "@" + pkg.Version + "-" + short(pkg.Integrity)
	dst := filepath.Join(storeDir, name)
	if _, err := os.Stat(filepath.Join(dst, "package.json")); err == nil {
		return dst, nil
	}
	if offline {
		return "", fmt.Errorf("npm: %s@%s is not in the store (offline)", pkg.Name, pkg.Version)
	}
	if pkg.Tarball == "" {
		return "", fmt.Errorf("npm: %s@%s has no tarball URL", pkg.Name, pkg.Version)
	}
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(storeDir, ".fetch-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err := r.Fetch(ctx, pkg.Tarball, pkg.Integrity, "", tmp); err != nil {
		return "", fmt.Errorf("npm: %s@%s: %w", pkg.Name, pkg.Version, err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "package.json")); err != nil {
		return "", fmt.Errorf("npm: %s@%s: the tarball has no package.json", pkg.Name, pkg.Version)
	}
	if err := os.Rename(tmp, dst); err != nil {
		if _, serr := os.Stat(filepath.Join(dst, "package.json")); serr == nil {
			return dst, nil // another install stored it meanwhile
		}
		return "", err
	}
	return dst, nil
}

// short is 8 hex characters of an SRI hash, for store directory names.
func short(sri string) string {
	_, b64, _ := strings.Cut(strings.Fields(sri + " x")[0], "-")
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) < 4 {
		return "00000000"
	}
	return fmt.Sprintf("%x", raw[:4])
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		info, err := in.Stat()
		if err != nil {
			return err
		}
		f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, err = io.Copy(f, in)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return err
	})
}
