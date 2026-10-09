package resolve

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"gtr-manager/internal/lock"
	"gtr-manager/internal/manifest"
	"gtr-manager/internal/semver"
	"gtr-manager/internal/source"
	"gtr-manager/internal/spec"
)

// fake is a synthetic package universe: "repo" → version → gtr.json.
type fake struct {
	pkgs       map[string]map[string]string
	heads      map[string]bool
	noManifest map[string]bool
	calls      int
}

func (f *fake) Candidates(_ context.Context, _ string, s spec.Spec) ([]source.Candidate, error) {
	vs, ok := f.pkgs[repoName(s)]
	if !ok {
		return nil, fmt.Errorf("github: %s not found", s.Repo)
	}
	var out []source.Candidate
	for v := range vs {
		out = append(out, source.Candidate{Version: v, Commit: "c-" + v, Head: f.heads[repoName(s)]})
	}
	sort.Slice(out, func(i, j int) bool {
		return semver.Compare(semver.MustParse(out[i].Version), semver.MustParse(out[j].Version)) > 0
	})
	return out, nil
}

func (f *fake) Manifest(_ context.Context, s spec.Spec, c source.Candidate) (*manifest.Manifest, error) {
	f.calls++
	if f.noManifest[c.Version] {
		return nil, fmt.Errorf("x: %w", source.ErrNoManifest)
	}
	return manifest.Parse([]byte(f.pkgs[repoName(s)][c.Version]), s.Repo)
}

func repoName(s spec.Spec) string { _, n, _ := strings.Cut(s.Repo, "/"); return n }

func (f *fake) Fetch(context.Context, spec.Spec, source.Candidate, string) error { return nil }

func pkg(name string, deps, peers map[string]string) string {
	m := &manifest.Manifest{Name: name, Version: "0.0.0", Dependencies: deps, PeerDependencies: peers}
	data, _ := manifest.Marshal(m)
	return string(data)
}

func gh(name, rng string) spec.Spec {
	s, err := spec.Parse(name, "github:o/"+name+"#"+rng)
	if err != nil {
		panic(err)
	}
	return s
}

func run(t *testing.T, f *fake, root map[string]string, l *lock.Lock, update ...string) (map[string]string, error) {
	t.Helper()
	r := map[string]spec.Spec{}
	for n, v := range root {
		s, err := spec.Parse(n, v)
		if err != nil {
			t.Fatal(err)
		}
		r[n] = s
	}
	up := map[string]bool{}
	for _, u := range update {
		up[u] = true
	}
	nodes, err := Resolve(context.Background(), func(spec.Kind) (source.Provider, error) { return f, nil }, Request{Root: r, Lock: l, Update: up})
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for n, node := range nodes {
		out[n] = node.Candidate.Version
	}
	return out, nil
}

func dep(n, rng string) string { return "github:o/" + n + "#" + rng }

func TestSimpleAndTransitive(t *testing.T) {
	f := &fake{pkgs: map[string]map[string]string{
		"orm":       {"0.1.0": pkg("orm", nil, nil), "0.1.3": pkg("orm", nil, nil), "0.2.0": pkg("orm", nil, nil)},
		"orm-mysql": {"0.1.0": pkg("orm-mysql", map[string]string{"orm": dep("orm", "^0.1")}, nil)},
	}}
	got, err := run(t, f, map[string]string{"orm-mysql": dep("orm-mysql", "^0.1")}, nil)
	if err != nil || got["orm"] != "0.1.3" || got["orm-mysql"] != "0.1.0" {
		t.Fatalf("%v %v", got, err)
	}
}

