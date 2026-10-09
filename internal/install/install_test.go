package install

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gtr-manager/internal/gomod"
	"gtr-manager/internal/gomodules"
	"gtr-manager/internal/home"
	"gtr-manager/internal/lock"
	"gtr-manager/internal/manifest"
	"gtr-manager/internal/source"
	"gtr-manager/internal/source/githubtest"
)

var ctx = context.Background()

func gtrJSON(name string, deps, peers map[string]string) string {
	m := map[string]any{"name": name, "version": "0.0.0", "engines": map[string]string{"go": ">=1.22"}}
	if deps != nil {
		m["dependencies"] = deps
	}
	if peers != nil {
		m["peerDependencies"] = peers
	}
	data, _ := json.Marshal(m)
	return string(data)
}

// universe publishes orm (core with a subpackage) and orm-mysql (a driver
// that needs orm as a peer and pulls in a helper package).
func universe(t *testing.T) *githubtest.Server {
	gh := githubtest.New(t)
	for _, v := range []string{"v0.1.0", "v0.1.3", "v0.2.0"} {
		gh.Commit("shibisty/orm.go", map[string]string{
			"gtr.json":         gtrJSON("orm", nil, nil),
			"orm.go":           "package orm\n\nconst Version = \"" + v + "\"\n\nfunc Name() string { return \"orm\" }\n",
			"schema/schema.go": "package schema\n\nfunc Table() string { return \"users\" }\n",
			"go.mod":           "module github.com/shibisty/orm.go\n\ngo 1.22\n", // a published go.mod is replaced in the store
		}, v)
	}
	gh.Commit("shibisty/strutil.go", map[string]string{
		"gtr.json":   gtrJSON("strutil", nil, nil),
		"strutil.go": "package strutil\n\nimport \"strings\"\n\nfunc Up(s string) string { return strings.ToUpper(s) }\n",
	}, "v1.0.0")
	gh.Commit("shibisty/orm.go-mysql-driver", map[string]string{
		"gtr.json": gtrJSON("orm-mysql", map[string]string{"strutil": "github:shibisty/strutil.go#^1"}, map[string]string{"orm": "^0.1"}),
		"mysql.go": "package mysql\n\nimport (\n\t\"orm\"\n\t\"strutil\"\n)\n\nfunc Driver() string { return strutil.Up(orm.Name() + \"-mysql\") }\n",
	}, "v0.1.0")
	return gh
}

type project struct {
	dir  string
	home home.Layout
	in   *Installer
	log  *bytes.Buffer
	gh   *githubtest.Server
}

func newProject(t *testing.T, gh *githubtest.Server, gtr string) *project {
	p := &project{dir: t.TempDir(), home: home.Layout{Root: t.TempDir()}, log: &bytes.Buffer{}, gh: gh}
	os.WriteFile(filepath.Join(p.dir, "gtr.json"), []byte(gtr), 0o644)
	p.in = &Installer{Dir: p.dir, Home: p.home, GitHub: &source.GitHub{API: gh.URL, Client: gh.Client(), Tmp: p.home.Tmp()}, Log: p.log}
	return p
}

func (p *project) read(name string) string {
	data, _ := os.ReadFile(filepath.Join(p.dir, name))
	return string(data)
}

