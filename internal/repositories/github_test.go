package repositories

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gtr-manager/internal/dependency"
	"gtr-manager/internal/home"
)

type fakeGitHub struct {
	*httptest.Server
	tags      map[string]bool
	status    int
	rateLimit bool
	zip       []byte
	auth      []string
	zipballs  []string
}

func zipOf(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

func newFake(t *testing.T) (*fakeGitHub, *GitHub) {
	f := &fakeGitHub{tags: map[string]bool{"v1.2.0": true, "0.3.0": true},
		zip: zipOf(t, map[string]string{"owner-repo-abc123/go.mod": "module x", "owner-repo-abc123/pkg/a.go": "package pkg"})}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		if f.rateLimit {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if f.status != 0 {
			w.WriteHeader(f.status)
			w.Write([]byte(`{"message":"boom"}`))
			return
		}
		switch p := r.URL.Path; {
		case p == "/repos/owner/repo":
			w.Write([]byte(`{"default_branch":"main"}`))
		case p == "/repos/owner/empty":
			w.Write([]byte(`{}`))
		case strings.HasPrefix(p, "/repos/owner/repo/git/ref/tags/"):
			if f.tags[strings.TrimPrefix(p, "/repos/owner/repo/git/ref/tags/")] {
				w.Write([]byte(`{"ref":"x"}`))
				return
			}
			http.NotFound(w, r)
		case strings.HasPrefix(p, "/repos/owner/repo/zipball/"):
			f.zipballs = append(f.zipballs, strings.TrimPrefix(p, "/repos/owner/repo/zipball/"))
			w.Write(f.zip)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f, &GitHub{API: f.URL, Client: f.Client(), Home: home.Layout{Root: t.TempDir()}}
}

func dep(t *testing.T, s string) *dependency.Dependency {
	d, err := dependency.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestInstallDefaultBranchAndTags(t *testing.T) {
	f, g := newFake(t)
	project := t.TempDir()
	for _, c := range [][2]string{{"github:owner/repo", "main"}, {"github:owner/repo@1.2.0", "v1.2.0"}, {"github:owner/repo@v0.3.0", "0.3.0"}} {
		spec, wantRef := c[0], c[1]
		ref, err := g.Install(context.Background(), dep(t, spec), project)
		if err != nil || ref != wantRef {
			t.Fatalf("%s: ref %q, err %v", spec, ref, err)
		}
		got, err := os.ReadFile(filepath.Join(project, "packages", "github", "owner", "repo", "pkg", "a.go"))
		if err != nil || string(got) != "package pkg" {
			t.Fatalf("%s: installed tree: %v", spec, err)
		}
	}
	if strings.Join(f.zipballs, ",") != "main,v1.2.0,0.3.0" {
		t.Fatalf("zipballs: %v", f.zipballs)
	}
	if entries, _ := os.ReadDir(filepath.Join(project, "packages", "github", "owner")); len(entries) != 1 {
		t.Fatalf("temp dirs left: %v", entries)
	}
	if f.auth[0] != "" {
		t.Fatal("no token — no Authorization header")
	}
}

func TestToken(t *testing.T) {
	f, g := newFake(t)
	g.Token = "secret"
	g.Install(context.Background(), dep(t, "github:owner/repo"), t.TempDir())
	if f.auth[0] != "Bearer secret" {
		t.Fatalf("auth header: %v", f.auth)
	}
}

func TestErrors(t *testing.T) {
	cases := map[string]struct {
		spec  string
		setup func(f *fakeGitHub, g *GitHub)
		want  string
	}{
		"not found":         {spec: "github:owner/missing", want: "set GITHUB_TOKEN"},
		"not found + token": {spec: "github:owner/missing", setup: func(_ *fakeGitHub, g *GitHub) { g.Token = "t" }, want: "not found"},
		"no tag":            {spec: "github:owner/repo@9.9.9", want: "no tag v9.9.9 or 9.9.9"},
		"range":             {spec: "github:owner/repo@^1", want: "not supported yet"},
		"empty repo":        {spec: "github:owner/empty", want: "no default branch"},
		"rate limit":        {spec: "github:owner/repo", setup: func(f *fakeGitHub, _ *GitHub) { f.rateLimit = true }, want: "rate limit"},
		"bad token":         {spec: "github:owner/repo", setup: func(f *fakeGitHub, _ *GitHub) { f.status = 401 }, want: "invalid or expired"},
		"server error":      {spec: "github:owner/repo", setup: func(f *fakeGitHub, _ *GitHub) { f.status = 502 }, want: "502"},
		"bad zip":           {spec: "github:owner/repo", setup: func(f *fakeGitHub, _ *GitHub) { f.zip = []byte("nope") }, want: "zip"},
		"zip slip":          {spec: "github:owner/repo", setup: func(f *fakeGitHub, _ *GitHub) { f.zip = zipOf(t, map[string]string{"../x": "y"}) }, want: "illegal path"},
		"offline":           {spec: "github:owner/repo", setup: func(_ *fakeGitHub, g *GitHub) { g.API = "http://127.0.0.1:1" }, want: "github:"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f, g := newFake(t)
			if tc.setup != nil {
				tc.setup(f, g)
			}
			project := t.TempDir()
			_, err := g.Install(context.Background(), dep(t, tc.spec), project)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			if _, err := os.Stat(filepath.Join(project, "packages", "github", "owner", "repo")); err == nil {
				t.Fatal("failed install must not leave the package dir")
			}
		})
	}
}

func TestFlatArchive(t *testing.T) {
	f, g := newFake(t)
	f.zip = zipOf(t, map[string]string{"a.go": "package a", "b/c.go": "package b"})
	project := t.TempDir()
	if _, err := g.Install(context.Background(), dep(t, "github:owner/repo"), project); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(project, "packages", "github", "owner", "repo", "b", "c.go")); err != nil {
		t.Fatal(err)
	}
}

func TestPackageDir(t *testing.T) {
	if got := PackageDir("p", dep(t, "orm")); got != filepath.Join("p", "packages", "orm") {
		t.Fatal(got)
	}
	if got := PackageDir("p", dep(t, "github:a/b")); got != filepath.Join("p", "packages", "github", "a", "b") {
		t.Fatal(got)
	}
}

func TestNewGitHub(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("GTR_GITHUB_API", "")
	t.Setenv("GTR_HOME", t.TempDir())
	g, err := NewGitHub()
	if err != nil || g.Token != "tok" || g.API != DefaultGitHubAPI || g.Client.Timeout == 0 {
		t.Fatalf("%+v %v", g, err)
	}
}

func TestNewGitHubAPIOverride(t *testing.T) {
	t.Setenv("GTR_GITHUB_API", "https://ghe.example.com/api/v3")
	t.Setenv("GITHUB_TOKEN", "github-com-token")
	t.Setenv("GTR_GITHUB_TOKEN", "ghe-token")
	t.Setenv("GTR_HOME", t.TempDir())
	if g, err := NewGitHub(); err != nil || g.API != "https://ghe.example.com/api/v3" || g.Token != "ghe-token" {
		t.Fatalf("%+v %v", g, err)
	}
}
