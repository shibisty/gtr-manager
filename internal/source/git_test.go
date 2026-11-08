package source

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gtr-manager/internal/spec"
)

// GitRepo is a local repository for tests; URL is its git+file:// spelling
// without "git+".
type gitRepo struct {
	t   *testing.T
	dir string
	URL string
}

func newGitRepo(t *testing.T) *gitRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	dir := t.TempDir()
	r := &gitRepo{t: t, dir: dir}
	r.git("init", "-q", "-b", "main")
	u := filepath.ToSlash(dir)
	if !strings.HasPrefix(u, "/") {
		u = "/" + u // C:/x → /C:/x
	}
	r.URL = "file://" + u
	return r
}

func (r *gitRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
	cmd.Dir = r.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes files, commits them and tags the commit; it returns the SHA.
func (r *gitRepo) commit(files map[string]string, tags ...string) string {
	r.t.Helper()
	for name, body := range files {
		p := filepath.Join(r.dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "c")
	for _, tg := range tags {
		if strings.HasPrefix(tg, "annotated:") {
			r.git("tag", "-a", "-m", "m", strings.TrimPrefix(tg, "annotated:"))
		} else {
			r.git("tag", tg)
		}
	}
	return r.git("rev-parse", "HEAD")
}

func TestGit(t *testing.T) {
	r := newGitRepo(t)
	first := r.commit(map[string]string{"gtr.json": `{"name":"lib","version":"0.0.0"}`, "lib.go": "package lib // v1"}, "v1.0.0")
	second := r.commit(map[string]string{"lib.go": "package lib // v1.1", "sub/gtr.json": `{"name":"sub","version":"0.0.0"}`, "sub/s.go": "package sub"}, "annotated:v1.1.0", "sub@0.2.0", "nope")
	home := t.TempDir()
	g := &Git{Cache: filepath.Join(home, "git"), Tmp: filepath.Join(home, "tmp")}
	ctx := context.Background()
	s, err := spec.Parse("lib", "git+"+r.URL+"#^1")
	if err != nil {
		t.Fatal(err)
	}
	cands, err := g.Candidates(ctx, "lib", s)
	if err != nil || len(cands) != 2 || cands[0].Version != "1.1.0" || cands[0].Commit != second || cands[1].Commit != first {
		t.Fatalf("candidates: %+v %v", cands, err)
	}
	m, err := g.Manifest(ctx, s, cands[1])
	if err != nil || m.Name != "lib" {
		t.Fatalf("manifest: %v %v", m, err)
	}
	dst := filepath.Join(t.TempDir(), "pkg")
	if err := g.Fetch(ctx, s, cands[1], dst); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dst, "lib.go")); string(data) != "package lib // v1" {
		t.Fatalf("v1.0.0 content: %q", data)
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); err == nil {
		t.Fatal(".git exported")
	}

	sub, _ := spec.Parse("sub", "git+"+r.URL+"//sub")
	cands, err = g.Candidates(ctx, "sub", sub)
	if err != nil || len(cands) != 1 || cands[0].Version != "0.2.0" {
		t.Fatalf("monorepo: %+v %v", cands, err)
	}
	dst = filepath.Join(t.TempDir(), "pkg")
	if err := g.Fetch(ctx, sub, cands[0], dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "s.go")); err != nil {
		t.Fatal("subdirectory not exported")
	}
	if _, err := g.Manifest(ctx, spec.Spec{Kind: spec.Git, URL: s.URL, Subdir: "none"}, cands[0]); !errors.Is(err, ErrNoManifest) {
		t.Fatalf("no gtr.json: %v", err)
	}
}

func TestGitHeadAndErrors(t *testing.T) {
	r := newGitRepo(t)
	sha := r.commit(map[string]string{"gtr.json": `{"name":"lib","version":"0.0.0"}`})
	g := &Git{Cache: filepath.Join(t.TempDir(), "git"), Tmp: t.TempDir()}
	s, _ := spec.Parse("lib", "git+"+r.URL)
	cands, err := g.Candidates(context.Background(), "lib", s)
	if err != nil || len(cands) != 1 || !cands[0].Head || cands[0].Commit != sha || cands[0].Version != "0.0.0-g"+sha[:12] {
		t.Fatalf("head: %+v %v", cands, err)
	}
	dst := filepath.Join(t.TempDir(), "pkg")
	if err := g.Fetch(context.Background(), s, cands[0], dst); err != nil {
		t.Fatal(err)
	}
	// The branch moved after listing: the old commit is no longer fetchable
	// through HEAD.
	r.commit(map[string]string{"x.go": "package lib"})
	g2 := &Git{Cache: filepath.Join(t.TempDir(), "git"), Tmp: t.TempDir()}
	if _, err := g2.Manifest(context.Background(), s, cands[0]); err == nil || !strings.Contains(err.Error(), "no longer points") {
		t.Fatalf("moved head: %v", err)
	}

	missing, _ := spec.Parse("x", "git+file:///nonexistent/repo")
	if _, err := g.Candidates(context.Background(), "x", missing); err == nil {
		t.Fatal("missing repository: want an error")
	}
	if runtime.GOOS != "windows" {
		noGit := &Git{Bin: "/nonexistent/git", Cache: t.TempDir()}
		if _, err := noGit.Candidates(context.Background(), "x", s); err == nil || !strings.Contains(err.Error(), "need the git command") {
			t.Fatalf("no git: %v", err)
		}
	}
}
