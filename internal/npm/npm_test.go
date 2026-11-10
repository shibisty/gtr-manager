package npm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

var ctx = context.Background()

// fake is an npm registry in memory.
type fake struct {
	srv        *httptest.Server
	packuments map[string]*Packument
	tarballs   map[string][]byte
	metaHits   atomic.Int32
	tarHits    atomic.Int32
}

func tgz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.WriteHeader(&tar.Header{Name: "package/link", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink})
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func sri(data []byte) string {
	h := sha512.Sum512(data)
	return "sha512-" + base64.StdEncoding.EncodeToString(h[:])
}

func newFake(t *testing.T) *fake {
	f := &fake{packuments: map[string]*Packument{}, tarballs: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/-/") {
			f.tarHits.Add(1)
			data, ok := f.tarballs[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Write(data)
			return
		}
		f.metaHits.Add(1)
		name := strings.TrimPrefix(r.URL.Path, "/")
		if r.Header.Get("Authorization") != "" && r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		p, ok := f.packuments[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(p)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// add publishes name@version with dependencies.
func (f *fake) add(t *testing.T, name, version string, deps map[string]string, mutate ...func(*Release)) {
	t.Helper()
	data := tgz(t, map[string]string{
		"package/package.json": `{"name":"` + name + `","version":"` + version + `"}`,
		"package/index.js":     "export const v = '" + version + "';",
	})
	urlPath := "/-/" + strings.ReplaceAll(name, "/", "_") + "-" + version + ".tgz"
	f.tarballs[urlPath] = data
	p := f.packuments[name]
	if p == nil {
		p = &Packument{Name: name, DistTags: map[string]string{}, Versions: map[string]Release{}}
		f.packuments[name] = p
	}
	rel := Release{Name: name, Version: version, Dependencies: deps}
	rel.Dist.Tarball = f.srv.URL + urlPath
	rel.Dist.Integrity = sri(data)
	for _, m := range mutate {
		m(&rel)
	}
	p.Versions[version] = rel
	if !strings.Contains(version, "-") {
		p.DistTags["latest"] = version
	}
}

func TestResolveHoisting(t *testing.T) {
	f := newFake(t)
	f.add(t, "a", "1.0.0", map[string]string{"c": "^1"})
	f.add(t, "a", "1.2.0", map[string]string{"c": "^1"})
	f.add(t, "b", "2.0.0", map[string]string{"c": "^2", "a": "^1"})
	f.add(t, "c", "1.5.0", nil)
	f.add(t, "c", "2.1.0", nil)
	f.add(t, "c", "3.0.0-rc.1", nil)
	r := &Registry{URL: f.srv.URL}
	tree, err := r.Resolve(ctx, Request{Deps: map[string]string{"a": "^1", "b": "^2"}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"node_modules/a":                "1.2.0",
		"node_modules/b":                "2.0.0",
		"node_modules/c":                "1.5.0",
		"node_modules/b/node_modules/c": "2.1.0",
	}
	if len(tree) != len(want) {
		t.Fatalf("%v", keys(tree))
	}
	for p, v := range want {
		if tree[p] == nil || tree[p].Version != v {
			t.Errorf("%s: %+v", p, tree[p])
		}
	}
	// The "latest" tag wins when it matches; else the highest match.
	f.packuments["c"].DistTags["latest"] = "1.5.0"
	f.add(t, "c", "2.9.0", nil)
	f.packuments["c"].DistTags["latest"] = "1.5.0"
	r2 := &Registry{URL: f.srv.URL}
	tree, _ = r2.Resolve(ctx, Request{Deps: map[string]string{"c": "*"}})
	if tree["node_modules/c"].Version != "1.5.0" {
		t.Fatal(tree["node_modules/c"])
	}
	tree, _ = r2.Resolve(ctx, Request{Deps: map[string]string{"c": "^2"}})
	if tree["node_modules/c"].Version != "2.9.0" {
		t.Fatal(tree["node_modules/c"])
	}
}

func keys[V any](m map[string]V) []string { return sortedKeys(m) }

func TestResolveLockAndErrors(t *testing.T) {
	f := newFake(t)
	f.add(t, "a", "1.0.0", nil)
	f.add(t, "a", "1.1.0", nil)
	r := &Registry{URL: f.srv.URL}
	locked := map[string]*Package{"node_modules/a": {Name: "a", Version: "1.0.0", Tarball: "x", Integrity: "sha512-x"}}
	tree, err := r.Resolve(ctx, Request{Deps: map[string]string{"a": "^1"}, Locked: locked})
	if err != nil || tree["node_modules/a"].Version != "1.0.0" || f.metaHits.Load() != 0 {
		t.Fatalf("lock not reused: %v %v %d", tree, err, f.metaHits.Load())
	}
	tree, _ = r.Resolve(ctx, Request{Deps: map[string]string{"a": "^1"}, Locked: locked, Update: map[string]bool{"a": true}})
	if tree["node_modules/a"].Version != "1.1.0" {
		t.Fatal("update ignored")
	}
	tree, _ = r.Resolve(ctx, Request{Deps: map[string]string{"a": "~1.1"}, Locked: locked})
	if tree["node_modules/a"].Version != "1.1.0" {
		t.Fatal("a lock outside the range is kept")
	}
	// Frozen: exactly the lock, or an error.
	if tree, err := r.Resolve(ctx, Request{Deps: map[string]string{"a": "^1"}, Locked: locked, Frozen: true}); err != nil || tree["node_modules/a"].Version != "1.0.0" {
		t.Fatal(err)
	}
	if _, err := r.Resolve(ctx, Request{Deps: map[string]string{"a": "^2"}, Locked: locked, Frozen: true}); err == nil || !strings.Contains(err.Error(), "gtr.lock does not match") {
		t.Fatal(err)
	}
	if _, err := r.Resolve(ctx, Request{Deps: map[string]string{"a": "^x"}, Frozen: true}); err == nil {
		t.Fatal("bad range frozen")
	}
	for deps, want := range map[string]string{
		"nope": "not found", "a@^9": "no version of a", "a@>=one": "invalid range", "BAD@1": "invalid npm package name",
	} {
		name, rng, _ := strings.Cut(deps, "@")
		if rng == "" {
			rng = "*"
		}
		if _, err := r.Resolve(ctx, Request{Deps: map[string]string{name: rng}}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", deps, err)
		}
	}
	// Optional dependencies that fail, and platform-specific ones, are skipped.
	f.add(t, "native-linux", "1.0.0", nil, func(r *Release) { r.OS = []string{"!" + osName(), "plan9"} })
	f.add(t, "opt", "1.0.0", nil, func(r *Release) {
		r.OptionalDependencies = map[string]string{"missing": "^1", "native-linux": "^1"}
	})
	tree, err = (&Registry{URL: f.srv.URL}).Resolve(ctx, Request{Deps: map[string]string{"opt": "^1"}})
	if err != nil || len(tree) != 1 {
		t.Fatal(err, keys(tree))
	}
	if _, err := (&Registry{URL: f.srv.URL}).Resolve(ctx, Request{Deps: map[string]string{"native-linux": "^1"}}); err == nil || !strings.Contains(err.Error(), "not for this platform") {
		t.Fatal(err)
	}
}

func osName() string {
	n := map[string]string{"windows": "win32"}[goosForTest]
	if n == "" {
		n = goosForTest
	}
	return n
}

func TestInstall(t *testing.T) {
	f := newFake(t)
	f.add(t, "a", "1.0.0", map[string]string{"c": "^1"})
	f.add(t, "@scope/b", "2.0.0", map[string]string{"c": "^2"})
	f.add(t, "c", "1.0.0", nil)
	f.add(t, "c", "2.0.0", nil)
	r := &Registry{URL: f.srv.URL, Token: "tok"}
	dir, store := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(dir, "node_modules/foreign"), 0o755)
	tree, err := r.Resolve(ctx, Request{Deps: map[string]string{"a": "^1", "@scope/b": "^2"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Install(ctx, dir, store, tree, false); err != nil {
		t.Fatal(err)
	}
	// Dependencies are placed in name order: @scope/b's c ^2 is hoisted first.
	for p, v := range map[string]string{"node_modules/a": "1.0.0", "node_modules/@scope/b": "2.0.0", "node_modules/c": "2.0.0", "node_modules/a/node_modules/c": "1.0.0"} {
		data, err := os.ReadFile(filepath.Join(dir, p, "index.js"))
		if err != nil || !strings.Contains(string(data), v) {
			t.Errorf("%s: %s %v", p, data, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "node_modules/a/link")); !os.IsNotExist(err) {
		t.Fatal("symlink extracted")
	}
	hits := f.tarHits.Load()
	// Again: nothing is downloaded or rewritten.
	stamp := filepath.Join(dir, "node_modules/a/marker")
	os.WriteFile(stamp, nil, 0o644)
	if err := r.Install(ctx, dir, store, tree, false); err != nil || f.tarHits.Load() != hits {
		t.Fatal(err, f.tarHits.Load())
	}
	if _, err := os.Stat(stamp); err != nil {
		t.Fatal("unchanged package rewritten")
	}
	// Dropping @scope/b removes it; foreign dirs stay.
	delete(tree, "node_modules/@scope/b")
	if err := r.Install(ctx, dir, store, tree, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "node_modules/@scope/b")); !os.IsNotExist(err) {
		t.Fatal("removed package kept")
	}
	if _, err := os.Stat(filepath.Join(dir, "node_modules/foreign")); err != nil {
		t.Fatal("foreign directory removed")
	}
	// A new store and offline: not available.
	if err := r.Install(ctx, t.TempDir(), t.TempDir(), tree, true); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatal(err)
	}
	// From the store, offline works.
	if err := r.Install(ctx, t.TempDir(), store, tree, true); err != nil {
		t.Fatal(err)
	}
	// A wrong integrity is refused.
	bad := map[string]*Package{"node_modules/c": {Path: "node_modules/c", Name: "c", Version: "9.9.9", Tarball: tree["node_modules/c"].Tarball, Integrity: "sha512-AAAA"}}
	if err := r.Install(ctx, t.TempDir(), store, bad, false); err == nil || !strings.Contains(err.Error(), "integrity mismatch") {
		t.Fatal(err)
	}
	none := map[string]*Package{"node_modules/c": {Path: "node_modules/c", Name: "c", Version: "9.9.8", Integrity: "sha512-AAAA"}}
	if err := r.Install(ctx, t.TempDir(), store, none, false); err == nil {
		t.Fatal("no tarball URL")
	}
	if err := r.Install(ctx, t.TempDir(), store, nil, false); err != nil {
		t.Fatal(err)
	}
}

func TestExtractAndIntegrity(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "package/../../evil", Size: 1, Mode: 0o644, Typeflag: tar.TypeReg})
	tw.Write([]byte("x"))
	tw.Close()
	gz.Close()
	if err := extract(&buf, t.TempDir()); err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatal(err)
	}
	if err := extract(strings.NewReader("not gzip"), t.TempDir()); err == nil {
		t.Fatal("not gzip")
	}
	if _, _, _, err := integrity("", ""); err == nil {
		t.Fatal("no hash")
	}
	if a, _, _, err := integrity("sha1-AAAA md5-x", ""); a != "sha1" || err != nil {
		t.Fatal(a, err)
	}
	if a, _, _, err := integrity("", "0a0b"); a != "sha1" || err != nil {
		t.Fatal(a, err)
	}
	if hexToB64("0a0b") != "Cgs=" || short("sha512-AAAAAAAA") != "00000000" || short("bad") != "00000000" {
		t.Fatal(hexToB64("0a0b"), short("sha512-AAAAAAAA"))
	}
	if !platformOK(nil, "linux") || !platformOK([]string{"linux"}, "linux") || platformOK([]string{"darwin"}, "linux") || platformOK([]string{"!win32"}, "windows") || !platformOK([]string{"x64"}, "amd64") {
		t.Fatal("platformOK")
	}
	if err := ValidName("@preact/signals"); err != nil || ValidName("Bad") == nil || ValidName("@x") == nil {
		t.Fatal("ValidName")
	}
	if strings.Join(lookupDirs("node_modules/a/node_modules/b"), "|") != "node_modules/a/node_modules/b|node_modules/a|" {
		t.Fatal(lookupDirs("node_modules/a/node_modules/b"))
	}
	if top("node_modules/@s/x/node_modules/y") != "node_modules/@s/x" || top("node_modules/a") != "node_modules/a" {
		t.Fatal("top")
	}
}

func TestRegistryErrors(t *testing.T) {
	f := newFake(t)
	r := &Registry{URL: f.srv.URL + "/", Token: "wrong"}
	f.add(t, "a", "1.0.0", nil)
	if _, err := r.Packument(ctx, "a"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatal(err)
	}
	r = &Registry{URL: "http://127.0.0.1:1"}
	if _, err := r.Packument(ctx, "a"); err == nil {
		t.Fatal("unreachable")
	}
	if (&Registry{}).base() != DefaultRegistry || (&Registry{}).client() == nil {
		t.Fatal("defaults")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("{broken")) }))
	defer srv.Close()
	if _, err := (&Registry{URL: srv.URL}).Packument(ctx, "a"); err == nil {
		t.Fatal("broken JSON")
	}
	// A tarball without package.json.
	data := tgz(t, map[string]string{"package/index.js": "x"})
	f.tarballs["/-/empty.tgz"] = data
	empty := map[string]*Package{"node_modules/e": {Path: "node_modules/e", Name: "e", Version: "1.0.0", Tarball: f.srv.URL + "/-/empty.tgz", Integrity: sri(data)}}
	if err := (&Registry{URL: f.srv.URL}).Install(ctx, t.TempDir(), t.TempDir(), empty, false); err == nil || !strings.Contains(err.Error(), "no package.json") {
		t.Fatal(err)
	}
	missing := map[string]*Package{"node_modules/m": {Path: "node_modules/m", Name: "m", Version: "1.0.0", Tarball: f.srv.URL + "/-/missing.tgz", Integrity: sri(data)}}
	if err := (&Registry{URL: f.srv.URL}).Install(ctx, t.TempDir(), t.TempDir(), missing, false); err == nil {
		t.Fatal("missing tarball")
	}
}
