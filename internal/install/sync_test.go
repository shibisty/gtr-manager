package install

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gtr-manager/internal/gomod"
	"gtr-manager/internal/gomodules"
	"gtr-manager/internal/lock"
	"gtr-manager/internal/manifest"
)

func TestCarryOver(t *testing.T) {
	newManifest := func() *manifest.Manifest {
		return &manifest.Manifest{Name: "app", Dependencies: map[string]string{
			"orm":                          "github:shibisty/orm.go#^0.1",
			"github.com/google/uuid":       "^1.3.0",
			"github.com/cespare/xxhash/v2": "^2.1",
		}, DevDependencies: map[string]string{"github.com/stretchr/testify": "^1.8.0"}}
	}
	l := &lock.Lock{Packages: map[string]lock.Entry{"orm": {Version: "0.1.3"}}}
	generated := `module app

go 1.22

require (
	github.com/cespare/xxhash/v2 v2.3.0
	github.com/google/uuid v1.6.0
	github.com/stretchr/testify v1.9.0
	orm v0.1.3
)

replace orm => ./gtr_modules/orm
`
	t.Run("generated go.mod: nothing to carry", func(t *testing.T) {
		m := newManifest()
		changes, err := carryOver(m, gomod.Parse([]byte(generated)), l, false)
		if err != nil || len(changes) != 0 {
			t.Fatalf("%v %v", changes, err)
		}
	})
	t.Run("edits", func(t *testing.T) {
		m := newManifest()
		edited := strings.Replace(generated, "\tgithub.com/google/uuid v1.6.0\n", "\tgithub.com/google/uuid v2.0.0+incompatible\n\tgolang.org/x/sync v0.8.0\n", 1)
		edited = strings.Replace(edited, "\tgithub.com/cespare/xxhash/v2 v2.3.0\n", "", 1)
		edited = strings.Replace(edited, "\tgithub.com/stretchr/testify v1.9.0\n", "\tgithub.com/stretchr/testify v2.0.0+incompatible\n\tmylib v0.0.0\n", 1)
		edited += "replace mylib => ../mylib\n"
		edited = strings.Replace(edited, "go 1.22", "go 1.24", 1)
		changes, err := carryOver(m, gomod.Parse([]byte(edited)), l, false)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"orm": "github:shibisty/orm.go#^0.1", "github.com/google/uuid": "^2.0.0",
			"golang.org/x/sync": "^0.8", "mylib": "file:../mylib"}
		if len(m.Dependencies) != len(want) {
			t.Fatalf("dependencies: %v", m.Dependencies)
		}
		for k, v := range want {
			if m.Dependencies[k] != v {
				t.Fatalf("%s = %q, want %q (%v)", k, m.Dependencies[k], v, m.Dependencies)
			}
		}
		if m.DevDependencies["github.com/stretchr/testify"] != "^2.0.0" {
			t.Fatalf("devDependencies: %v", m.DevDependencies)
		}
		if m.Engines.Go != "" {
			t.Fatalf("a generated go directive must not change engines.go: %q", m.Engines.Go)
		}
		joined := strings.Join(changes, "\n")
		for _, s := range []string{"removed github.com/cespare/xxhash/v2", "added golang.org/x/sync", "mylib: \"file:../mylib\""} {
			if !strings.Contains(joined, s) {
				t.Fatalf("changes lack %q:\n%s", s, joined)
			}
		}
	})
	t.Run("problems", func(t *testing.T) {
		for _, c := range []struct{ edit, want string }{
			{"\tstrutil v1.0.0\n", "strutil: a gtr package needs a source"},
			{"\torm v0.2.0\n", "change gtr package versions in gtr.json"},
			{"replace github.com/google/uuid => ../uuid\n", "only for gtr packages"},
			{"replace github.com/google/uuid => github.com/gofrs/uuid v4.0.0\n", "another module is not supported"},
			{"module other\n", "comes from \"name\" in gtr.json"},
		} {
			text := generated
			switch {
			case strings.HasPrefix(c.edit, "\torm"):
				text = strings.Replace(text, "\torm v0.1.3\n", c.edit, 1)
			case strings.HasPrefix(c.edit, "\t"):
				text = strings.Replace(text, "require (\n", "require (\n"+c.edit, 1)
			case strings.HasPrefix(c.edit, "module"):
				text = strings.Replace(text, "module app\n", c.edit, 1)
			default:
				text += c.edit
			}
			_, err := carryOver(newManifest(), gomod.Parse([]byte(text)), l, false)
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "gtr sync --force") {
				t.Errorf("%q: %v", c.edit, err)
			}
		}
	})
	t.Run("foreign go.mod keeps gtr.json entries", func(t *testing.T) {
		m := newManifest()
		changes, err := carryOver(m, gomod.Parse([]byte("module legacy\n\ngo 1.23.4\n\nrequire golang.org/x/text v0.21.0\n")), &lock.Lock{}, true)
		if err != nil {
			t.Fatal(err)
		}
		if m.Dependencies["orm"] == "" || m.Dependencies["golang.org/x/text"] != "^0.21" || len(changes) != 1 {
			t.Fatalf("%v %v", changes, m.Dependencies)
		}
	})
	t.Run("quoted paths, tidy and unsupported directives", func(t *testing.T) {
		m := newManifest()
		lk := &lock.Lock{Packages: map[string]lock.Entry{"orm": {Version: "0.1.3", Source: "github:shibisty/orm.go"}, "strutil": {Version: "1.0.0", Source: "github:shibisty/strutil.go"}}}
		text := strings.Replace(generated, "require (\n", "require (\n\tstrutil v1.0.0\n\tmy-lib v0.0.0\n", 1) + "replace my-lib => \"../my lib\"\n"
		if _, err := carryOver(m, gomod.Parse([]byte(text)), lk, false); err != nil {
			t.Fatal(err) // strutil: installed for another package, marked direct by go mod tidy
		}
		if m.Dependencies["my-lib"] != "file:../my lib" || m.Dependencies["strutil"] != "" {
			t.Fatalf("%v", m.Dependencies)
		}
		_, err := carryOver(newManifest(), gomod.Parse([]byte(generated+"tool golang.org/x/tools/cmd/stringer\n")), lk, false)
		if err == nil || !strings.Contains(err.Error(), "tool golang.org/x/tools/cmd/stringer: gtr.json has no equivalent") {
			t.Fatalf("tool: %v", err)
		}
	})
}

