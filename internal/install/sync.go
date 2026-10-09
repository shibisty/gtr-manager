package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gtr-manager/internal/gomod"
	"gtr-manager/internal/gomodules"
	"gtr-manager/internal/lock"
	"gtr-manager/internal/manifest"
	"gtr-manager/internal/semver"
	"gtr-manager/internal/spec"
)

// Sync reconciles go.mod with gtr.json (ADR-0001, item 6). Edits made to a
// generated go.mod, or the requirements of a go.mod gtr did not write (an
// existing Go project), are carried over to gtr.json:
//
//   - a new require of an external module → a dependency with a range;
//   - a changed module version outside the current range → a new range;
//   - a removed require → the dependency is removed;
//   - replace <gtr package> => <local path> → a file: source;
//   - an indirect module raised above what gtr selects → a dependency;
//   - a go directive above what gtr generates → engines.go.
//
// A foreign go.mod may declare another module path: the generated go.mod
// uses the gtr.json name (`gtr init` warns that imports must follow).
//
// Then go.mod is regenerated, keeping the module versions it names where the
// ranges allow. With force, nothing is carried over.
func (in *Installer) Sync(ctx context.Context, force bool) error {
	m, err := in.Load()
	if err != nil {
		return err
	}
	modPath := in.path("go.mod")
	state, err := gomod.Check(modPath)
	if err != nil {
		return err
	}
	if members, err := Members(in.Dir, m, nil); err != nil {
		return err
	} else if len(members) > 0 && !force {
		// Edits may be spread over go.work and members' go.mod files.
		return errors.New("in a workspace, gtr sync only regenerates the files: put changes into the gtr.json files and run `gtr install`, or `gtr sync --force` to discard edits of go.work and go.mod")
	}
	if force || state == gomod.Missing || state == gomod.Generated || state == gomod.PackageMod {
		return in.Install(ctx, Options{Manifest: m, ForceMod: true})
	}
	data, err := os.ReadFile(modPath)
	if err != nil {
		return err
	}
	old, err := lock.Load(in.path(lock.FileName))
	if err != nil {
		return err
	}
	f := gomod.Parse(data)
	changes, err := carryOver(m, f, old, state == gomod.Foreign)
	if err != nil {
		return err
	}
	for _, c := range changes {
		fmt.Fprintf(in.log(), "gtr.json: %s\n", c)
	}
	prefer, raise := map[string]string{}, map[string]string{}
	for _, r := range f.Require {
		if !spec.IsGoModule(r.Path) {
			continue
		}
		if r.Indirect {
			raise[r.Path] = r.Version
		} else {
			prefer[r.Path] = r.Version
		}
	}
	adjusted := 0
	err = in.Install(ctx, Options{Manifest: m, ForceMod: true, SaveAfter: len(changes) > 0, Prefer: prefer,
		Raise: raise, MinGo: f.Go, OnAdjust: func(c string) {
			adjusted++
			fmt.Fprintf(in.log(), "gtr.json: %s\n", c)
		}})
	if err == nil && len(changes)+adjusted == 0 {
		fmt.Fprintln(in.log(), "go.mod had nothing to carry over to gtr.json; regenerated it")
	}
	return err
}

// carryOver applies go.mod changes to the manifest and describes them.
func carryOver(m *manifest.Manifest, f gomod.File, l *lock.Lock, foreign bool) ([]string, error) {
	var changes, problems []string
	deps := func(key string) map[string]string {
		if _, ok := m.DevDependencies[key]; ok {
			return m.DevDependencies
		}
		if _, ok := m.Dependencies[key]; ok {
			return m.Dependencies
		}
		return nil
	}
	if f.Module != "" && f.Module != m.Name && !foreign {
		problems = append(problems, fmt.Sprintf("module %s: the module path comes from \"name\" in gtr.json (%s); change it there", f.Module, m.Name))
	}
	for _, d := range f.Unsupported {
		problems = append(problems, fmt.Sprintf("%s: gtr.json has no equivalent; remove it from go.mod", d))
	}
	required := map[string]bool{}
	for _, r := range f.Require {
		required[r.Path] = true
		if r.Indirect { // gtr recomputes the indirect requirements itself
			continue
		}
		if r.Path == m.Name {
			continue
		}
		if err := spec.ValidateKey(r.Path); err != nil {
			problems = append(problems, err.Error())
			continue
		}
		target := deps(r.Path)
		switch {
		case target != nil && spec.IsGoModule(r.Path):
			s, err := spec.Parse(r.Path, target[r.Path])
			if err != nil {
				problems = append(problems, err.Error())
				continue
			}
			v, err := gomodules.Parse(r.Version)
			rng, _ := semver.ParseRange(s.Range)
			if err == nil && s.Range != "*" && !rng.Match(v) {
				target[r.Path] = ModuleRange(r.Version)
				changes = append(changes, fmt.Sprintf("%s: %q → %q (from go.mod %s)", r.Path, s.Range, target[r.Path], r.Version))
			}
		case target != nil: // a gtr package: versions come from gtr.json ranges
			if e, ok := l.Packages[r.Path]; ok && "v"+e.Version != r.Version {
				problems = append(problems, fmt.Sprintf("%s: change gtr package versions in gtr.json (the range in %q), not in go.mod", r.Path, target[r.Path]))
			}
		case spec.IsGoModule(r.Path):
			m.Dependencies[r.Path] = ModuleRange(r.Version)
			changes = append(changes, fmt.Sprintf("added %s: %q (from go.mod %s)", r.Path, m.Dependencies[r.Path], r.Version))
		case l.Packages[r.Path].Source != "" && l.Packages[r.Path].Source != goSource:
			// A package installed for another package; go mod tidy lists
			// what the project imports directly. Its source is known.
		default:
			if _, local := f.Replace[r.Path]; !local {
				problems = append(problems, fmt.Sprintf("%s: a gtr package needs a source; add it with `gtr add github:owner/repo` or `gtr add file:../path`", r.Path))
			}
		}
	}
	for _, p := range sortedKeys(f.Replace) {
		target, version := gomod.ReplaceTarget(f.Replace[p])
		if target == "" || strings.HasPrefix(target, "./"+ModulesDir+"/") {
			continue // generated
		}
		if !isLocalPath(target) || version != "" {
			problems = append(problems, fmt.Sprintf("replace %s => %s: replacing a module with another module is not supported", p, f.Replace[p]))
			continue
		}
		if spec.IsGoModule(p) {
			problems = append(problems, fmt.Sprintf("replace %s => %s: local directories are supported only for gtr packages (with a gtr.json)", p, target))
			continue
		}
		value := "file:" + filepath.ToSlash(target)
		dm := deps(p)
		if dm == nil {
			dm = m.Dependencies
		}
		if dm[p] != value {
			changes = append(changes, fmt.Sprintf("%s: %q (from replace)", p, value))
			dm[p] = value
		}
		required[p] = true
	}
	if !foreign {
		for _, dm := range []map[string]string{m.Dependencies, m.DevDependencies} {
			for _, key := range sortedKeys(dm) {
				if !required[key] {
					delete(dm, key)
					changes = append(changes, fmt.Sprintf("removed %s (no longer in go.mod)", key))
				}
			}
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, errors.New("go.mod has changes gtr cannot carry over:\n  " + strings.Join(problems, "\n  ") +
			"\nfix them, or run `gtr sync --force` to regenerate go.mod from gtr.json")
	}
	return changes, nil
}

func isLocalPath(p string) bool {
	return strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "/") ||
		strings.HasPrefix(p, `.\`) || strings.HasPrefix(p, `..\`) || len(p) > 2 && p[1] == ':'
}
