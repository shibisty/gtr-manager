package commands

import (
	"bytes"
	"context"
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

// TestPublishWorkspace: gtr publish -r publishes the members of a monorepo
// in dependency order, with workspace: ranges turned into registry ranges in
// the archive's gtr.json (ADR-0012).
func TestPublishWorkspace(t *testing.T) {
	reg := registrytest.New(t)
	t.Setenv("GTR_HOME", t.TempDir())
	t.Setenv("GTR_REGISTRY", reg.URL)
	t.Setenv("GTR_REGISTRY_TOKEN", "")
	out := project(t, `{"name":"orm-dev","version":"0.0.0","private":true,"workspaces":["core","drivers/*","example"]}`)
	files := map[string]string{
		"core/gtr.json":             `{"name":"orm","version":"0.2.0","engines":{"go":">=1.22"}}`,
		"core/orm.go":               "package orm\n",
		"drivers/postgres/gtr.json": `{"name":"@orm/postgres","version":"0.1.0","dependencies":{"orm":"workspace:^"},"devDependencies":{"@orm/mysql":"workspace:*"}}`,
		"drivers/postgres/pg.go":    "package postgres\n",
		"drivers/mysql/gtr.json":    `{"name":"@orm/mysql","version":"0.1.4","dependencies":{"orm":"file:../../core"},"peerDependencies":{"@orm/postgres":"workspace:~"}}`,
		"drivers/mysql/my.go":       "package mysql\n",
		"example/gtr.json":          `{"name":"orm-example","version":"0.0.0","private":true,"dependencies":{"@orm/postgres":"workspace:*"}}`,
	}
	for name, body := range files {
		os.MkdirAll(filepath.Dir(name), 0o755)
		os.WriteFile(name, []byte(body), 0o644)
	}
	if err := Publish([]string{"-r", "--skip-tests"}); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatal(err)
	}
	if err := Login([]string{"--token", "gtr_ann@example.com"}); err != nil {
		t.Fatal(err)
	}
	t.Chdir("drivers/postgres") // anywhere in the workspace
	out.Reset()
	if err := Publish([]string{"-r", "--skip-tests"}); err != nil {
		t.Fatal(err, out)
	}
	s := out.String()
	i, j, k := strings.Index(s, "Published orm 0.2.0"), strings.Index(s, "Published @orm/postgres 0.1.0"), strings.Index(s, "Published @orm/mysql 0.1.4")
	if i < 0 || j < 0 || k < 0 || !(i < j && j < k) || !strings.Contains(s, "- orm-example (example): private") {
		t.Fatal(s)
	}
	ctx := context.Background()
	manifestOf := func(name, version string) string {
		c := reg.Client()
		ix, err := c.Package(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		var buf bytes.Buffer
		for _, v := range ix.Versions {
			if v.Version == version {
				if err := c.Download(ctx, name, version, v.SHA256, &buf); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := registry.Extract(&buf, dir); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(dir, "gtr.json"))
		return string(data)
	}
	if m := manifestOf("@orm/postgres", "0.1.0"); !strings.Contains(m, `"orm": "^0.2.0"`) || !strings.Contains(m, `"@orm/mysql": "workspace:*"`) {
		t.Fatal(m) // devDependencies stay as they are
	}
	if m := manifestOf("@orm/mysql", "0.1.4"); !strings.Contains(m, `"orm": "^0.2.0"`) || !strings.Contains(m, `"@orm/postgres": "~0.1.0"`) {
		t.Fatal(m)
	}
	if data, _ := os.ReadFile("gtr.json"); !strings.Contains(string(data), "workspace:^") {
		t.Fatal("the member's own gtr.json was changed:", string(data))
	}

	// Again: nothing new. A bumped member alone goes up.
	out.Reset()
	if err := Publish([]string{"-r", "--skip-tests"}); err != nil || !strings.Contains(out.String(), "Nothing to publish") {
		t.Fatal(err, out)
	}
	os.WriteFile("../mysql/gtr.json", []byte(`{"name":"@orm/mysql","version":"0.1.5","dependencies":{"orm":"workspace:^0.3"}}`), 0o644)
	out.Reset()
	if err := Publish([]string{"-r", "--skip-tests"}); err == nil || !strings.Contains(err.Error(), "does not match the member's version 0.2.0") {
		t.Fatal(err, out)
	}
	os.WriteFile("../mysql/gtr.json", []byte(`{"name":"@orm/mysql","version":"0.1.5","dependencies":{"orm":"workspace:^0.2"}}`), 0o644)
	out.Reset()
	if err := Publish([]string{"-r", "--skip-tests"}); err != nil || !strings.Contains(out.String(), "Published @orm/mysql 0.1.5") || strings.Contains(out.String(), "Published orm") {
		t.Fatal(err, out)
	}

	// A member that depends on a private one, or on a path outside, is refused.
	os.WriteFile("gtr.json", []byte(`{"name":"@orm/postgres","version":"0.1.1","dependencies":{"orm-example":"workspace:*","x":"file:../../../x"}}`), 0o644)
	if err := Publish([]string{"--skip-tests"}); err == nil || !strings.Contains(err.Error(), "is private") || !strings.Contains(err.Error(), "not a member") {
		t.Fatal(err)
	}
	if err := Publish([]string{"-r", "--git"}); err == nil {
		t.Fatal("-r with --git")
	}
}
