package gomodules

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var ctx = context.Background()

// realProxy serves the local module download cache, which has exactly the
// GOPROXY layout, and a checksum database built from a real go.sum.
func realProxy(t *testing.T) (*Proxy, *SumDB, string) {
	out, err := exec.Command("go", "env", "GOMODCACHE").Output()
	if err != nil {
		t.Skip("go not available")
	}
	cache := filepath.Join(strings.TrimSpace(string(out)), "cache", "download")
	if _, err := os.Stat(filepath.Join(cache, "github.com", "redis", "go-redis", "v9", "@v", "v9.7.0.zip")); err != nil {
		t.Skip("go-redis v9.7.0 is not in the module cache")
	}
	proxy := httptest.NewServer(http.FileServer(http.Dir(cache)))
	t.Cleanup(proxy.Close)
	sums := map[string]string{}
	f, err := os.Open(filepath.Join("testdata", "redis.go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fl := strings.Fields(sc.Text()); len(fl) == 3 {
			sums[fl[0]+" "+fl[1]] = fl[2]
		}
	}
	db := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/lookup/")
		at := strings.LastIndex(key, "@")
		mod, ver := key[:at], key[at+1:]
		zip, ok1 := sums[mod+" "+ver]
		gm, ok2 := sums[mod+" "+ver+"/go.mod"]
		if !ok1 || !ok2 { // not in go.sum: hash what the cache has (e.g. the latest go.mod)
			ep, _ := Escape(mod)
			data, err := os.ReadFile(filepath.Join(cache, filepath.FromSlash(ep), "@v", ver+".mod"))
			if err != nil {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			gm, zip = HashMod(data), "h1:unknown"
		}
		fmt.Fprintf(w, "123\n%s %s %s\n%s %s/go.mod %s\n\ngo.sum database tree\n", mod, ver, zip, mod, ver, gm)
	}))
	t.Cleanup(db.Close)
	return &Proxy{URL: proxy.URL, Client: proxy.Client()}, &SumDB{URL: db.URL, Client: db.Client()}, cache
}

