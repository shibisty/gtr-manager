package gomodules

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// synthetic is an in-memory proxy + checksum database for made-up modules.
type synthetic struct {
	mods    map[string]map[string]string            // path → version → go.mod
	files   map[string]map[string]map[string]string // path → version → zip files (no go.mod unless listed)
	tamper  map[string]string                       // "path@version" → replacement go.mod served by the proxy
	proxy   *httptest.Server
	db      *httptest.Server
	modHits int
}

func newSynthetic(t *testing.T) *synthetic {
	s := &synthetic{mods: map[string]map[string]string{}, files: map[string]map[string]map[string]string{}, tamper: map[string]string{}}
	s.proxy = httptest.NewServer(http.HandlerFunc(s.serveProxy))
	s.db = httptest.NewServer(http.HandlerFunc(s.serveDB))
	t.Cleanup(func() { s.proxy.Close(); s.db.Close() })
	return s
}

func (s *synthetic) add(path, version, gomod string, files map[string]string) {
	if s.mods[path] == nil {
		s.mods[path] = map[string]string{}
		s.files[path] = map[string]map[string]string{}
	}
	s.mods[path][version] = gomod
	if files == nil {
		files = map[string]string{"x.go": "package x"}
	}
	s.files[path][version] = files
}

func (s *synthetic) find(escaped string) string {
	for p := range s.mods {
		if e, _ := Escape(p); e == escaped {
			return p
		}
	}
	return ""
}

