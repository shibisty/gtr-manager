package source

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gtr-manager/internal/spec"
)

// fakeGitLab serves one project, acme/libs/orm, with a monorepo layout.
func fakeGitLab(t *testing.T, tags []map[string]any) (*httptest.Server, *[]string) {
	var seen []string
	files := map[string]string{
		"gtr.json":               `{"name":"orm","version":"0.0.0"}`,
		"orm.go":                 "package orm",
		"drivers/pg/gtr.json":    `{"name":"orm-pg","version":"0.0.0"}`,
		"drivers/pg/pg.go":       "package pg",
		"drivers/none/readme.md": "no gtr.json here",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("PRIVATE-TOKEN")+" "+r.URL.EscapedPath()+"?"+r.URL.RawQuery)
		const base = "/api/v4/projects/acme%2Flibs%2Form"
		p := r.URL.EscapedPath()
		switch {
		case !strings.HasPrefix(p, base):
			http.NotFound(w, r)
		case p == base+"/repository/tags":
			if r.URL.Query().Get("page") == "1" {
				json.NewEncoder(w).Encode(tags)
			} else {
				w.Write([]byte("[]"))
			}
		case p == base:
			w.Write([]byte(`{"default_branch":"main"}`))
		case p == base+"/repository/commits/main":
			w.Write([]byte(`{"id":"cccccccccccccccccccccccccccccccccccccccc"}`))
		case strings.HasPrefix(p, base+"/repository/files/") && strings.HasSuffix(p, "/raw"):
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v4/projects/acme/libs/orm/repository/files/"), "/raw")
			body, ok := files[name]
			if !ok || r.URL.Query().Get("ref") == "" {
				http.NotFound(w, r)
				return
			}
			w.Write([]byte(body))
		case p == base+"/repository/archive.zip":
			var buf bytes.Buffer
			zw := zip.NewWriter(&buf)
			for name, body := range files {
				f, _ := zw.Create("orm-" + r.URL.Query().Get("sha") + "/" + name)
				f.Write([]byte(body))
			}
			zw.Close()
			w.Write(buf.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func tagJSON(name, sha string) map[string]any {
	return map[string]any{"name": name, "commit": map[string]string{"id": sha}}
}

func TestGitLab(t *testing.T) {
	sha1 := strings.Repeat("1", 40)
	sha2 := strings.Repeat("2", 40)
	srv, seen := fakeGitLab(t, []map[string]any{tagJSON("v0.1.0", sha1), tagJSON("v0.2.0", sha2), tagJSON("orm-pg@0.3.0", sha2), tagJSON("junk", sha1)})
	g := &GitLab{URL: srv.URL, Token: "secret", Client: srv.Client(), Tmp: t.TempDir()}
	ctx := context.Background()
	s, _ := spec.Parse("orm", "gitlab:acme/libs/orm#^0.1")
	cands, err := g.Candidates(ctx, "orm", s)
	if err != nil || len(cands) != 2 || cands[0].Version != "0.2.0" || cands[0].Commit != sha2 {
		t.Fatalf("candidates: %+v %v", cands, err)
	}
	m, err := g.Manifest(ctx, s, cands[1])
	if err != nil || m.Name != "orm" {
		t.Fatalf("manifest: %v %v", m, err)
	}
	dst := filepath.Join(t.TempDir(), "pkg")
	if err := g.Fetch(ctx, s, cands[1], dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "orm.go")); err != nil {
		t.Fatal("orm.go not fetched")
	}

	// Monorepo package: tags with its name, files from its subdirectory.
	pg, _ := spec.Parse("orm-pg", "gitlab:acme/libs/orm//drivers/pg")
	cands, err = g.Candidates(ctx, "orm-pg", pg)
	if err != nil || len(cands) != 1 || cands[0].Version != "0.3.0" {
		t.Fatalf("monorepo: %+v %v", cands, err)
	}
	dst = filepath.Join(t.TempDir(), "pkg")
	if err := g.Fetch(ctx, pg, cands[0], dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "pg.go")); err != nil {
		t.Fatal("subdirectory not fetched")
	}
	none, _ := spec.Parse("x", "gitlab:acme/libs/orm//drivers/none")
	if _, err := g.Manifest(ctx, none, cands[0]); !errors.Is(err, ErrNoManifest) {
		t.Fatalf("no gtr.json: %v", err)
	}
	if err := g.Fetch(ctx, spec.Spec{Kind: spec.GitLab, Repo: "acme/libs/orm", Subdir: "missing"}, cands[0], filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("missing subdirectory: want an error")
	}
	for _, line := range *seen {
		if !strings.HasPrefix(line, "secret ") {
			t.Fatalf("token not sent: %q", line)
		}
	}

	// Unknown project.
	other, _ := spec.Parse("x", "gitlab:acme/other")
	if _, err := g.Candidates(ctx, "x", other); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown project: %v", err)
	}
}

func TestGitLabHead(t *testing.T) {
	srv, _ := fakeGitLab(t, nil)
	g := &GitLab{URL: srv.URL, Client: srv.Client(), Tmp: t.TempDir()}
	s, _ := spec.Parse("orm", "gitlab:acme/libs/orm")
	cands, err := g.Candidates(context.Background(), "orm", s)
	if err != nil || len(cands) != 1 || !cands[0].Head || cands[0].Version != "0.0.0-gcccccccccccc" {
		t.Fatalf("head: %+v %v", cands, err)
	}
	t.Setenv("GTR_GITLAB_URL", "")
	t.Setenv("GITLAB_TOKEN", "tok")
	if n := NewGitLab("x"); n.URL != DefaultGitLab || n.Token != "tok" {
		t.Fatalf("defaults: %+v", n)
	}
	t.Setenv("GTR_GITLAB_URL", "https://git.corp")
	t.Setenv("GTR_GITLAB_TOKEN", "corp")
	if n := NewGitLab("x"); n.URL != "https://git.corp" || n.Token != "corp" {
		t.Fatalf("self-hosted: %+v", n)
	}
}

func TestGitLabTokenNotForwarded(t *testing.T) {
	var leaked string
	hit := false
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		leaked = r.Header.Get("PRIVATE-TOKEN")
		w.Write([]byte(`[]`))
	}))
	defer storage.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, storage.URL+"/elsewhere", http.StatusFound)
	}))
	defer api.Close()
	g := &GitLab{URL: api.URL, Token: "secret", Client: &http.Client{}, Tmp: t.TempDir()}
	s, _ := spec.Parse("x", "gitlab:a/b")
	g.listTags(context.Background(), s.Repo)
	if !hit || leaked != "" {
		t.Fatalf("redirect followed: %v, token on another host: %q", hit, leaked)
	}
}