func TestResolveRealModules(t *testing.T) {
	p, db, _ := realProxy(t)
	r := &Resolver{Proxies: &Proxies{Public: p}, SumDB: db}
	res, err := r.Resolve(ctx, []Requirement{{Path: "github.com/redis/go-redis/v9", Range: "~9.7.0", By: "the project"}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"github.com/redis/go-redis/v9":     "v9.7.0",
		"github.com/cespare/xxhash/v2":     "v2.2.0",
		"github.com/dgryski/go-rendezvous": "v0.0.0-20200823014737-9f7001d12a5f",
		"github.com/bsm/ginkgo/v2":         "v2.12.0",
		"github.com/bsm/gomega":            "v1.27.10",
	}
	for m, v := range want {
		if res.Versions[m] != v {
			t.Errorf("%s = %s, want %s", m, res.Versions[m], v)
		}
	}
	if len(res.Versions) != len(want) || !res.Direct["github.com/redis/go-redis/v9"] || res.Direct["github.com/cespare/xxhash/v2"] {
		t.Fatalf("build list %v, direct %v", res.Versions, res.Direct)
	}
	if res.Mods["github.com/redis/go-redis/v9"].Go != "1.18" {
		t.Fatal("go line")
	}
	// The h1 of every go.mod read matches go.sum.
	if res.ModHashes["github.com/redis/go-redis/v9@v9.7.0"] != "h1:f6zhXITC7JUJIlPEiBOTXxJgPLdZcA93GewI7inzyWw=" {
		t.Fatalf("go.mod hash %s", res.ModHashes["github.com/redis/go-redis/v9@v9.7.0"])
	}

	// Fetch: the zip hash must equal go.sum's h1.
	st := &Store{Root: filepath.Join(t.TempDir(), "store"), Tmp: t.TempDir()}
	dir, h, err := st.Fetch(ctx, p, db, "github.com/redis/go-redis/v9", "v9.7.0", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if h != "h1:HhLSs+B6O021gwzl+locl0zEDnyNkxMtf/Z3NNBMa9E=" {
		t.Fatalf("zip hash %s", h)
	}
	if _, err := os.Stat(filepath.Join(dir, "redis.go")); err != nil {
		t.Fatal(err)
	}
	// Second fetch: from the store, re-verified.
	if _, h2, err := st.Fetch(ctx, p, db, "github.com/redis/go-redis/v9", "v9.7.0", "", nil); err != nil || h2 != h {
		t.Fatal(h2, err)
	}
	// A wrong lock hash is a security error.
	st2 := &Store{Root: filepath.Join(t.TempDir(), "store"), Tmp: t.TempDir()}
	if _, _, err := st2.Fetch(ctx, p, db, "github.com/cespare/xxhash/v2", "v2.2.0", "h1:wrong", nil); err == nil || !strings.Contains(err.Error(), "SECURITY") {
		t.Fatalf("lock mismatch: %v", err)
	}
}

func TestRangeTooNarrowAfterMVS(t *testing.T) {
	p, db, _ := realProxy(t)
	r := &Resolver{Proxies: &Proxies{Public: p}, SumDB: db}
	// go-redis requires xxhash v2.2.0; a gtr package pinning 2.1 conflicts.
	_, err := r.Resolve(ctx, []Requirement{
		{Path: "github.com/redis/go-redis/v9", Range: "9.7.0", By: "the project"},
		{Path: "github.com/cespare/xxhash/v2", Range: "~2.1", By: "hasher@1.0.0"},
	})
	if err == nil || !strings.Contains(err.Error(), "no version of github.com/cespare/xxhash/v2") && !strings.Contains(err.Error(), "never goes down") {
		t.Fatalf("got %v", err)
	}
}

func TestSumDBMismatch(t *testing.T) {
	p, _, _ := realProxy(t)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "1\ngithub.com/cespare/xxhash/v2 v2.2.0 h1:AAAA\ngithub.com/cespare/xxhash/v2 v2.2.0/go.mod h1:BBBB\n")
	}))
	defer bad.Close()
	r := &Resolver{Proxies: &Proxies{Public: p}, SumDB: &SumDB{URL: bad.URL, Client: bad.Client()}}
	if _, err := r.Resolve(ctx, []Requirement{{Path: "github.com/cespare/xxhash/v2", Range: "*", By: "x"}}); err == nil || !strings.Contains(err.Error(), "SECURITY") {
		t.Fatalf("got %v", err)
	}
	// A private module is not looked up.
	r = &Resolver{Proxies: &Proxies{Public: p}, SumDB: &SumDB{URL: bad.URL, Client: bad.Client(), Private: []string{"github.com/cespare"}}}
	if _, err := r.Resolve(ctx, []Requirement{{Path: "github.com/cespare/xxhash/v2", Range: "*", By: "x"}}); err != nil {
		t.Fatalf("private: %v", err)
	}
}

func TestLockedAndMissing(t *testing.T) {
	p, db, _ := realProxy(t)
	r := &Resolver{Proxies: &Proxies{Public: p}, SumDB: db, Locked: map[string]string{"github.com/redis/go-redis/v9": "v9.7.0"}}
	res, err := r.Resolve(ctx, []Requirement{{Path: "github.com/redis/go-redis/v9", Range: "^9", By: "the project"}})
	if err != nil || res.Versions["github.com/redis/go-redis/v9"] != "v9.7.0" {
		t.Fatalf("locked: %v %v", res, err)
	}
	if _, err := (&Resolver{Proxies: &Proxies{Public: p}}).Resolve(ctx, []Requirement{{Path: "example.com/nope", Range: "*", By: "x"}}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing: %v", err)
	}
	if _, err := (&Resolver{Proxies: &Proxies{Public: p}}).Resolve(ctx, []Requirement{{Path: "github.com/cespare/xxhash/v2", Range: "^3", By: "x"}}); err == nil || !strings.Contains(err.Error(), "available: v2.2.0") {
		t.Fatalf("no match: %v", err)
	}
}
