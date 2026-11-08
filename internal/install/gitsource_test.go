package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gtr-manager/internal/source"
)

func gitRepo(t *testing.T) (dir, url string, commit func(files map[string]string, tag string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	dir = t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	u := filepath.ToSlash(dir)
	if !strings.HasPrefix(u, "/") {
		u = "/" + u
	}
	return dir, "git+file://" + u, func(files map[string]string, tag string) string {
		for name, body := range files {
			os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
			os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
		}
		run("add", "-A")
		run("commit", "-q", "-m", "c")
		if tag != "" {
			run("tag", tag)
		}
		return run("rev-parse", "HEAD")
	}
}

func TestGitSourceEndToEnd(t *testing.T) {
	_, url, commit := gitRepo(t)
	v1 := commit(map[string]string{"gtr.json": gtrJSON("gitlib", nil, nil), "gitlib.go": "package gitlib\n\nconst V = 1\n"}, "v1.0.0")
	p := newProject(t, universe(t), `{"name":"app","version":"1.0.0"}`)
	p.in.Git = source.NewGit(filepath.Join(p.home.Root, "cache", "git"), p.home.Tmp())
	if err := p.in.Add(ctx, []string{url}, false); err != nil {
		t.Fatalf("%v\n%s", err, p.log)
	}
	if got := p.manifest(t).Dependencies["gitlib"]; got != url+"#^1.0.0" {
		t.Fatalf("gtr.json: %q", got)
	}
	e := p.lock(t).Packages["gitlib"]
	if e.Version != "1.0.0" || e.Source != url || e.Commit != v1 || e.Integrity == "" {
		t.Fatalf("lock: %+v", e)
	}
	if !strings.Contains(p.read("gtr_modules/gitlib/gitlib.go"), "V = 1") {
		t.Fatal("not linked")
	}

	commit(map[string]string{"gitlib.go": "package gitlib\n\nconst V = 2\n"}, "v1.1.0")
	p.in.Git = source.NewGit(filepath.Join(p.home.Root, "cache", "git"), p.home.Tmp()) // a new run
	if err := p.in.Install(ctx, Options{}); err != nil || p.lock(t).Packages["gitlib"].Version != "1.0.0" {
		t.Fatalf("install keeps the lock: %v", err)
	}
	if err := p.in.Install(ctx, Options{Update: map[string]bool{"gitlib": true}}); err != nil {
		t.Fatal(err)
	}
	if p.lock(t).Packages["gitlib"].Version != "1.1.0" || !strings.Contains(p.read("gtr_modules/gitlib/gitlib.go"), "V = 2") {
		t.Fatalf("update: %+v", p.lock(t).Packages["gitlib"])
	}

	// Offline: the git server is not asked.
	p.in.Git = &source.Git{Bin: "/nonexistent/git"}
	p.in.Offline = true
	p.in.offline = nil
	os.RemoveAll(filepath.Join(p.dir, ModulesDir))
	if err := p.in.Install(ctx, Options{Frozen: true}); err != nil {
		t.Fatalf("offline: %v", err)
	}
}
