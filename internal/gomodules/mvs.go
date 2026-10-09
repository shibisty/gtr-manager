package gomodules

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"gtr-manager/internal/semver"
)

// Requirement is a Go module the project or one of its gtr packages asks
// for, with an npm-style range from gtr.json.
type Requirement struct {
	Path  string
	Range string
	By    string // "the project" or "orm-mysql@0.1.0"
}

// Resolver selects module versions.
type Resolver struct {
	Proxies *Proxies
	SumDB   *SumDB            // nil: no checks
	Locked  map[string]string // module → version from gtr.lock
	// LockedMods pins go.mod hashes from gtr.lock ("path@version" → h1); a
	// pinned file is checked locally, without the checksum database.
	LockedMods map[string]string
	Update     map[string]bool // ignore Locked for these paths; "*" = all

	mods      map[string]ModFile
	modHashes map[string]string
}

// Result is a resolved module build list.
type Result struct {
	Versions  map[string]string  // module → selected version (the build list)
	Direct    map[string]bool    // required from gtr.json (the project or a gtr package)
	Mods      map[string]ModFile // go.mod of every selected version
	ModHashes map[string]string  // "path@version" → verified go.mod h1
}

// Resolve picks a version for each required module (the highest one the
// ranges allow, or the locked one), then raises versions with minimal
// version selection. The generated go.mod lists every selected module, so
// the go command treats each one as a root and reads its go.mod; gtr does
// the same, iterating to a fixed point, so both agree on the build list.
func (r *Resolver) Resolve(ctx context.Context, reqs []Requirement) (*Result, error) {
	r.mods = map[string]ModFile{}
	r.modHashes = map[string]string{}
	byPath := map[string][]Requirement{}
	var paths []string
	for _, q := range reqs {
		if err := CheckPath(q.Path); err != nil {
			return nil, err
		}
		if _, ok := byPath[q.Path]; !ok {
			paths = append(paths, q.Path)
		}
		byPath[q.Path] = append(byPath[q.Path], q)
	}
	sort.Strings(paths)

	selected := map[string]string{}
	for _, p := range paths {
		v, err := r.pickRoot(ctx, p, byPath[p])
		if err != nil {
			return nil, err
		}
		selected[p] = v
	}

	read := map[string]bool{} // "path@version" whose requirements were applied
	for changed := true; changed; {
		changed = false
		for _, p := range sortedKeys(selected) {
			v := selected[p]
			if read[p+"@"+v] {
				continue
			}
			read[p+"@"+v] = true
			mf, err := r.mod(ctx, p, v)
			if err != nil {
				return nil, err
			}
			for _, rp := range sortedKeys(mf.Require) {
				rv := mf.Require[rp]
				if err := CheckPath(rp); err != nil {
					return nil, fmt.Errorf("%s@%s requires %w", p, v, err)
				}
				if !ValidVersion(rp, rv) {
					return nil, fmt.Errorf("%s@%s requires %s at invalid version %q", p, v, rp, rv)
				}
				if cur, ok := selected[rp]; !ok || Compare(rv, cur) > 0 {
					selected[rp] = rv
					changed = true
				}
			}
		}
	}

	// MVS may have raised a module above what a gtr.json range allows.
	for _, p := range paths {
		v, _ := Parse(selected[p])
		for _, q := range byPath[p] {
			rng, _ := semver.ParseRange(q.Range)
			if !rangeMatch(rng, q.Range, selected[p], v) {
				return nil, fmt.Errorf("%s: %s requires %s, but other modules require %s (minimal version selection never goes down); widen the range in gtr.json",
					p, q.By, q.Range, selected[p])
			}
		}
	}
	res := &Result{Versions: selected, Direct: map[string]bool{}, Mods: map[string]ModFile{}, ModHashes: map[string]string{}}
	for _, p := range paths {
		res.Direct[p] = true
	}
	for p, v := range selected {
		mf, err := r.mod(ctx, p, v)
		if err != nil {
			return nil, err
		}
		res.Mods[p] = mf
		res.ModHashes[p+"@"+v] = r.modHashes[p+"@"+v]
	}
	return res, nil
}

