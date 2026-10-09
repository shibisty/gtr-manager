package install

import (
	"bufio"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gtr-manager/internal/gomod"
	"gtr-manager/internal/gomodules"
)

// realModules serves the local module download cache as a GOPROXY and a
// checksum database built from a real go.sum.
func realModules(t *testing.T, p *project) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not in PATH")
	}
	out, _ := exec.Command(goBin, "env", "GOMODCACHE").Output()
	cache := filepath.Join(strings.TrimSpace(string(out)), "cache", "download")
	if _, err := os.Stat(filepath.Join(cache, "github.com", "redis", "go-redis", "v9", "@v", "v9.7.0.zip")); err != nil {
		t.Skip("go-redis v9.7.0 is not in the module cache")
	}
	proxy := httptest.NewServer(http.FileServer(http.Dir(cache)))
	t.Cleanup(proxy.Close)
	sums := map[string]string{}
	f, _ := os.Open(filepath.Join("testdata", "redis.go.sum"))
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fl := strings.Fields(sc.Text()); len(fl) == 3 {
			sums[fl[0]+" "+fl[1]] = fl[2]
		}
	}
	f.Close()
	db := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/lookup/")
		at := strings.LastIndex(key, "@")
		mod, ver := key[:at], key[at+1:]
		zip, gm := sums[mod+" "+ver], sums[mod+" "+ver+"/go.mod"]
		if gm == "" { // not in go.sum: hash what the cache has (e.g. the latest go.mod)
			ep, _ := gomodules.Escape(mod)
			data, err := os.ReadFile(filepath.Join(cache, filepath.FromSlash(ep), "@v", ver+".mod"))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			gm, zip = gomodules.HashMod(data), "h1:unknown"
		}
		fmt.Fprintf(w, "1\n%s %s %s\n%s %s/go.mod %s\n", mod, ver, zip, mod, ver, gm)
	}))
	t.Cleanup(db.Close)
	p.in.GoProxy = &gomodules.Proxies{Public: &gomodules.Proxy{URL: proxy.URL, Client: proxy.Client(), Cache: filepath.Join(p.home.Root, "cache", "download", "go")}}
	p.in.SumDB = &gomodules.SumDB{URL: db.URL, Client: db.Client()}
}

func TestExternalModulesEndToEnd(t *testing.T) {
	gh := universe(t)
	// A gtr package that needs an external module itself.
	gh.Commit("shibisty/hasher.go", map[string]string{
		"gtr.json":  gtrJSON("hasher", map[string]string{"github.com/cespare/xxhash/v2": "^2.1"}, nil),
		"hasher.go": "package hasher\n\nimport \"github.com/cespare/xxhash/v2\"\n\nfunc Sum(s string) uint64 { return xxhash.Sum64String(s) }\n",
	}, "v1.0.0")
	p := newProject(t, gh, `{"name":"app","version":"1.0.0","engines":{"go":">=1.22"}}`)
	realModules(t, p)

	if err := p.in.Add(ctx, []string{"github.com/redis/go-redis/v9@~9.7.0", "github:shibisty/hasher.go"}, false); err != nil {
		t.Fatal(err)
	}
	m := p.manifest(t)
	if m.Dependencies["github.com/redis/go-redis/v9"] != "~9.7.0" || m.Dependencies["hasher"] != "github:shibisty/hasher.go#^1.0.0" {
		t.Fatalf("gtr.json: %v", m.Dependencies)
	}
	l := p.lock(t)
	if e := l.Packages["github.com/redis/go-redis/v9"]; e.Version != "v9.7.0" || e.Source != "go" || e.Integrity != "h1:HhLSs+B6O021gwzl+locl0zEDnyNkxMtf/Z3NNBMa9E=" ||
		e.GoMod != "h1:f6zhXITC7JUJIlPEiBOTXxJgPLdZcA93GewI7inzyWw=" {
		t.Fatalf("lock: %+v", e)
	}
	if l.Packages["github.com/cespare/xxhash/v2"].Version != "v2.2.0" {
		t.Fatalf("xxhash: %+v", l.Packages["github.com/cespare/xxhash/v2"])
	}
	goMod := p.read("go.mod")
	for _, want := range []string{
		"require (\n\tgithub.com/redis/go-redis/v9 v9.7.0\n\thasher v1.0.0\n)",
		"\tgithub.com/cespare/xxhash/v2 v2.2.0 // indirect\n",
		"replace github.com/redis/go-redis/v9 => ./gtr_modules/.go/github.com/redis/go-redis/v9@v9.7.0\n",
	} {
		if !strings.Contains(goMod, want) {
			t.Errorf("go.mod lacks %q:\n%s", want, goMod)
		}
	}
	// The package go.mod requires the floor of its range.
	if pm := read(t, filepath.Join(p.dir, "gtr_modules", "hasher", "go.mod")); !strings.Contains(pm, "github.com/cespare/xxhash/v2 v2.1.0") {
		t.Fatalf("package go.mod:\n%s", pm)
	}

	os.WriteFile(filepath.Join(p.dir, "main.go"), []byte(`package main

import (
	"fmt"

	"github.com/redis/go-redis/v9"
	"hasher"
)

func main() {
	c := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	defer c.Close()
	fmt.Println(c.Options().Addr, hasher.Sum("gtr") != 0)
}
`), 0o644)
	env := append(os.Environ(), "GOPROXY=off", "GOFLAGS=-mod=mod", "GOWORK=off", "GOTOOLCHAIN=local")
	for _, args := range [][]string{{"run", "."}, {"vet", "./..."}, {"build", "./..."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir, cmd.Env = p.dir, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %v: %v\n%s", args, err, out)
		}
		if args[0] == "run" && strings.TrimSpace(string(out)) != "127.0.0.1:1 true" {
			t.Fatalf("output %q", out)
		}
	}
	if st, _ := gomod.Check(filepath.Join(p.dir, "go.mod")); st != gomod.Generated {
		t.Fatalf("go changed go.mod:\n%s", p.read("go.mod"))
	}

	// Reinstall works without the proxy: everything is locked and stored.
	p.in.GoProxy.Public.URL = "http://127.0.0.1:1"
	if err := p.in.Install(ctx, Options{}); err != nil {
		t.Fatalf("offline reinstall: %v", err)
	}
	if err := p.in.Install(ctx, Options{Frozen: true}); err != nil {
		t.Fatalf("ci: %v", err)
	}
	if err := p.in.Remove(ctx, []string{"github.com/redis/go-redis/v9"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(p.dir, "gtr_modules", ".go", "github.com", "redis")); !os.IsNotExist(err) {
		t.Fatal("stale module links must be removed")
	}
	if strings.Contains(p.read("go.mod"), "go-redis") || p.lock(t).Packages["github.com/cespare/xxhash/v2"].Version != "v2.2.0" {
		t.Fatalf("after remove:\n%s", p.read("go.mod"))
	}
}

func TestModuleRange(t *testing.T) {
	for v, want := range map[string]string{"v9.7.0": "^9.7.0", "v0.3.1": "^0.3", "v0.0.4": "^0.0.4", "v0.0.0-20200823014737-9f7001d12a5f": "*", "bad": "*"} {
		if got := ModuleRange(v); got != want {
			t.Errorf("ModuleRange(%s) = %s, want %s", v, got, want)
		}
	}
}
