package source

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gtr-manager/internal/source/githubtest"
	"gtr-manager/internal/spec"
)

var ctx = context.Background()

func client(gh *githubtest.Server, t *testing.T) *GitHub {
	return &GitHub{API: gh.URL, Client: gh.Client(), Tmp: t.TempDir()}
}

func mustSpec(t *testing.T, v string) spec.Spec {
	s, err := spec.Parse("x", v)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTagVersion(t *testing.T) {
	for _, c := range []struct{ tag, name, sub, want string }{
		{"v1.2.3", "", "", "1.2.3"}, {"1.2.3", "", "", "1.2.3"}, {"v1.2.3-rc.1", "", "", "1.2.3-rc.1"},
		{"v1.2", "", "", ""}, {"release-1", "", "", ""}, {"orm-mysql@0.1.0", "", "", ""},
		{"orm-mysql@0.1.0", "orm-mysql", "drivers/mysql", "0.1.0"}, {"drivers/mysql/v0.2.0", "orm-mysql", "drivers/mysql", "0.2.0"},
		{"drivers/pg/v0.2.0", "orm-mysql", "drivers/mysql", ""}, {"v0.1.0", "orm-mysql", "drivers/mysql", ""},
		{"v1.0.0-01", "", "", ""}, {"v1.0.0-a..b", "", "", ""}, {"v01.0.0", "", "", ""}, {"v1.0.0+build", "", "", ""},
		{"v1.0.0-0a", "", "", "1.0.0-0a"}, {"v1.0.0-rc.0", "", "", "1.0.0-rc.0"},
	} {
		if got := TagVersion(c.tag, c.name, c.sub); got != c.want {
			t.Errorf("TagVersion(%q, %q, %q) = %q, want %q", c.tag, c.name, c.sub, got, c.want)
		}
	}
}

func TestCandidatesManifestFetch(t *testing.T) {
	gh := githubtest.New(t)
	gh.Commit("o/mono", map[string]string{"gtr.json": `{"name":"root","version":"0"}`, "drivers/mysql/gtr.json": `{"name":"orm-mysql","version":"0"}`,
		"drivers/mysql/m.go": "package mysql"}, "v1.0.0", "orm-mysql@0.1.0")
	gh.Commit("o/mono", map[string]string{"drivers/mysql/gtr.json": `{"name":"orm-mysql","version":"0"}`, "drivers/mysql/m.go": "package mysql // v2"},
		"drivers/mysql/v0.2.0")
	g := client(gh, t)
	s := mustSpec(t, "github:o/mono/drivers/mysql")
	cands, err := g.Candidates(ctx, "orm-mysql", s)
	if err != nil || len(cands) != 2 || cands[0].Version != "0.2.0" || cands[1].Tag != "orm-mysql@0.1.0" {
		t.Fatalf("%+v %v", cands, err)
	}
	m, err := g.Manifest(ctx, s, cands[1])
	if err != nil || m.Name != "orm-mysql" {
		t.Fatalf("%+v %v", m, err)
	}
	dst := filepath.Join(t.TempDir(), "pkg")
	if err := g.Fetch(ctx, s, cands[0], dst); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "m.go")); string(got) != "package mysql // v2" {
		t.Fatalf("fetched %q", got)
	}
	if _, err := os.Stat(filepath.Join(dst, "drivers")); err == nil {
		t.Fatal("only the subdirectory must be fetched")
	}
	root, _ := g.Candidates(ctx, "", mustSpec(t, "github:o/mono"))
	if len(root) != 1 || root[0].Version != "1.0.0" {
		t.Fatalf("root tags: %+v", root)
	}
	if err := g.Fetch(ctx, mustSpec(t, "github:o/mono/nope"), cands[0], filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("missing subdir must fail")
	}
}

func TestHeadAndNoManifest(t *testing.T) {
	gh := githubtest.New(t)
	sha := gh.Commit("o/untagged", map[string]string{"x.go": "package x"})
	g := client(gh, t)
	s := mustSpec(t, "github:o/untagged")
	cands, err := g.Candidates(ctx, "", s)
	if err != nil || len(cands) != 1 || !cands[0].Head || cands[0].Version != "0.0.0-g"+sha[:12] {
		t.Fatalf("%+v %v", cands, err)
	}
	if _, err := g.Manifest(ctx, s, cands[0]); !errors.Is(err, ErrNoManifest) {
		t.Fatalf("no gtr.json: %v", err)
	}
	if _, err := g.Candidates(ctx, "", mustSpec(t, "github:o/missing")); err == nil || !strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Fatalf("missing repo: %v", err)
	}
}

func TestHTTPErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		handler http.HandlerFunc
		want    string
	}{
		"rate limit": {func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(403)
		}, "rate limit"},
		"unauthorized": {func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }, "invalid or expired"},
		"server":       {func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(502) }, "502"},
		"bad json":     {func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>")) }, "bad tags response"},
	} {
		srv := httptest.NewServer(tc.handler)
		g := &GitHub{API: srv.URL, Client: srv.Client(), Token: "t", Tmp: t.TempDir()}
		_, err := g.Candidates(ctx, "", mustSpec(t, "github:o/r"))
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestNewGitHub(t *testing.T) {
	t.Setenv("GTR_GITHUB_API", "")
	t.Setenv("GITHUB_TOKEN", "gh")
	if g := NewGitHub("/tmp"); g.API != DefaultGitHubAPI || g.Token != "gh" {
		t.Fatalf("%+v", g)
	}
	t.Setenv("GTR_GITHUB_API", "https://ghe/api/v3")
	t.Setenv("GTR_GITHUB_TOKEN", "ghe")
	if g := NewGitHub("/tmp"); g.API != "https://ghe/api/v3" || g.Token != "ghe" {
		t.Fatalf("%+v", g)
	}
}

func TestFile(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "lib"), 0o755)
	os.WriteFile(filepath.Join(dir, "lib", "gtr.json"), []byte(`{"name":"lib","version":"1.2.3"}`), 0o644)
	f := &File{ProjectDir: filepath.Join(dir, "app")}
	s := mustSpec(t, "file:../lib")
	if c, err := f.Candidates(ctx, "lib", s); err != nil || c[0].Version != "1.2.3" {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := f.Candidates(ctx, "x", mustSpec(t, "file:../missing")); err == nil {
		t.Fatal("missing dir")
	}
	os.MkdirAll(filepath.Join(dir, "empty"), 0o755)
	if _, err := f.Manifest(ctx, mustSpec(t, "file:../empty"), Candidate{}); !errors.Is(err, ErrNoManifest) {
		t.Fatalf("no gtr.json: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "lib", "gtr.json"), []byte(`{"name":"lib","version":"banana"}`), 0o644)
	if _, err := f.Candidates(ctx, "lib", s); err == nil {
		t.Fatal("bad version")
	}
	if f.Fetch(ctx, s, Candidate{}, "") == nil {
		t.Fatal("Fetch must fail")
	}
	if abs := f.Dir(mustSpec(t, "file:"+dir)); abs != filepath.Clean(dir) {
		t.Fatal(abs)
	}
}