func (s *synthetic) zip(path, version string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range s.files[path][version] {
		w, _ := zw.Create(path + "@" + version + "/" + name)
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

func (s *synthetic) serveProxy(w http.ResponseWriter, r *http.Request) {
	u := strings.TrimPrefix(r.URL.Path, "/")
	i := strings.Index(u, "/@")
	if i < 0 {
		http.NotFound(w, r)
		return
	}
	path, rest := s.find(u[:i]), u[i+1:]
	if path == "" {
		http.NotFound(w, r)
		return
	}
	switch {
	case rest == "@v/list":
		var vs []string
		for v := range s.mods[path] {
			if !IsPseudo(v) {
				vs = append(vs, v)
			}
		}
		sort.Strings(vs)
		fmt.Fprint(w, strings.Join(vs, "\n"))
	case rest == "@latest":
		for v := range s.mods[path] {
			json.NewEncoder(w).Encode(map[string]string{"Version": v})
			return
		}
	case strings.HasSuffix(rest, ".mod"):
		v := strings.TrimSuffix(strings.TrimPrefix(rest, "@v/"), ".mod")
		s.modHits++
		if t, ok := s.tamper[path+"@"+v]; ok {
			fmt.Fprint(w, t)
			return
		}
		fmt.Fprint(w, s.mods[path][v])
	case strings.HasSuffix(rest, ".zip"):
		v := strings.TrimSuffix(strings.TrimPrefix(rest, "@v/"), ".zip")
		w.Write(s.zip(path, v))
	default:
		http.NotFound(w, r)
	}
}

// serveDB answers lookups with honest hashes computed from the content.
func (s *synthetic) serveDB(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/lookup/")
	at := strings.LastIndex(key, "@")
	path, v := s.find(key[:at]), key[at+1:]
	if path == "" {
		http.NotFound(w, r)
		return
	}
	dir, _ := os.MkdirTemp("", "synth")
	defer os.RemoveAll(dir)
	for name, body := range s.files[path][v] {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
	}
	h, _ := Hash(dir, path, v)
	fmt.Fprintf(w, "1\n%s %s %s\n%s %s/go.mod %s\n", path, v, h, path, v, HashMod([]byte(s.mods[path][v])))
}

func (s *synthetic) resolver() *Resolver {
	return &Resolver{Proxies: &Proxies{Public: &Proxy{URL: s.proxy.URL, Client: s.proxy.Client()}}, SumDB: &SumDB{URL: s.db.URL, Client: s.db.Client()}}
}

func mod(path, goLine string, reqs ...string) string {
	out := "module " + path + "\n\ngo " + goLine + "\n"
	for i := 0; i+1 < len(reqs); i += 2 {
		out += "require " + reqs[i] + " " + reqs[i+1] + "\n"
	}
	return out
}

// Every selected module is a root in the generated go.mod, so requirements
// reached only through pruned modules must be applied too.
func TestMVSMatchesGo(t *testing.T) {
	s := newSynthetic(t)
	s.add("ex.com/a", "v1.0.0", mod("ex.com/a", "1.21", "ex.com/b", "v1.0.0", "ex.com/c", "v1.0.0", "ex.com/t", "v1.0.0"), nil)
	s.add("ex.com/b", "v1.0.0", mod("ex.com/b", "1.21", "ex.com/c", "v1.1.0"), nil)
	s.add("ex.com/c", "v1.0.0", mod("ex.com/c", "1.21"), nil)
	s.add("ex.com/c", "v1.1.0", mod("ex.com/c", "1.21"), nil)
	s.add("ex.com/t", "v1.0.0", mod("ex.com/t", "1.13", "ex.com/o", "v0.5.0"), nil) // unpruned
	s.add("ex.com/o", "v0.5.0", mod("ex.com/o", "1.20"), nil)
	res, err := s.resolver().Resolve(ctx, []Requirement{{Path: "ex.com/a", Range: "*", By: "the project"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Versions["ex.com/c"] != "v1.1.0" || res.Versions["ex.com/o"] != "v0.5.0" || len(res.Versions) != 5 {
		t.Fatalf("%v", res.Versions)
	}
}

func TestPickRootLikeGoGet(t *testing.T) {
	s := newSynthetic(t)
	s.add("ex.com/r", "v1.0.0", mod("ex.com/r", "1.21"), nil)
	s.add("ex.com/r", "v1.1.0", mod("ex.com/r", "1.21"), nil)
	s.add("ex.com/r", "v1.2.0", "module ex.com/r\n\ngo 1.21\n\nretract v1.1.0\nretract [v1.2.0, v1.2.0] // oops\n", nil)
	s.add("ex.com/r", "v2.0.0+incompatible", "module ex.com/r\n", nil)
	s.add("ex.com/r", "v1.3.0-rc.9", mod("ex.com/r", "1.21"), nil)
	s.add("ex.com/r", "v1.3.0-rc.10", mod("ex.com/r", "1.21"), nil)
	s.add("ex.com/r", "v1.3.0-rc9", mod("ex.com/r", "1.21"), nil)
	s.add("ex.com/r", "v1.3.0-rc10", mod("ex.com/r", "1.21"), nil)
	pick := func(rng string) string {
		res, err := s.resolver().Resolve(ctx, []Requirement{{Path: "ex.com/r", Range: rng, By: "x"}})
		if err != nil {
			t.Fatalf("%s: %v", rng, err)
		}
		return res.Versions["ex.com/r"]
	}
	// v1.2.0 and v1.1.0 are retracted; +incompatible is not preferred.
	if v := pick("*"); v != "v1.0.0" {
		t.Fatalf("* → %s", v)
	}
	if v := pick("^2"); v != "v2.0.0+incompatible" {
		t.Fatalf("^2 → %s", v)
	}
	// Strict SemVer: rc.10 > rc.9 (numeric), but rc9 > rc10 (lexical).
	if Compare("v1.3.0-rc.10", "v1.3.0-rc.9") <= 0 || Compare("v1.3.0-rc9", "v1.3.0-rc10") <= 0 || Compare("v1.0.0-1", "v1.0.0-a") >= 0 {
		t.Fatal("pre-release order")
	}
}

func TestTamperedCacheAndProxy(t *testing.T) {
	s := newSynthetic(t)
	s.add("ex.com/m", "v1.0.0", mod("ex.com/m", "1.21"), nil)
	cache := t.TempDir()
	r := s.resolver()
	r.Proxies.Public.Cache = cache
	if _, err := r.Resolve(ctx, []Requirement{{Path: "ex.com/m", Range: "*", By: "x"}}); err != nil {
		t.Fatal(err)
	}
	// Edit the cached go.mod: the next resolution must reject it.
	cached := filepath.Join(cache, "ex.com", "m@v1.0.0.mod")
	os.WriteFile(cached, []byte(mod("ex.com/m", "1.21", "ex.com/evil", "v1.0.0")), 0o644)
	if _, err := s.resolver().Resolve(ctx, nil); err != nil {
		t.Fatal(err)
	}
	r2 := s.resolver()
	r2.Proxies.Public.Cache = cache
	if _, err := r2.Resolve(ctx, []Requirement{{Path: "ex.com/m", Range: "*", By: "x"}}); err == nil || !strings.Contains(err.Error(), "SECURITY") {
		t.Fatalf("tampered cache: %v", err)
	}
	// A lock pin catches a tampered proxy even for a private module.
	s.tamper["ex.com/m@v1.0.0"] = mod("ex.com/m", "1.21", "ex.com/evil", "v1.0.0")
	r3 := s.resolver()
	r3.SumDB.Private = []string{"ex.com"}
	r3.LockedMods = map[string]string{"ex.com/m@v1.0.0": HashMod([]byte(s.mods["ex.com/m"]["v1.0.0"]))}
	if _, err := r3.Resolve(ctx, []Requirement{{Path: "ex.com/m", Range: "*", By: "x"}}); err == nil || !strings.Contains(err.Error(), "gtr.lock") {
		t.Fatalf("lock pin: %v", err)
	}
}

func TestLegacyModuleWithoutGoMod(t *testing.T) {
	s := newSynthetic(t)
	s.add("ex.com/old", "v1.0.0", "module ex.com/old\n", map[string]string{"old.go": "package old"})
	st := &Store{Root: filepath.Join(t.TempDir(), "store"), Tmp: t.TempDir()}
	r := s.resolver()
	p := r.Proxies.Public
	dir, h, err := st.Fetch(ctx, p, r.SumDB, "ex.com/old", "v1.0.0", "", []byte("module ex.com/old\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		t.Fatal("go.mod must be written")
	}
	if again, _ := Hash(dir, "ex.com/old", "v1.0.0"); again != h {
		t.Fatal("the added go.mod must not change the hash")
	}
	// A zip that smuggles a go.mod into a module without one changes the
	// hash and is rejected by the checksum database.
	s.files["ex.com/old"]["v1.0.0"]["go.mod"] = "module ex.com/old\n\ngo 1.99\n"
	db := &SumDB{URL: s.db.URL, Client: s.db.Client()}
	honest := map[string]string{"old.go": "package old"}
	tmpDir, _ := os.MkdirTemp("", "h")
	defer os.RemoveAll(tmpDir)
	os.WriteFile(filepath.Join(tmpDir, "old.go"), []byte(honest["old.go"]), 0o644)
	honestHash, _ := Hash(tmpDir, "ex.com/old", "v1.0.0")
	db.cache = map[string][2]string{"ex.com/old@v1.0.0": {honestHash, HashMod([]byte("module ex.com/old\n"))}}
	st2 := &Store{Root: filepath.Join(t.TempDir(), "store"), Tmp: t.TempDir()}
	if _, _, err := st2.Fetch(ctx, p, db, "ex.com/old", "v1.0.0", "", []byte("module ex.com/old\n")); err == nil || !strings.Contains(err.Error(), "SECURITY") {
		t.Fatalf("smuggled go.mod: %v", err)
	}
}

func TestPathsAndRouting(t *testing.T) {
	for _, bad := range []string{`corp.example/..\..\x`, "corp.example/../x", "nodot/x", "Upper.com/x", "ex.com/a@b", "ex.com//x", "ex.com/x?y", ".ex.com/x"} {
		if CheckPath(bad) == nil {
			t.Errorf("CheckPath(%q) must fail", bad)
		}
	}
	if CheckPath("github.com/BurntSushi/toml") != nil || CheckPath("gopkg.in/yaml.v3") != nil {
		t.Fatal("valid paths")
	}
	if _, err := DirName(`corp.example/..\..\x`, "v1.0.0"); err == nil {
		t.Fatal("DirName traversal")
	}
	ps := &Proxies{Public: &Proxy{URL: "pub"}, PrivatePatterns: []string{"git.corp.example"}}
	if p, err := ps.For("github.com/a/b"); err != nil || p.URL != "pub" {
		t.Fatal("public routing")
	}
	if _, err := ps.For("git.corp.example/team/x"); err == nil || !strings.Contains(err.Error(), "GTR_GOPROXY_PRIVATE") {
		t.Fatalf("private without proxy: %v", err)
	}
	ps.Private = &Proxy{URL: "priv"}
	if p, _ := ps.For("git.corp.example/team/x"); p.URL != "priv" {
		t.Fatal("private routing")
	}
}

func TestEnvironment(t *testing.T) {
	t.Setenv("GTR_GOPROXY", "")
	t.Setenv("GOPROXY", "off")
	if _, err := ProxyURL(); err == nil {
		t.Fatal("GOPROXY=off")
	}
	t.Setenv("GOPROXY", "https://goproxy.io,direct")
	if u, _ := ProxyURL(); u != "https://goproxy.io" {
		t.Fatal(u)
	}
	t.Setenv("GOPROXY", "")
	if u, _ := ProxyURL(); u != DefaultProxy {
		t.Fatal(u)
	}
	t.Setenv("GTR_GOSUMDB", "")
	for env, want := range map[string]string{"": DefaultSumDB, "sum.golang.google.cn": "https://sum.golang.google.cn",
		"sum.golang.org+033de0ae+Ac4z https://mirror.example/sumdb": "https://mirror.example/sumdb"} {
		t.Setenv("GOSUMDB", env)
		if db := NewSumDB(); db == nil || db.URL != want {
			t.Errorf("GOSUMDB=%q → %+v", env, db)
		}
	}
	t.Setenv("GOSUMDB", "off")
	if NewSumDB() != nil {
		t.Fatal("GOSUMDB=off")
	}
}