// The newest version of a pulls in a conflicting b; the resolver must go
// back and pick an older a.
func TestBacktracking(t *testing.T) {
	f := &fake{pkgs: map[string]map[string]string{
		"pa": {"1.0.0": pkg("pa", map[string]string{"pb": dep("pb", "^1")}, nil),
			"2.0.0": pkg("pa", map[string]string{"pb": dep("pb", "^2")}, nil)},
		"pb": {"1.0.0": pkg("pb", nil, nil), "2.0.0": pkg("pb", map[string]string{"pc": dep("pc", "^1")}, nil)},
		"pc": {"1.0.0": pkg("pc", nil, nil), "2.0.0": pkg("pc", nil, nil)},
	}}
	got, err := run(t, f, map[string]string{"pa": dep("pa", "*"), "pc": dep("pc", "^2")}, nil)
	if err != nil || got["pa"] != "1.0.0" || got["pb"] != "1.0.0" || got["pc"] != "2.0.0" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestConflictMessage(t *testing.T) {
	f := &fake{pkgs: map[string]map[string]string{
		"orm":       {"0.1.0": pkg("orm", nil, nil), "0.2.0": pkg("orm", nil, nil)},
		"orm-mysql": {"0.1.0": pkg("orm-mysql", map[string]string{"orm": dep("orm", "^0.1")}, nil)},
	}}
	_, err := run(t, f, map[string]string{"orm": dep("orm", "^0.2"), "orm-mysql": dep("orm-mysql", "*")}, nil)
	if err == nil {
		t.Fatal("want conflict")
	}
	for _, want := range []string{"orm", "the project requires orm ^0.2", "orm-mysql@0.1.0 requires orm ^0.1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message lacks %q:\n%v", want, err)
		}
	}
	_, err = run(t, f, map[string]string{"orm": dep("orm", "^9")}, nil)
	if err == nil || !strings.Contains(err.Error(), "available versions: 0.2.0, 0.1.0") {
		t.Fatalf("no version: %v", err)
	}
}

func TestLockAndUpdate(t *testing.T) {
	f := &fake{pkgs: map[string]map[string]string{"orm": {"0.1.0": pkg("orm", nil, nil), "0.1.5": pkg("orm", nil, nil)}}}
	l := &lock.Lock{Packages: map[string]lock.Entry{"orm": {Version: "0.1.0", Commit: "c-0.1.0"}}}
	root := map[string]string{"orm": dep("orm", "^0.1")}
	if got, _ := run(t, f, root, l); got["orm"] != "0.1.0" {
		t.Fatalf("locked version must be kept: %v", got)
	}
	if got, _ := run(t, f, root, l, "orm"); got["orm"] != "0.1.5" {
		t.Fatalf("update: %v", got)
	}
	if got, _ := run(t, f, root, l, "*"); got["orm"] != "0.1.5" {
		t.Fatalf("update all: %v", got)
	}
	// The locked version no longer fits the range: it is dropped.
	if got, _ := run(t, f, map[string]string{"orm": dep("orm", ">=0.1.5")}, l); got["orm"] != "0.1.5" {
		t.Fatalf("stale lock: %v", got)
	}
	// A moved tag (same version, different commit) is an error, not a silent switch.
	l.Packages["orm"] = lock.Entry{Version: "0.1.0", Commit: "other"}
	if _, err := run(t, f, root, l); err == nil || !strings.Contains(err.Error(), "was moved") {
		t.Fatalf("moved tag: %v", err)
	}
	if got, err := run(t, f, root, l, "orm"); err != nil || got["orm"] != "0.1.5" {
		t.Fatalf("update accepts a moved tag: %v %v", got, err)
	}
}

func TestNameMismatchAndSources(t *testing.T) {
	f := &fake{pkgs: map[string]map[string]string{
		"evil": {"1.0.0": pkg("not-orm", nil, nil)},
		"px":   {"1.0.0": pkg("px", map[string]string{"py": "github:o/py#^1"}, nil)},
		"pz":   {"1.0.0": pkg("pz", map[string]string{"py": "github:other/py#^1"}, nil)},
		"py":   {"1.0.0": pkg("py", nil, nil)},
	}}
	_, err := run(t, f, map[string]string{"evil": "github:o/evil"}, nil)
	if err == nil || !strings.Contains(err.Error(), `named "not-orm"`) {
		t.Fatalf("name mismatch: %v", err)
	}
	_, err = run(t, f, map[string]string{"px": dep("px", "*"), "pz": dep("pz", "*")}, nil)
	if err == nil || !strings.Contains(err.Error(), "two different sources") {
		t.Fatalf("sources: %v", err)
	}
	// The project's own entry decides the source.
	f.pkgs["py"]["1.0.0"] = pkg("py", nil, nil)
	if _, err := run(t, f, map[string]string{"px": dep("px", "*"), "pz": dep("pz", "*"), "py": dep("py", "*")}, nil); err != nil {
		t.Fatalf("root source wins: %v", err)
	}
}

