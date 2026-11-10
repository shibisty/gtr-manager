package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gtr-manager/internal/registry"
	"gtr-manager/internal/registry/registrytest"
	"gtr-manager/internal/source"
	"gtr-manager/internal/spec"
)

func regJSON(name, version string, deps map[string]string) string {
	m := map[string]any{"name": name, "version": version, "engines": map[string]string{"go": ">=1.22"}}
	if deps != nil {
		m["dependencies"] = deps
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// regProject is a project whose registry is a fake one.
func regProject(t *testing.T, gtr string) (*project, *registrytest.Server) {
	t.Helper()
	reg := registrytest.New(t)
	t.Setenv("GTR_REGISTRY", reg.URL)
	t.Setenv("GTR_HOME", t.TempDir())
	for _, v := range []string{"1.0.0", "1.1.0", "2.0.0-rc.1"} {
		reg.Add(t, map[string]string{
			"gtr.json":   regJSON("strutil", v, nil),
			"strutil.go": "package strutil\n\nconst Version = \"" + v + "\"\n",
		})
	}
	reg.Add(t, map[string]string{
		"gtr.json":       regJSON("textkit", "0.2.0", map[string]string{"strutil": "^1"}),
		"textkit.go":     "package textkit\n\nimport \"strutil\"\n\nconst Uses = strutil.Version\n",
		"words/words.go": "package words\n",
	})
	p := newProject(t, universe(t), gtr)
	p.in.Registry = &source.Registry{Client: reg.Client()}
	return p, reg
}

func TestRegistryDependencies(t *testing.T) {
	p, reg := regProject(t, `{"name":"app","version":"1.0.0","engines":{"go":">=1.22"},"dependencies":{"textkit":"^0.2"}}`)
	if err := p.in.Install(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	l := p.lock(t)
	tk, su := l.Packages["textkit"], l.Packages["strutil"]
	if tk.Version != "0.2.0" || su.Version != "1.1.0" || tk.Source != "registry:"+reg.URL ||
		!strings.HasPrefix(su.Commit, "sha256:") || !strings.HasPrefix(su.Integrity, "h1:") {
		t.Fatalf("%+v", l.Packages)
	}
	if _, err := os.Stat(filepath.Join(p.dir, "gtr_modules", "textkit", "words", "words.go")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.read("go.mod"), "textkit v0.2.0") {
		t.Fatal(p.read("go.mod"))
	}

	// A yanked version stays for the lock, and is skipped otherwise.
	reg.Yank("strutil", "1.1.0")
	p.in.Registry = &source.Registry{Client: reg.Client()}
	if err := p.in.Install(ctx, Options{}); err != nil || p.lock(t).Packages["strutil"].Version != "1.1.0" {
		t.Fatal("locked yanked version", err)
	}
	if err := p.in.Install(ctx, Options{Update: map[string]bool{"*": true}}); err != nil || p.lock(t).Packages["strutil"].Version != "1.0.0" {
		t.Fatal("update past a yanked version", err, p.lock(t).Packages["strutil"])
	}

	// Offline: from the store.
	requests := len(reg.Requests)
	p.in.Offline = true
	if err := p.in.Install(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	if len(reg.Requests) != requests {
		t.Fatal("offline install asked the registry")
	}
}

func TestRegistryAdd(t *testing.T) {
	p, reg := regProject(t, `{"name":"app","version":"1.0.0","engines":{"go":">=1.22"}}`)
	if err := p.in.Add(ctx, []string{"strutil", "textkit@^0.2.0"}, false); err != nil {
		t.Fatal(err)
	}
	if d := p.manifest(t).Dependencies; d["strutil"] != "^1.1.0" || d["textkit"] != "^0.2.0" {
		t.Fatal(d)
	}
	reg.Yank("strutil", "1.1.0")
	p2, _ := regProject(t, `{"name":"app2","version":"1.0.0"}`)
	p2.in.Registry = &source.Registry{Client: reg.Client()}
	t.Setenv("GTR_REGISTRY", reg.URL)
	if err := p2.in.Add(ctx, []string{"strutil"}, false); err != nil || p2.manifest(t).Dependencies["strutil"] != "^1.0.0" {
		t.Fatal(err, p2.manifest(t).Dependencies)
	}
	for arg, want := range map[string]string{
		"nothing":     "no package nothing",
		"strutil@^x":  "invalid version range",
		"Bad_Name":    "add other packages by their source",
		"orm=strutil": "added by its name",
	} {
		if err := p.in.Add(ctx, []string{arg}, false); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", arg, err, want)
		}
	}
	// an unreachable registry says so
	p.in.Registry = &source.Registry{Client: &registry.Client{URL: "http://127.0.0.1:1"}}
	if err := p.in.Add(ctx, []string{"strutil"}, false); err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Fatal(err)
	}
}

func TestRegistryChecksum(t *testing.T) {
	p, reg := regProject(t, `{"name":"app","version":"1.0.0","dependencies":{"strutil":"^1"}}`)
	// the index promises another archive
	ix, err := reg.Client().Package(ctx, "strutil")
	if err != nil {
		t.Fatal(err)
	}
	bad := &source.Registry{Client: reg.Client()}
	if _, err := bad.Candidates(ctx, "strutil", parseSpec(t, "^1")); err != nil {
		t.Fatal(err)
	}
	cands, _ := bad.Candidates(ctx, "strutil", parseSpec(t, "^1"))
	c := cands[1] // 1.1.0
	c.SHA256 = strings.Repeat("0", 64)
	if err := bad.Fetch(ctx, parseSpec(t, "^1"), c, filepath.Join(t.TempDir(), "x")); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatal(err)
	}
	_ = ix
	_ = p
}

func parseSpec(t *testing.T, v string) spec.Spec {
	t.Helper()
	s, err := spec.Parse("strutil", v)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