func (p *project) manifest(t *testing.T) *manifest.Manifest {
	m, err := manifest.Load(filepath.Join(p.dir, "gtr.json"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (p *project) lock(t *testing.T) *lock.Lock {
	l, err := lock.Load(filepath.Join(p.dir, "gtr.lock"))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestAddInstallsAndGeneratesEverything(t *testing.T) {
	gh := universe(t)
	p := newProject(t, gh, `{"name":"app","version":"1.0.0","engines":{"go":">=1.22"}}`)
	if err := p.in.Add(ctx, []string{"github:shibisty/orm.go#^0.1", "github:shibisty/orm.go-mysql-driver"}, false); err != nil {
		t.Fatal(err)
	}
	m := p.manifest(t)
	if m.Dependencies["orm"] != "github:shibisty/orm.go#^0.1" || m.Dependencies["orm-mysql"] != "github:shibisty/orm.go-mysql-driver#^0.1" {
		t.Fatalf("gtr.json: %v", m.Dependencies)
	}
	l := p.lock(t)
	if l.Packages["orm"].Version != "0.1.3" || l.Packages["strutil"].Version != "1.0.0" || !strings.HasPrefix(l.Packages["orm"].Integrity, "h1:") ||
		l.Packages["orm-mysql"].Dependencies["strutil"] != "github:shibisty/strutil.go#^1" || l.Packages["orm"].Source != "github:shibisty/orm.go" {
		t.Fatalf("lock: %+v", l.Packages)
	}
	// gtr_modules links into the store; the store copy has a generated go.mod.
	ormDir := filepath.Join(p.dir, "gtr_modules", "orm")
	if _, err := os.Stat(filepath.Join(ormDir, "schema", "schema.go")); err != nil {
		t.Fatal(err)
	}
	if mod, _ := gomod.ModulePath(filepath.Join(ormDir, "go.mod")); mod != "orm" {
		t.Fatalf("package go.mod declares %q", mod)
	}
	if !strings.Contains(read(t, filepath.Join(p.dir, "gtr_modules", "orm-mysql", "go.mod")), "orm v0.0.0-0") {
		t.Fatal("peer must be required in the package go.mod")
	}
	if st, _ := gomod.Check(filepath.Join(p.dir, "go.mod")); st != gomod.Generated {
		t.Fatalf("go.mod state %v", st)
	}
	goMod := p.read("go.mod")
	for _, want := range []string{"module app\n\ngo 1.22\n", "require (\n\torm v0.1.3\n\torm-mysql v0.1.0\n)", "require strutil v1.0.0 // indirect",
		"replace orm => ./gtr_modules/orm\n", "replace strutil => ./gtr_modules/strutil\n"} {
		if !strings.Contains(goMod, want) {
			t.Errorf("go.mod lacks %q:\n%s", want, goMod)
		}
	}
	if gi := p.read(".gitignore"); !strings.Contains(gi, "gtr_modules/\ngo.mod\ngo.work\n") {
		t.Fatalf(".gitignore: %q", gi)
	}
	// A second install downloads nothing and changes nothing.
	before, downloads := goMod, gh.Zipball
	if err := p.in.Install(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	if gh.Zipball != downloads || p.read("go.mod") != before {
		t.Fatalf("reinstall: %d downloads", gh.Zipball-downloads)
	}
	// A new project reuses the store.
	q := newProject(t, gh, p.read("gtr.json"))
	q.home = p.home
	q.in.Home = p.home
	os.WriteFile(filepath.Join(q.dir, "gtr.lock"), []byte(p.read("gtr.lock")), 0o644)
	if err := q.in.Install(ctx, Options{}); err != nil || gh.Zipball != downloads {
		t.Fatalf("store reuse: %v, %d downloads", err, gh.Zipball-downloads)
	}
}

func read(t *testing.T, path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRemoveUpdateAndCI(t *testing.T) {
	gh := universe(t)
	p := newProject(t, gh, `{"name":"app","version":"1.0.0","dependencies":{"orm":"github:shibisty/orm.go#^0.1"}}`)
	if err := p.in.Install(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	if err := p.in.Install(ctx, Options{Frozen: true}); err != nil {
		t.Fatalf("ci on a fresh lock: %v", err)
	}
	// A new tag appears: install keeps the lock, update takes it.
	gh.Commit("shibisty/orm.go", map[string]string{"gtr.json": gtrJSON("orm", nil, nil), "orm.go": "package orm\n"}, "v0.1.9")
	p.in.GitHub = &source.GitHub{API: gh.URL, Client: gh.Client(), Tmp: p.home.Tmp()} // drop the tag cache
	p.in.Install(ctx, Options{})
	if v := p.lock(t).Packages["orm"].Version; v != "0.1.3" {
		t.Fatalf("install must keep the locked version: %s", v)
	}
	if err := p.in.Install(ctx, Options{Update: map[string]bool{"orm": true}}); err != nil {
		t.Fatal(err)
	}
	if v := p.lock(t).Packages["orm"].Version; v != "0.1.9" {
		t.Fatalf("update: %s", v)
	}
	// gtr.json changed behind the lock's back: ci refuses.
	m := p.manifest(t)
	m.Dependencies["strutil"] = "github:shibisty/strutil.go"
	manifest.Save(filepath.Join(p.dir, "gtr.json"), m)
	if err := p.in.Install(ctx, Options{Frozen: true}); err == nil || !strings.Contains(err.Error(), "+ strutil@1.0.0") {
		t.Fatalf("ci: %v", err)
	}
	if err := p.in.Remove(ctx, []string{"orm"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(p.dir, "gtr_modules", "orm")); !os.IsNotExist(err) {
		t.Fatal("removed package link must disappear")
	}
	if strings.Contains(p.read("go.mod"), "orm") || p.manifest(t).Dependencies["orm"] != "" {
		t.Fatal("orm still referenced")
	}
	if err := p.in.Remove(ctx, []string{"nothere"}); err == nil {
		t.Fatal("removing an unknown package must fail")
	}
}

func TestMovedTagIsRejected(t *testing.T) {
	gh := universe(t)
	p := newProject(t, gh, `{"name":"app","version":"1.0.0","dependencies":{"strutil":"github:shibisty/strutil.go#1.0.0"}}`)
	if err := p.in.Install(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	// Same version and commit in the lock, but the store copy is gone and the
	// download differs: integrity must catch it.
	l := p.lock(t)
	e := l.Packages["strutil"]
	e.Integrity = "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	l.Packages["strutil"] = e
	data, _ := lock.Marshal(l)
	os.WriteFile(filepath.Join(p.dir, "gtr.lock"), data, 0o644)
	if err := p.in.Install(ctx, Options{}); err == nil || !strings.Contains(err.Error(), "integrity mismatch") {
		t.Fatalf("got %v", err)
	}
}

func TestGoModGuards(t *testing.T) {
	gh := universe(t)
	p := newProject(t, gh, `{"name":"app","version":"1.0.0","dependencies":{"strutil":"github:shibisty/strutil.go"}}`)
	os.WriteFile(filepath.Join(p.dir, "go.mod"), []byte("module mine\n"), 0o644)
	if err := p.in.Install(ctx, Options{}); err == nil || !strings.Contains(err.Error(), "not generated by gtr") {
		t.Fatalf("foreign go.mod: %v", err)
	}
	if err := p.in.Install(ctx, Options{ForceMod: true}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(p.dir, "go.mod"), []byte(p.read("go.mod")+"\nrequire x v1.0.0\n"), 0o644)
	if err := p.in.Install(ctx, Options{}); err == nil || !strings.Contains(err.Error(), "edited by hand") {
		t.Fatalf("edited go.mod: %v", err)
	}
	if err := p.in.Install(ctx, Options{ForceMod: true}); err != nil || strings.Contains(p.read("go.mod"), "require x") {
		t.Fatalf("sync --force: %v", err)
	}
	// CRLF line endings (Git on Windows) do not count as an edit.
	os.WriteFile(filepath.Join(p.dir, "go.mod"), []byte(strings.ReplaceAll(p.read("go.mod"), "\n", "\r\n")), 0o644)
	if err := p.in.Install(ctx, Options{}); err != nil {
		t.Fatalf("CRLF: %v", err)
	}
}

func TestLegacyMigration(t *testing.T) {
	gh := universe(t)
	p := newProject(t, gh, `{"name":"app","version":"1.0.0","author":"","website":"","entrypoint":"main.go","engine":">=1.22",
		"dependencies":{"github:shibisty/strutil.go":"*"},"scripts":{}}`)
	if err := p.in.Install(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	m := p.manifest(t)
	if m.Dependencies["strutil"] != "github:shibisty/strutil.go" || m.Engines.Go != ">=1.22" || strings.Contains(p.read("gtr.json"), "entrypoint") {
		t.Fatalf("migrated: %s", p.read("gtr.json"))
	}
	if !strings.Contains(p.log.String(), "migrated") {
		t.Fatalf("log: %s", p.log)
	}
}

func TestFilePackages(t *testing.T) {
	gh := universe(t)
	p := newProject(t, gh, `{"name":"app","version":"1.0.0"}`)
	lib := filepath.Join(filepath.Dir(p.dir), filepath.Base(p.dir)+"-lib")
	os.MkdirAll(lib, 0o755)
	t.Cleanup(func() { os.RemoveAll(lib) })
	os.WriteFile(filepath.Join(lib, "gtr.json"), []byte(gtrJSON("mylib", map[string]string{"strutil": "github:shibisty/strutil.go#^1"}, nil)), 0o644)
	os.WriteFile(filepath.Join(lib, "lib.go"), []byte("package mylib\n"), 0o644)
	rel := "../" + filepath.Base(lib)
	if err := p.in.Add(ctx, []string{"file:" + rel}, true); err != nil {
		t.Fatal(err)
	}
	if p.manifest(t).DevDependencies["mylib"] != "file:"+rel {
		t.Fatalf("devDependencies: %v", p.manifest(t).DevDependencies)
	}
	// A go.mod is generated in the local package; the link points at it in place.
	if !gomod.IsPackageGenerated(filepath.Join(lib, "go.mod")) {
		t.Fatal("file: package go.mod not generated")
	}
	os.WriteFile(filepath.Join(lib, "new.go"), []byte("package mylib\n"), 0o644)
	if _, err := os.Stat(filepath.Join(p.dir, "gtr_modules", "mylib", "new.go")); err != nil {
		t.Fatal("file: packages must be linked, not copied")
	}
	// An author-written go.mod with another module path is an error.
	os.WriteFile(filepath.Join(lib, "go.mod"), []byte("module github.com/me/mylib\n"), 0o644)
	if err := p.in.Install(ctx, Options{}); err == nil || !strings.Contains(err.Error(), "declares module") {
		t.Fatalf("foreign package go.mod: %v", err)
	}
}

func TestAddErrors(t *testing.T) {
	gh := universe(t)
	gh.Commit("someone/plain", map[string]string{"main.go": "package main\n"}, "v1.0.0")
	p := newProject(t, gh, `{"name":"app","version":"1.0.0"}`)
	p.in.GoProxy = &gomodules.Proxies{Public: &gomodules.Proxy{URL: "http://127.0.0.1:1", Client: http.DefaultClient}}
	for arg, want := range map[string]string{
		"orm":                            "registry is not available",
		"github.com/go-sql-driver/mysql": "module proxy",
		"github:someone/plain":           "no gtr.json",
		"github:shibisty/orm.go#^9":      "no version matches",
		"strutil=github:shibisty/orm.go": `named "orm"`,
		"github:missing/repo":            "not found",
	} {
		if err := p.in.Add(ctx, []string{arg}, false); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("add %s: %v, want %q", arg, err, want)
		}
	}
	if len(p.manifest(t).Dependencies) != 0 {
		t.Fatal("failed adds must not change gtr.json")
	}
	if err := p.in.Add(ctx, nil, false); err == nil {
		t.Fatal("add without arguments")
	}
}

// TestGoBuild builds a generated project with the real go command in the
// gtr environment (ADR-0001, item 5).
func TestGoBuild(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not in PATH")
	}
	gh := universe(t)
	p := newProject(t, gh, `{"name":"app","version":"1.0.0","engines":{"go":">=1.22"}}`)
	if err := p.in.Add(ctx, []string{"github:shibisty/orm.go#^0.1", "github:shibisty/orm.go-mysql-driver"}, false); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(p.dir, "main.go"), []byte(`package main

import (
	"fmt"

	"orm"
	mysql "orm-mysql"
	"orm/schema"
	"strutil" // only a transitive dependency: go moves it to the direct block
)

func main() { fmt.Println(orm.Version, schema.Table(), mysql.Driver(), strutil.Up("ok")) }
`), 0o644)
	cmd := exec.Command(goBin, "run", ".")
	cmd.Dir = p.dir
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOFLAGS=-mod=mod", "GOWORK=off", "GOTOOLCHAIN=local", "GOFLAGS=-mod=mod")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "v0.1.3 users ORM-MYSQL OK" {
		t.Fatalf("output %q", got)
	}
	// go may only have done its own bookkeeping, which gtr accepts.
	if st, _ := gomod.Check(filepath.Join(p.dir, "go.mod")); st != gomod.Generated {
		t.Fatalf("go modified go.mod:\n%s", p.read("go.mod"))
	}
	if err := p.in.Install(ctx, Options{}); err != nil {
		t.Fatalf("install after go build: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.dir, "go.sum")); err == nil {
		t.Fatal("no go.sum expected")
	}
	vet := exec.Command(goBin, "vet", "./...")
	vet.Dir, vet.Env = p.dir, cmd.Env
	if out, err := vet.CombinedOutput(); err != nil {
		t.Fatalf("go vet: %v\n%s", err, out)
	}
	tidy := exec.Command(goBin, "mod", "tidy")
	tidy.Dir, tidy.Env = p.dir, cmd.Env
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy: %v\n%s", err, out)
	}
	if st, _ := gomod.Check(filepath.Join(p.dir, "go.mod")); st != gomod.Generated {
		t.Fatalf("go mod tidy changed the canonical go.mod:\n%s", p.read("go.mod"))
	}
}

func TestReviewRegressions(t *testing.T) {
	t.Run("moved tag on install", func(t *testing.T) {
		gh := universe(t)
		p := newProject(t, gh, `{"name":"app","version":"1.0.0","dependencies":{"strutil":"github:shibisty/strutil.go#^1"}}`)
		if err := p.in.Install(ctx, Options{}); err != nil {
			t.Fatal(err)
		}
		gh.MoveTag("shibisty/strutil.go", "v1.0.0", map[string]string{"gtr.json": gtrJSON("strutil", nil, nil), "strutil.go": "package strutil // evil"})
		p.in.GitHub = &source.GitHub{API: gh.URL, Client: gh.Client(), Tmp: p.home.Tmp()}
		if err := p.in.Install(ctx, Options{}); err == nil || !strings.Contains(err.Error(), "was moved") {
			t.Fatalf("got %v", err)
		}
		if err := p.in.Install(ctx, Options{Update: map[string]bool{"strutil": true}}); err != nil {
			t.Fatalf("update must accept the moved tag: %v", err)
		}
	})
	t.Run("go directive follows the packages", func(t *testing.T) {
		gh := githubtest.New(t)
		gh.Commit("o/newgo", map[string]string{"gtr.json": `{"name":"newgo","version":"0","engines":{"go":">=1.23"}}`, "n.go": "package newgo"}, "v1.0.0")
		p := newProject(t, gh, `{"name":"app","version":"1.0.0"}`)
		if err := p.in.Add(ctx, []string{"github:o/newgo"}, false); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(p.read("go.mod"), "\ngo 1.23\n") {
			t.Fatalf("go.mod:\n%s", p.read("go.mod"))
		}
	})
	t.Run("stray entries in gtr_modules are kept", func(t *testing.T) {
		gh := universe(t)
		p := newProject(t, gh, `{"name":"app","version":"1.0.0","dependencies":{"strutil":"github:shibisty/strutil.go"}}`)
		p.in.Install(ctx, Options{})
		os.WriteFile(filepath.Join(p.dir, "gtr_modules", "NOTES.txt"), []byte("mine"), 0o644)
		mine := t.TempDir()
		os.Symlink(mine, filepath.Join(p.dir, "gtr_modules", "mylink"))
		if err := p.in.Install(ctx, Options{}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(p.dir, "gtr_modules", "NOTES.txt")); err != nil {
			t.Fatal("user file removed")
		}
		if _, err := os.Lstat(filepath.Join(p.dir, "gtr_modules", "mylink")); err != nil {
			t.Fatal("user link removed")
		}
	})
	t.Run("a file: package is also a project", func(t *testing.T) {
		gh := universe(t)
		p := newProject(t, gh, `{"name":"app","version":"1.0.0"}`)
		lib := filepath.Join(t.TempDir(), "lib")
		os.MkdirAll(lib, 0o755)
		os.WriteFile(filepath.Join(lib, "gtr.json"), []byte(gtrJSON("mylib", nil, nil)), 0o644)
		if err := p.in.Add(ctx, []string{"file:" + lib}, false); err != nil {
			t.Fatal(err)
		}
		libIn := &Installer{Dir: lib, Home: p.home, GitHub: p.in.GitHub, Log: p.log}
		if err := libIn.Install(ctx, Options{}); err != nil {
			t.Fatalf("installing the library itself: %v", err)
		}
	})
	t.Run("invalid project name", func(t *testing.T) {
		p := newProject(t, universe(t), `{"name":"My App","version":"1.0.0"}`)
		if err := p.in.Install(ctx, Options{}); err == nil || !strings.Contains(err.Error(), "module path") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("monorepo tags with the package name", func(t *testing.T) {
		gh := githubtest.New(t)
		gh.Commit("o/orm.go", map[string]string{"drivers/mysql/gtr.json": gtrJSON("orm-mysql", nil, nil), "drivers/mysql/m.go": "package mysql"}, "orm-mysql@0.1.0")
		p := newProject(t, gh, `{"name":"app","version":"1.0.0"}`)
		if err := p.in.Add(ctx, []string{"github:o/orm.go/drivers/mysql#^0.1"}, false); err != nil {
			t.Fatal(err)
		}
		if p.manifest(t).Dependencies["orm-mysql"] != "github:o/orm.go/drivers/mysql#^0.1" || p.lock(t).Packages["orm-mysql"].Version != "0.1.0" {
			t.Fatalf("%v %v", p.manifest(t).Dependencies, p.lock(t).Packages)
		}
	})
	t.Run("ci detects a modified store", func(t *testing.T) {
		gh := universe(t)
		p := newProject(t, gh, `{"name":"app","version":"1.0.0","dependencies":{"strutil":"github:shibisty/strutil.go"}}`)
		p.in.Install(ctx, Options{})
		os.WriteFile(filepath.Join(p.dir, "gtr_modules", "strutil", "strutil.go"), []byte("package strutil // edited"), 0o644)
		if err := p.in.Install(ctx, Options{Frozen: true}); err == nil || !strings.Contains(err.Error(), "was modified") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("untagged dependency stays on its locked commit", func(t *testing.T) {
		gh := githubtest.New(t)
		first := gh.Commit("o/untagged", map[string]string{"gtr.json": gtrJSON("untagged", nil, nil), "u.go": "package untagged"})
		p := newProject(t, gh, `{"name":"app","version":"1.0.0","dependencies":{"untagged":"github:o/untagged"}}`)
		if err := p.in.Install(ctx, Options{}); err != nil {
			t.Fatal(err)
		}
		gh.Commit("o/untagged", map[string]string{"gtr.json": gtrJSON("untagged", nil, nil), "u.go": "package untagged // new"})
		p.in.GitHub = &source.GitHub{API: gh.URL, Client: gh.Client(), Tmp: p.home.Tmp()}
		if err := p.in.Install(ctx, Options{Frozen: true}); err != nil {
			t.Fatalf("ci after the branch moved: %v", err)
		}
		if c := p.lock(t).Packages["untagged"].Commit; c != first {
			t.Fatalf("commit changed: %s", c)
		}
		if err := p.in.Install(ctx, Options{Update: map[string]bool{"untagged": true}}); err != nil || p.lock(t).Packages["untagged"].Commit == first {
			t.Fatalf("update must move to the new head: %v", err)
		}
	})
}