func TestSync(t *testing.T) {
	gh := universe(t)
	p := newProject(t, gh, `{"name":"app","version":"1.0.0","engines":{"go":">=1.22"},"dependencies":{"strutil":"github:shibisty/strutil.go","orm":"github:shibisty/orm.go#^0.1"}}`)
	if err := p.in.Install(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(filepath.Dir(p.dir), filepath.Base(p.dir)+"-lib")
	os.MkdirAll(lib, 0o755)
	t.Cleanup(func() { os.RemoveAll(lib) })
	os.WriteFile(filepath.Join(lib, "gtr.json"), []byte(gtrJSON("mylib", nil, nil)), 0o644)
	os.WriteFile(filepath.Join(lib, "lib.go"), []byte("package mylib\n"), 0o644)
	rel := "../" + filepath.Base(lib)

	// Hand edits: drop orm, point a new package at a local directory.
	edited := strings.Replace(p.read("go.mod"), "\torm v0.1.3\n", "\tmylib v0.0.0\n", 1)
	edited = strings.Replace(edited, "replace orm => ./gtr_modules/orm\n", "replace mylib => "+rel+"\n", 1)
	os.WriteFile(filepath.Join(p.dir, "go.mod"), []byte(edited), 0o644)
	if state, _ := gomod.Check(filepath.Join(p.dir, "go.mod")); state != gomod.Edited {
		t.Fatalf("state %v\n%s", state, edited)
	}
	if err := p.in.Sync(ctx, false); err != nil {
		t.Fatal(err)
	}
	m := p.manifest(t)
	if _, ok := m.Dependencies["orm"]; ok || m.Dependencies["mylib"] != "file:"+rel || m.Dependencies["strutil"] == "" {
		t.Fatalf("gtr.json: %v", m.Dependencies)
	}
	goMod := p.read("go.mod")
	if state, _ := gomod.Check(filepath.Join(p.dir, "go.mod")); state != gomod.Generated || !strings.Contains(goMod, "replace mylib => ./gtr_modules/mylib") {
		t.Fatalf("go.mod not regenerated:\n%s", goMod)
	}
	if _, err := os.Stat(filepath.Join(p.dir, "gtr_modules", "orm")); !os.IsNotExist(err) {
		t.Fatalf("orm link left behind: %v", err)
	}
	if !strings.Contains(p.log.String(), "removed orm") {
		t.Fatalf("log: %s", p.log)
	}

	// A problem leaves both files untouched.
	before := p.read("gtr.json")
	broken := strings.Replace(p.read("go.mod"), "require (\n", "require (\n\tnosource v1.0.0\n", 1)
	os.WriteFile(filepath.Join(p.dir, "go.mod"), []byte(broken), 0o644)
	if err := p.in.Sync(ctx, false); err == nil || !strings.Contains(err.Error(), "gtr add") {
		t.Fatalf("no source: %v", err)
	}
	if p.read("gtr.json") != before || p.read("go.mod") != broken {
		t.Fatal("a failed sync changed files")
	}
	// --force discards the edit.
	if err := p.in.Sync(ctx, true); err != nil || strings.Contains(p.read("go.mod"), "nosource") {
		t.Fatalf("--force: %v", err)
	}
}

// An existing Go project adopts gtr: `gtr init` + `gtr sync` imports go.mod.
func TestSyncImportsForeignGoMod(t *testing.T) {
	gh := universe(t)
	p := newProject(t, gh, `{"name":"app","version":"1.0.0"}`)
	realModules(t, p)
	os.WriteFile(filepath.Join(p.dir, "go.mod"), []byte("module app\n\ngo 1.23.4\n\nrequire github.com/redis/go-redis/v9 v9.7.0\n\nrequire github.com/cespare/xxhash/v2 v2.2.0 // indirect\n"), 0o644)
	if err := p.in.Install(ctx, Options{}); err == nil || !strings.Contains(err.Error(), "not generated by gtr") {
		t.Fatalf("install over a foreign go.mod: %v", err)
	}
	if err := p.in.Sync(ctx, false); err != nil {
		t.Fatal(err)
	}
	m := p.manifest(t)
	if m.Dependencies["github.com/redis/go-redis/v9"] != "^9.7.0" || len(m.Dependencies) != 1 || m.Engines.Go != ">=1.23" {
		t.Fatalf("gtr.json: %v %q", m.Dependencies, m.Engines.Go)
	}
	if state, _ := gomod.Check(filepath.Join(p.dir, "go.mod")); state != gomod.Generated || !strings.Contains(p.read("go.mod"), "github.com/redis/go-redis/v9 v9.7.0") {
		t.Fatalf("go.mod:\n%s", p.read("go.mod"))
	}
}

// fakeModules serves made-up modules (path → version → go.mod) as a GOPROXY
// with a matching checksum database.
func fakeModules(t *testing.T, p *project, mods map[string]map[string]string) {
	find := func(escaped string) string {
		for m := range mods {
			if e, _ := gomodules.Escape(m); e == escaped {
				return m
			}
		}
		return ""
	}
	zipOf := func(path, v string) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for name, body := range map[string]string{"go.mod": mods[path][v], "x.go": "package x\n"} {
			w, _ := zw.Create(path + "@" + v + "/" + name)
			w.Write([]byte(body))
		}
		zw.Close()
		return buf.Bytes()
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := strings.TrimPrefix(r.URL.Path, "/")
		i := strings.Index(u, "/@v/")
		if i < 0 || find(u[:i]) == "" {
			http.NotFound(w, r)
			return
		}
		path, rest := find(u[:i]), u[i+4:]
		switch {
		case rest == "list":
			for v := range mods[path] {
				fmt.Fprintln(w, v)
			}
		case strings.HasSuffix(rest, ".mod"):
			fmt.Fprint(w, mods[path][strings.TrimSuffix(rest, ".mod")])
		case strings.HasSuffix(rest, ".zip"):
			w.Write(zipOf(path, strings.TrimSuffix(rest, ".zip")))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(proxy.Close)
	db := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/lookup/")
		at := strings.LastIndex(key, "@")
		path, v := find(key[:at]), key[at+1:]
		if path == "" {
			http.NotFound(w, r)
			return
		}
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mods[path][v]), 0o644)
		os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x\n"), 0o644)
		h, _ := gomodules.Hash(dir, path, v)
		fmt.Fprintf(w, "1\n%s %s %s\n%s %s/go.mod %s\n", path, v, h, path, v, gomodules.HashMod([]byte(mods[path][v])))
	}))
	t.Cleanup(db.Close)
	p.in.GoProxy = &gomodules.Proxies{Public: &gomodules.Proxy{URL: proxy.URL, Client: proxy.Client(), Cache: filepath.Join(p.home.Root, "cache", "download", "go")}}
	p.in.SumDB = &gomodules.SumDB{URL: db.URL, Client: db.Client()}
}