func TestPeers(t *testing.T) {
	f := &fake{pkgs: map[string]map[string]string{
		"orm":       {"0.1.0": pkg("orm", nil, nil), "0.2.0": pkg("orm", nil, nil)},
		"orm-mysql": {"0.1.0": pkg("orm-mysql", nil, map[string]string{"orm": "^0.1"})},
	}}
	_, err := run(t, f, map[string]string{"orm-mysql": dep("orm-mysql", "*")}, nil)
	if err == nil || !strings.Contains(err.Error(), "needs orm ^0.1") || !strings.Contains(err.Error(), "gtr add") {
		t.Fatalf("missing peer: %v", err)
	}
	// With orm in the project, the peer range steers the choice to 0.1.x.
	got, err := run(t, f, map[string]string{"orm-mysql": dep("orm-mysql", "*"), "orm": dep("orm", "*")}, nil)
	if err != nil || got["orm"] != "0.1.0" {
		t.Fatalf("peer constraint: %v %v", got, err)
	}
	_, err = run(t, f, map[string]string{"orm-mysql": dep("orm-mysql", "*"), "orm": dep("orm", "0.2.0")}, nil)
	if err == nil {
		t.Fatal("incompatible peer must fail")
	}
}

// Untagged repositories (heads) satisfy peer ranges, as checkPeers allows:
// a strategy needing passport ^0.1 installs before passport is tagged.
func TestPeerOnHead(t *testing.T) {
	f := &fake{pkgs: map[string]map[string]string{
		"passport":        {"0.0.0-gaaaaaaaaaaaa": pkg("passport", nil, nil)},
		"passport-oauth2": {"0.0.0-gbbbbbbbbbbbb": pkg("passport-oauth2", nil, map[string]string{"passport": "^0.1"})},
	}, heads: map[string]bool{"passport": true, "passport-oauth2": true}}
	got, err := run(t, f, map[string]string{"passport-oauth2": "github:o/passport-oauth2", "passport": "github:o/passport"}, nil)
	if err != nil || got["passport"] != "0.0.0-gaaaaaaaaaaaa" {
		t.Fatalf("peer on a head: %v %v", got, err)
	}
	// A dependency range still excludes a head.
	f.pkgs["needs"] = map[string]string{"1.0.0": pkg("needs", map[string]string{"passport": "github:o/passport#^0.1"}, nil)}
	if _, err := run(t, f, map[string]string{"needs": "github:o/needs#1.0.0"}, nil); err == nil {
		t.Fatal("a dependency range cannot match an untagged head")
	}
}

func TestHeadAndErrors(t *testing.T) {
	f := &fake{pkgs: map[string]map[string]string{"h": {"0.0.0-abcdef123456": pkg("h", nil, nil)}}, heads: map[string]bool{"h": true}}
	if got, err := run(t, f, map[string]string{"h": "github:o/h"}, nil); err != nil || got["h"] != "0.0.0-abcdef123456" {
		t.Fatalf("head: %v %v", got, err)
	}
	if _, err := run(t, f, map[string]string{"h": "github:o/h#^1"}, nil); err == nil {
		t.Fatal("a range cannot match an untagged head")
	}
	if _, err := run(t, f, map[string]string{"missing": "github:o/missing"}, nil); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing repo: %v", err)
	}
	f.pkgs["bad"] = map[string]string{"1.0.0": pkg("bad", map[string]string{"Bad Name": "*"}, nil)}
	if _, err := run(t, f, map[string]string{"bad": "github:o/bad"}, nil); err == nil {
		t.Fatal("invalid dependency key must fail")
	}
	f.pkgs["usesfile"] = map[string]string{"1.0.0": pkg("usesfile", map[string]string{"py": "file:../py"}, nil)}
	if _, err := run(t, f, map[string]string{"usesfile": "github:o/usesfile"}, nil); err == nil || !strings.Contains(err.Error(), "cannot use file:") {
		t.Fatalf("file: in published package: %v", err)
	}
	_, err := Resolve(context.Background(), func(spec.Kind) (source.Provider, error) { return nil, errors.New("no provider") },
		Request{Root: map[string]spec.Spec{"x": gh("x", "*")}})
	if err == nil || !strings.Contains(err.Error(), "no provider") {
		t.Fatalf("provider error: %v", err)
	}
}