func rangeMatch(rng semver.Range, text, version string, v semver.Version) bool {
	if text == "" || text == "*" {
		return true
	}
	if IsPseudo(version) {
		return false
	}
	return rng.Match(v)
}

// pickRoot chooses the version of a module required from gtr.json, like
// `go get`: retracted versions are skipped, and +incompatible versions are
// used only when no compatible version fits.
func (r *Resolver) pickRoot(ctx context.Context, p string, reqs []Requirement) (string, error) {
	fits := func(v string) bool {
		pv, err := Parse(v)
		if err != nil || !ValidVersion(p, v) {
			return false
		}
		for _, q := range reqs {
			rng, err := semver.ParseRange(q.Range)
			if err != nil || !rangeMatch(rng, q.Range, v, pv) {
				return false
			}
		}
		return true
	}
	if lv, ok := r.Locked[p]; ok && !r.Update["*"] && !r.Update[p] && fits(lv) {
		return lv, nil
	}
	proxy, err := r.Proxies.For(p)
	if err != nil {
		return "", err
	}
	versions, err := proxy.List(ctx, p)
	if err != nil {
		return "", err
	}
	sort.Slice(versions, func(i, j int) bool { return Compare(versions[i], versions[j]) > 0 })
	// Retractions are declared in the go.mod of the latest version.
	var latest ModFile
	for _, v := range versions {
		if pv, _ := Parse(v); pv.Pre == "" && !strings.HasSuffix(v, "+incompatible") {
			if latest, err = r.mod(ctx, p, v); err != nil {
				return "", err
			}
			break
		}
	}
	for _, pass := range []func(string) bool{
		func(v string) bool { pv, _ := Parse(v); return pv.Pre == "" && !strings.HasSuffix(v, "+incompatible") },
		func(v string) bool { pv, _ := Parse(v); return pv.Pre == "" },
		func(string) bool { return true }, // a range that names a pre-release
	} {
		for _, v := range versions {
			if pass(v) && fits(v) && !latest.Retracted(v) {
				return v, nil
			}
		}
	}
	if len(versions) == 0 {
		if lv, err := proxy.Latest(ctx, p); err == nil && fits(lv) {
			return lv, nil
		}
	}
	var asked []string
	for _, q := range reqs {
		asked = append(asked, fmt.Sprintf("%s (by %s)", q.Range, q.By))
	}
	avail := "none"
	if len(versions) > 0 {
		if len(versions) > 8 {
			versions = append(versions[:8], "…")
		}
		avail = strings.Join(versions, ", ")
	}
	return "", fmt.Errorf("no version of %s satisfies %s; available: %s", p, strings.Join(asked, ", "), avail)
}

// mod returns a verified, parsed go.mod.
func (r *Resolver) mod(ctx context.Context, p, v string) (ModFile, error) {
	key := p + "@" + v
	if mf, ok := r.mods[key]; ok {
		return mf, nil
	}
	proxy, err := r.Proxies.For(p)
	if err != nil {
		return ModFile{}, err
	}
	data, err := proxy.Mod(ctx, p, v, func(data []byte) error { return r.verifyMod(ctx, p, v, data) })
	if err != nil {
		return ModFile{}, err
	}
	r.modHashes[key] = HashMod(data)
	mf := ParseModFile(data)
	if mf.Module != "" && mf.Module != p {
		return ModFile{}, fmt.Errorf("%s@%s: its go.mod declares module %q", p, v, mf.Module)
	}
	r.mods[key] = mf
	return mf, nil
}

func (r *Resolver) verifyMod(ctx context.Context, p, v string, data []byte) error {
	if want, ok := r.LockedMods[p+"@"+v]; ok && want != "" {
		if got := HashMod(data); got != want {
			return fmt.Errorf("SECURITY ERROR: %s@%s go.mod hash %s does not match gtr.lock (%s)", p, v, got, want)
		}
		return nil
	}
	if r.SumDB == nil || r.SumDB.IsPrivate(p) {
		return nil
	}
	_, want, err := r.SumDB.Lookup(ctx, p, v)
	if err != nil {
		return err
	}
	if got := HashMod(data); got != want {
		return fmt.Errorf("SECURITY ERROR: %s@%s go.mod hash %s does not match the checksum database (%s)", p, v, got, want)
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