// Raising an indirect module or the go directive in go.mod is carried over
// instead of being reverted by the regeneration.
func TestSyncRaisesIndirectAndGo(t *testing.T) {
	p := newProject(t, universe(t), `{"name":"app","version":"1.0.0","engines":{"go":">=1.22"},"dependencies":{"example.com/top":"^1.0.0"}}`)
	fakeModules(t, p, map[string]map[string]string{
		"example.com/top": {"v1.0.0": "module example.com/top\n\ngo 1.21\n\nrequire example.com/low v1.1.0\n"},
		"example.com/low": {"v1.1.0": "module example.com/low\n\ngo 1.21\n", "v1.3.0": "module example.com/low\n\ngo 1.21\n", "v1.4.0": "module example.com/low\n\ngo 1.21\n"},
	})
	if err := p.in.Install(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.read("go.mod"), "example.com/low v1.1.0 // indirect") {
		t.Fatalf("go.mod:\n%s", p.read("go.mod"))
	}
	edited := strings.Replace(p.read("go.mod"), "example.com/low v1.1.0 // indirect", "example.com/low v1.3.0 // indirect", 1)
	edited = strings.Replace(edited, "\ngo 1.22\n", "\ngo 1.24\n", 1)
	os.WriteFile(filepath.Join(p.dir, "go.mod"), []byte(edited), 0o644)
	if err := p.in.Sync(ctx, false); err != nil {
		t.Fatal(err)
	}
	m := p.manifest(t)
	if m.Dependencies["example.com/low"] != "^1.3.0" || m.Engines.Go != ">=1.24" {
		t.Fatalf("gtr.json: %v %q\n%s", m.Dependencies, m.Engines.Go, p.log)
	}
	goMod := p.read("go.mod")
	if !strings.Contains(goMod, "\texample.com/low v1.3.0\n") || !strings.Contains(goMod, "\ngo 1.24\n") {
		t.Fatalf("go.mod (v1.3.0 kept, not v1.4.0):\n%s", goMod)
	}
	// Nothing left to carry: a second sync changes nothing.
	p.log.Reset()
	before := p.read("gtr.json")
	if err := p.in.Sync(ctx, false); err != nil || p.read("gtr.json") != before {
		t.Fatalf("second sync: %v\n%s", err, p.log)
	}
	// engines.go with an upper bound is not rewritten silently.
	m.Engines.Go = ">=1.22 <1.24"
	manifest.Save(filepath.Join(p.dir, "gtr.json"), m)
	os.WriteFile(filepath.Join(p.dir, "go.mod"), []byte(strings.Replace(p.read("go.mod"), "\ngo 1.24\n", "\ngo 1.25rc1\n", 1)), 0o644)
	if err := p.in.Sync(ctx, false); err == nil || !strings.Contains(err.Error(), "change engines.go") {
		t.Fatalf("bounded engines.go: %v", err)
	}
}