func TestJoinFile(t *testing.T) {
	if joinFile("../libs/a", "../b") != "../libs/b" || joinFile("core", "../shared") != "shared" || joinFile("x", "/abs") != "/abs" || joinFile("x", "C:/y") != "C:/y" {
		t.Fatal("joinFile")
	}
}

// A peer constraint on a package that only arrives later in the search must
// still steer its version.
func TestPeerOnTransitive(t *testing.T) {
	f := &fake{pkgs: map[string]map[string]string{
		"adrv":  {"1.0.0": pkg("adrv", nil, map[string]string{"zcore": "^1"})},
		"bpkg":  {"1.0.0": pkg("bpkg", map[string]string{"zcore": dep("zcore", "*")}, nil)},
		"zcore": {"1.0.0": pkg("zcore", nil, nil), "2.0.0": pkg("zcore", nil, nil)},
	}}
	got, err := run(t, f, map[string]string{"adrv": dep("adrv", "*"), "bpkg": dep("bpkg", "*")}, nil)
	if err != nil || got["zcore"] != "1.0.0" {
		t.Fatalf("%v %v", got, err)
	}
}

// Old tags without gtr.json are skipped, not fatal.
func TestSkipVersionsWithoutManifest(t *testing.T) {
	f := &fake{pkgs: map[string]map[string]string{"old": {"1.0.0": "", "2.0.0": pkg("old", nil, nil)}}}
	f.noManifest = map[string]bool{"1.0.0": true}
	got, err := run(t, f, map[string]string{"old": dep("old", "*")}, nil)
	if err != nil || got["old"] != "2.0.0" {
		t.Fatalf("%v %v", got, err)
	}
	_, err = run(t, f, map[string]string{"old": dep("old", "<2")}, nil)
	if err == nil || !strings.Contains(err.Error(), "skipped (no gtr.json): old@1.0.0") {
		t.Fatalf("%v", err)
	}
}

// A locked commit of an untagged repository stays installable after the
// branch moved on.
func TestLockedHead(t *testing.T) {
	f := &fake{pkgs: map[string]map[string]string{"hd": {"0.0.0-gnewcommit00": pkg("hd", nil, nil)}}, heads: map[string]bool{"hd": true}}
	l := &lock.Lock{Packages: map[string]lock.Entry{"hd": {Version: "0.0.0-goldcommit00", Commit: "c-0.0.0-goldcommit00", Source: "github:o/hd"}}}
	f.pkgs["hd"]["0.0.0-goldcommit00"] = pkg("hd", nil, nil) // the manifest is still readable at the old commit
	got, err := run(t, f, map[string]string{"hd": "github:o/hd"}, l)
	if err != nil || got["hd"] != "0.0.0-goldcommit00" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestSourceConflictWithChosen(t *testing.T) {
	f := &fake{pkgs: map[string]map[string]string{
		"aa": {"1.0.0": pkg("aa", map[string]string{"cc": "github:o/cc#*"}, nil)},
		"bb": {"1.0.0": pkg("bb", map[string]string{"cc": "github:fork/cc#*"}, nil)},
		"cc": {"1.0.0": pkg("cc", nil, nil)},
	}}
	if _, err := run(t, f, map[string]string{"aa": dep("aa", "*"), "bb": dep("bb", "*")}, nil); err == nil || !strings.Contains(err.Error(), "two different sources") {
		t.Fatalf("%v", err)
	}
}
