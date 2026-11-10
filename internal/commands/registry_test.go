package commands

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gtr-manager/internal/registry"
	"gtr-manager/internal/registry/registrytest"
)

func TestRegistryCommands(t *testing.T) {
	reg := registrytest.New(t)
	t.Setenv("GTR_HOME", t.TempDir())
	t.Setenv("GTR_REGISTRY", "")
	t.Setenv("GTR_REGISTRY_TOKEN", "")
	out := project(t, `{"name":"strutil","version":"1.0.0","description":"Strings","engines":{"go":">=1.22"}}`)
	os.WriteFile("strutil.go", []byte("package strutil\n"), 0o644)
	os.WriteFile("go.mod", []byte("module strutil\n"), 0o644) // generated: not published
	os.MkdirAll("gtr_modules/x", 0o755)
	os.WriteFile("gtr_modules/x/x.go", []byte("package x\n"), 0o644)
	os.WriteFile(".env", []byte("SECRET=1\n"), 0o644)
	os.MkdirAll(".github/workflows", 0o755)
	os.WriteFile(".github/workflows/ci.yml", []byte("on: push\n"), 0o644)

	// not logged in: publish takes the git path, which needs a repository
	if err := Publish([]string{"--skip-tests"}); err == nil || !strings.Contains(err.Error(), "git") {
		t.Fatal(err)
	}
	if err := Whoami(nil); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatal(err)
	}

	// the launcher's config.json (default versions, links) is not ours
	launcher := filepath.Join(os.Getenv("GTR_HOME"), "config.json")
	os.MkdirAll(os.Getenv("GTR_HOME"), 0o755)
	os.WriteFile(launcher, []byte(`{"links": {"gtr": "/dev/gtr-manager"}}`+"\n"), 0o644)

	Stdin = strings.NewReader("ann@example.com\nwrong\n")
	if err := Login([]string{"--registry", reg.URL}); err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Fatal(err)
	}
	Stdin = strings.NewReader("ann@example.com\nsecret\n")
	if err := Login([]string{"--registry", reg.URL + "/"}); err != nil || !strings.Contains(out.String(), "Logged in to "+reg.URL+" as Ann <ann@example.com>") {
		t.Fatal(err, out)
	}
	if registry.URL() != reg.URL || registry.Token(reg.URL) != "gtr_ann@example.com" {
		t.Fatal(registry.URL(), registry.Token(reg.URL))
	}
	if st, _ := os.Stat(filepath.Join(os.Getenv("GTR_HOME"), "registry.json")); st.Mode().Perm()&0o077 != 0 && runtimeNotWindows() {
		t.Fatal("registry.json readable by others", st.Mode())
	}
	if data, _ := os.ReadFile(launcher); !strings.Contains(string(data), "/dev/gtr-manager") || strings.Contains(string(data), "token") {
		t.Fatalf("login changed the launcher's config.json: %s", data)
	}
	out.Reset()
	if err := Whoami(nil); err != nil || !strings.Contains(out.String(), "Ann <ann@example.com>") {
		t.Fatal(err, out)
	}

	// publish: dry run lists the archive
	out.Reset()
	if err := Publish([]string{"--dry-run", "--skip-tests"}); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "  gtr.json\n  strutil.go\n") || strings.Contains(s, "go.mod") || strings.Contains(s, "gtr_modules") || strings.Contains(s, ".env") || strings.Contains(s, ".github") {
		t.Fatal(s)
	}
	out.Reset()
	if err := Publish([]string{"--skip-tests"}); err != nil || !strings.Contains(out.String(), "Published strutil 1.0.0 to "+reg.URL) {
		t.Fatal(err, out)
	}
	if err := Publish([]string{"--skip-tests"}); err == nil || !strings.Contains(err.Error(), "published already") {
		t.Fatal(err)
	}

	out.Reset()
	if err := Search([]string{"str"}); err != nil || !strings.Contains(out.String(), "strutil  1.0.0") {
		t.Fatal(err, out)
	}
	out.Reset()
	if err := Info([]string{"strutil"}); err != nil || !strings.Contains(out.String(), "strutil 1.0.0") || !strings.Contains(out.String(), "gtr add strutil") {
		t.Fatal(err, out)
	}
	if err := Info([]string{"nothing"}); err == nil || !strings.Contains(err.Error(), "no package nothing") {
		t.Fatal(err)
	}
	out.Reset()
	if err := Yank([]string{"strutil@1.0.0"}, false); err != nil || !strings.Contains(out.String(), "Yanked strutil@1.0.0") {
		t.Fatal(err, out)
	}
	if err := Yank([]string{"strutil@1.0.0", "--undo"}, false); err != nil {
		t.Fatal(err)
	}
	if err := Yank([]string{"strutil"}, false); err == nil {
		t.Fatal("yank without a version")
	}
	out.Reset()
	if err := Owner([]string{"add", "strutil", "bob@example.com"}); err != nil || !strings.Contains(out.String(), "bob@example.com") {
		t.Fatal(err, out)
	}
	out.Reset()
	if err := Owner([]string{"strutil"}); err != nil || out.String() != "ann@example.com\nbob@example.com\n" {
		t.Fatal(err, out)
	}
	if err := Owner([]string{"bad"}); err == nil {
		t.Fatal("owner of a missing package")
	}

	// --git keeps the old way; --registry takes another one
	if err := Publish([]string{"--git", "--skip-tests"}); err == nil || !strings.Contains(err.Error(), "git") {
		t.Fatal(err)
	}
	out.Reset()
	if err := Logout(nil); err != nil || !strings.Contains(out.String(), "Logged out") || registry.Token(reg.URL) != "" {
		t.Fatal(err, out)
	}
	if err := Publish([]string{"--registry", reg.URL, "--skip-tests"}); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatal(err)
	}
	// a token from the dashboard
	out.Reset()
	if err := Login([]string{"--token", "gtr_ann@example.com"}); err != nil || !strings.Contains(out.String(), "as Ann") {
		t.Fatal(err, out)
	}
}

func TestPackageFilesGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	project(t, `{"name":"lib","version":"0.1.0"}`)
	for f, body := range map[string]string{"lib.go": "package lib\n", "sub/a.go": "package sub\n", "ignored.txt": "x", ".gitignore": "ignored.txt\n", "go.mod": "module lib\n", "build/out": "x"} {
		os.MkdirAll(filepath.Dir(f), 0o755)
		os.WriteFile(f, []byte(body), 0o644)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatal(string(out))
		}
	}
	files, err := packageFiles(".", true)
	if err != nil || strings.Join(files, ",") != ".gitignore,gtr.json,lib.go,sub/a.go" {
		t.Fatal(files, err)
	}
	os.WriteFile(".gitignore", []byte("gtr.json\n"), 0o644)
	exec.Command("git", "rm", "-q", "--cached", "gtr.json").Run()
	if _, err := packageFiles(".", true); err == nil {
		t.Fatal("gtr.json ignored")
	}
}

func runtimeNotWindows() bool { return os.PathSeparator == '/' }
