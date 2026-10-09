package install

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gtr-manager/internal/gomodules"
	"gtr-manager/internal/link"
	"gtr-manager/internal/lock"
	"gtr-manager/internal/resolve"
	"gtr-manager/internal/spec"
)

// goSource is the "source" of external modules in gtr.lock.
const goSource = "go"

// resolveGo selects external Go modules required by the project and by its
// gtr packages (ADR-0003, rule 3; ADR-0004).
func (in *Installer) resolveGo(ctx context.Context, goRoot map[string]spec.Spec, nodes map[string]*resolve.Node, old *lock.Lock, update map[string]bool, prefer map[string]string) (*gomodules.Result, error) {
	var reqs []gomodules.Requirement
	for _, p := range sortedKeys(goRoot) {
		reqs = append(reqs, gomodules.Requirement{Path: p, Range: goRoot[p].Range, By: "the project"})
	}
	for _, name := range sortedNames(nodes) {
		n := nodes[name]
		for _, p := range sortedKeys(n.GoDeps) {
			reqs = append(reqs, gomodules.Requirement{Path: p, Range: n.GoDeps[p].Range, By: name + "@" + n.Candidate.Version})
		}
	}
	if len(reqs) == 0 {
		return &gomodules.Result{Versions: map[string]string{}, Direct: map[string]bool{}, Mods: map[string]gomodules.ModFile{}}, nil
	}
	locked, lockedMods := map[string]string{}, map[string]string{}
	for p, e := range old.Packages {
		if e.Source == goSource {
			locked[p] = e.Version
			if e.GoMod != "" {
				lockedMods[p+"@"+e.Version] = e.GoMod
			}
		}
	}
	for p, v := range prefer {
		if !update["*"] && !update[p] {
			locked[p] = v
		}
	}
	r := &gomodules.Resolver{Proxies: in.proxies(), SumDB: in.SumDB, Locked: locked, LockedMods: lockedMods, Update: update}
	return r.Resolve(ctx, reqs)
}

// fetchGo makes a selected module available in the store.
func (in *Installer) fetchGo(ctx context.Context, mods *gomodules.Result, p string, prev lock.Entry, update map[string]bool) (string, lock.Entry, error) {
	v := mods.Versions[p]
	want := ""
	if prev.Source == goSource && prev.Version == v {
		want = prev.Integrity
	}
	st := in.goStore()
	dir, err := st.Dir(p, v)
	if err != nil {
		return "", lock.Entry{}, err
	}
	if _, err := os.Stat(dir); err != nil {
		fmt.Fprintf(in.log(), "Downloading %s@%s\n", p, v)
	}
	proxy, err := in.proxies().For(p)
	if err != nil {
		return "", lock.Entry{}, err
	}
	modHash := mods.ModHashes[p+"@"+v]
	goModData, err := proxy.Mod(ctx, p, v, func(data []byte) error {
		if got := gomodules.HashMod(data); got != modHash {
			return fmt.Errorf("SECURITY ERROR: %s@%s go.mod changed during installation (%s, expected %s)", p, v, got, modHash)
		}
		return nil
	})
	if err != nil {
		return "", lock.Entry{}, err
	}
	dir, h, err := st.Fetch(ctx, proxy, in.SumDB, p, v, want, goModData)
	if err != nil {
		return "", lock.Entry{}, err
	}
	entry := lock.Entry{Version: v, Source: goSource, Integrity: h, GoMod: modHash}
	if len(mods.Mods[p].Require) > 0 {
		entry.Dependencies = map[string]string{}
		for rp, rv := range mods.Mods[p].Require {
			entry.Dependencies[rp] = rv
		}
	}
	return dir, entry, nil
}

// verifyGoStore re-hashes stored modules (gtr ci).
func (in *Installer) verifyGoStore(mods *gomodules.Result, l *lock.Lock, dirs map[string]string) error {
	for _, p := range sortedKeys(mods.Versions) {
		e := l.Packages[p]
		if e.Integrity == "" || e.GoMod == "" {
			return fmt.Errorf("gtr.lock has no hashes for %s; run `gtr install` and commit gtr.lock", p)
		}
		got, err := gomodules.Hash(dirs[p], p, e.Version)
		if err != nil {
			return err
		}
		if got != e.Integrity {
			return fmt.Errorf("%s@%s in the store (%s) was modified: expected %s, found %s; delete that directory and run `gtr ci` again",
				p, e.Version, dirs[p], e.Integrity, got)
		}
		// The go.mod the build uses must be the verified one, also when gtr
		// wrote it because the zip had none.
		data, err := os.ReadFile(filepath.Join(dirs[p], "go.mod"))
		if err != nil {
			return err
		}
		if got := gomodules.HashMod(data); got != e.GoMod {
			return fmt.Errorf("%s@%s: go.mod in the store (%s) was modified; delete that directory and run `gtr ci` again", p, e.Version, dirs[p])
		}
	}
	return nil
}

// linkGo makes gtr_modules/.go/<escaped path>@<version> for every module
// and removes stale links there.
func (in *Installer) linkGo(mods *gomodules.Result, dirs map[string]string) error {
	base := filepath.Join(in.path(ModulesDir), GoDir)
	want := map[string]bool{}
	for _, p := range sortedKeys(mods.Versions) {
		dn, err := gomodules.DirName(p, mods.Versions[p])
		if err != nil {
			return err
		}
		l := filepath.Join(base, filepath.FromSlash(dn))
		want[filepath.Clean(l)] = true
		if t := link.Target(l); t != "" && filepath.Clean(t) == filepath.Clean(dirs[p]) {
			continue
		}
		if _, err := link.Make(dirs[p], l); err != nil {
			return err
		}
	}
	storeRoot := in.goStore().Root
	var stale []string
	filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil || path == base {
			return nil
		}
		if want[filepath.Clean(path)] {
			return filepath.SkipDir
		}
		if t := link.Target(path); t != "" || link.IsCopy(path) {
			if link.IsCopy(path) || within(storeRoot, t) {
				stale = append(stale, path)
			}
			return filepath.SkipDir
		}
		return nil
	})
	for _, s := range stale {
		if err := link.Remove(s); err != nil {
			return err
		}
	}
	// Remove directories left empty (e.g. github.com/old-owner/).
	var dirsToCheck []string
	filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() && path != base && link.Target(path) == "" && !link.IsCopy(path) && !want[filepath.Clean(path)] {
			dirsToCheck = append(dirsToCheck, path)
		}
		return nil
	})
	sort.Slice(dirsToCheck, func(i, j int) bool { return len(dirsToCheck[i]) > len(dirsToCheck[j]) })
	for _, d := range dirsToCheck {
		os.Remove(d) // fails (and is ignored) unless empty
	}
	if len(mods.Versions) == 0 {
		os.Remove(base)
	}
	return nil
}

// ModuleRange suggests the gtr.json range for a newly added module version:
// ^1.10.0 → "^1.10.0"; 0.x → "^0.3"; pseudo-versions → "*".
func ModuleRange(version string) string {
	if gomodules.IsPseudo(version) {
		return "*"
	}
	v, err := gomodules.Parse(version)
	if err != nil {
		return "*"
	}
	if v.Major == 0 {
		if v.Minor == 0 {
			return "^" + v.String()
		}
		return fmt.Sprintf("^0.%d", v.Minor)
	}
	return "^" + strings.TrimPrefix(v.String(), "v")
}
