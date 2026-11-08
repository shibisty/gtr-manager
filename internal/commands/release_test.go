package commands

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gtr-manager/internal/install"
	"gtr-manager/internal/link"
	"gtr-manager/internal/lock"
	"gtr-manager/internal/manifest"
	"gtr-manager/internal/source/githubtest"
)

func TestBump(t *testing.T) {
	for _, c := range []struct{ cur, how, preid, want string }{
		{"1.2.3", "patch", "", "1.2.4"},
		{"1.2.3", "minor", "", "1.3.0"},
		{"1.2.3", "major", "", "2.0.0"},
		{"0.1.9", "minor", "", "0.2.0"},
		{"1.2.4-rc.1", "patch", "", "1.2.4"}, // releases the pre-release
		{"1.3.0-rc.0", "minor", "", "1.3.0"},
		{"1.3.1-rc.0", "minor", "", "1.4.0"},
		{"2.0.0-rc.2", "major", "", "2.0.0"},
		{"1.2.3", "prepatch", "", "1.2.4-rc.0"},
		{"1.2.3", "preminor", "beta", "1.3.0-beta.0"},
		{"1.2.3", "premajor", "", "2.0.0-rc.0"},
		{"1.2.3", "prerelease", "", "1.2.4-rc.0"},
		{"1.2.4-rc.0", "prerelease", "", "1.2.4-rc.1"},
		{"1.2.4-rc.9", "prerelease", "", "1.2.4-rc.10"},
		{"1.2.4-beta.3", "prerelease", "rc", "1.2.4-rc.0"},
		{"1.2.4-beta.3", "prerelease", "", "1.2.4-beta.4"},
		{"1.2.4-rc", "prerelease", "", "1.2.4-rc.1"},
		{"1.2.3", "3.0.0", "", "3.0.0"},
		{"1.2.3", "v3.0.0-rc.1", "", "3.0.0-rc.1"},
	} {
		got, err := bump(c.cur, c.how, c.preid)
		if err != nil || got != c.want {
			t.Errorf("bump(%s, %s, %q) = %q, %v; want %s", c.cur, c.how, c.preid, got, err, c.want)
		}
	}
	for _, c := range [][2]string{{"1.2.3", "huge"}, {"1.2", "patch"}, {"", "patch"}} {
		if _, err := bump(c[0], c[1], ""); err == nil {
			t.Errorf("bump(%q, %q): no error", c[0], c[1])
		}
	}
}

func TestExpandLdflags(t *testing.T) {
	b := buildInfo{name: "app", version: "1.2.3", commit: "abc123", date: time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)}
	got, err := expandLdflags("-s -w -X main.Version={{version}} -X main.Commit={{ commit }} -X main.Date={{date}} -X main.Name={{name}}", b)
	if want := "-s -w -X main.Version=1.2.3 -X main.Commit=abc123 -X main.Date=2026-11-08T10:00:00Z -X main.Name=app"; err != nil || got != want {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := expandLdflags("-X main.V={{tag}}", b); err == nil || !strings.Contains(err.Error(), "{{tag}}") {
		t.Fatalf("unknown placeholder: %v", err)
	}
}

func TestParseTestArgs(t *testing.T) {
	o, err := parseTestArgs([]string{"--integration", "--e2e", "--coverage", "--min", "80", "--run", "TestX", "--", "-shuffle=on"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(o.tags, ",") != "integration,e2e" || !o.coverage || o.min == nil || *o.min != 80 || o.run != "TestX" || strings.Join(o.goArgs, " ") != "-shuffle=on" {
		t.Fatalf("%+v", o)
	}
	if _, err := parseTestArgs([]string{"--nope"}); err == nil {
		t.Fatal("unknown flag accepted")
	}
}

func TestCoverageProfiles(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.out"), filepath.Join(dir, "b.out")
	os.WriteFile(a, []byte("mode: atomic\nx/a.go:1.1,3.2 2 1\nx/a.go:4.1,6.2 2 0\n"), 0o644)
	os.WriteFile(b, []byte("mode: atomic\nx/a.go:4.1,6.2 2 3\nx/b.go:1.1,2.2 4 0\n"), 0o644)
	pa, err := readProfile(a)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := readProfile(b)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range pb {
		pa[k] = mergeBlock(pa[k], v)
	}
	if got := percent(pa); got < 49.9 || got > 50.1 { // 4 of 8 statements
		t.Fatalf("percent = %v", got)
	}
	out := filepath.Join(dir, "all.out")
	if err := writeProfile(out, pa); err != nil {
		t.Fatal(err)
	}
	if again, err := readProfile(out); err != nil || len(again) != 3 {
		t.Fatalf("round trip: %v %v", again, err)
	}
}

func TestReleaseChangelog(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "CHANGELOG.md"), []byte("# Changelog\n\n## Unreleased\n\n### Added\n- x\n\n## 0.1.0 — 2026-01-01\n"), 0o644)
	ok, err := releaseChangelog(dir, "0.2.0", time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC))
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "CHANGELOG.md"))
	if want := "# Changelog\n\n## Unreleased\n\n## 0.2.0 — 2026-11-08\n\n### Added\n- x\n\n## 0.1.0"; !strings.HasPrefix(string(data), want) {
		t.Fatalf("got:\n%s", data)
	}
	os.WriteFile(filepath.Join(dir, "CHANGELOG.md"), []byte("# Changelog\r\n\r\n## Unreleased\r\n\r\n- y\r\n"), 0o644)
	if ok, err := releaseChangelog(dir, "0.3.0", time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)); err != nil || !ok {
		t.Fatal("CRLF:", ok, err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "CHANGELOG.md")); string(data) != "# Changelog\r\n\r\n## Unreleased\r\n\r\n## 0.3.0 — 2026-11-09\r\n\r\n- y\r\n" {
		t.Fatalf("CRLF: %q", data)
	}
	if ok, err := releaseChangelog(t.TempDir(), "1.0.0", time.Now()); ok || err != nil {
		t.Fatal("no CHANGELOG.md:", ok, err)
	}
}

func TestCIWorkflow(t *testing.T) {
	out := project(t, `{"name":"app","version":"1.0.0","engines":{"go":">=1.22"}}`)
	if err := Init([]string{"--ci"}); err != nil {
		t.Fatal(err, out)
	}
	data, err := os.ReadFile(filepath.FromSlash(CIWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`go: ["1.22", ">=1.22"]`, "gtr ci", "gtr test --coverage", "install.ps1", "GTR_GO_VERSION"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("ci.yml lacks %q", want)
		}
	}
	if err := Init([]string{"--ci"}); err == nil {
		t.Fatal("overwrote ci.yml")
	}
}

// goTest makes the go command usable by the commands under test: the
// test binary may run with GOFLAGS (-modfile) meant for gtr-manager itself.
func goTest(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not in PATH")
	}
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOWORK", "off")
}

func gitTest(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not in PATH")
	}
	for _, kv := range []string{"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull} {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	run(t, dir, "git", "init", "-q", "-b", "main")
}

func run(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestBuildAllTargets(t *testing.T) {
	goTest(t)
	out := project(t, `{"name":"hello","version":"1.4.0",
		"build":{"entry":"./cmd/hello","ldflags":"-s -w -X main.Version={{version}}"}}`)
	os.MkdirAll("cmd/hello", 0o755)
	os.WriteFile("go.mod", []byte("module hello\n\ngo 1.22\n"), 0o644)
	os.WriteFile("cmd/hello/main.go", []byte("package main\n\nimport \"fmt\"\n\nvar Version = \"dev\"\n\nfunc main() { fmt.Println(Version) }\n"), 0o644)
	os.WriteFile("README.md", []byte("hello\n"), 0o644)

	if err := Build([]string{"--all-targets", "--archive"}); err != nil {
		t.Fatal(err, out)
	}
	var bins []string
	for _, tg := range DefaultTargets {
		goos, arch, _ := strings.Cut(tg, "/")
		name := "hello"
		if goos == "windows" {
			name += ".exe"
		}
		bin := filepath.Join("build", "1.4.0", goos+"-"+arch, name)
		if _, err := os.Stat(bin); err != nil {
			t.Errorf("%s: %v", tg, err)
		}
		bins = append(bins, bin)
	}
	if len(bins) != 6 {
		t.Fatalf("%d binaries", len(bins))
	}
	sums, err := os.ReadFile(filepath.Join("build", "1.4.0", "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(sums), "\n"); n != 6 {
		t.Fatalf("SHA256SUMS has %d lines:\n%s", n, sums)
	}
	for _, name := range []string{"hello_1.4.0_linux_amd64.tar.gz", "hello_1.4.0_windows_arm64.zip"} {
		if !strings.Contains(string(sums), "  "+name+"\n") {
			t.Errorf("SHA256SUMS lacks %s", name)
		}
	}
	// The archive holds the binary at its root, and README.md.
	z, err := zip.OpenReader(filepath.Join("build", "1.4.0", "hello_1.4.0_windows_amd64.zip"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range z.File {
		names = append(names, f.Name)
	}
	z.Close()
	if strings.Join(names, ",") != "hello.exe,README.md" {
		t.Errorf("zip: %v", names)
	}
	f, err := os.Open(filepath.Join("build", "1.4.0", "hello_1.4.0_linux_arm64.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	h, err := tar.NewReader(gz).Next()
	if err != nil || h.Name != "hello" || h.Mode&0o111 == 0 {
		t.Fatalf("tar: %+v %v", h, err)
	}

	// The host binary prints the version from {{version}}.
	host := filepath.Join("build", "1.4.0", runtime.GOOS+"-"+runtime.GOARCH, "hello")
	if runtime.GOOS == "windows" {
		host += ".exe"
	}
	if contains(DefaultTargets, runtime.GOOS+"/"+runtime.GOARCH) {
		if got := run(t, ".", host); got != "1.4.0" {
			t.Errorf("binary prints %q", got)
		}
	}
	if err := Build([]string{"-t", "plan9/sparc"}); err == nil {
		t.Error("unsupported target built")
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestTestCoverageThreshold(t *testing.T) {
	goTest(t)
	out := project(t, `{"name":"calc","version":"0.1.0","test":{"coverage":90,"race":false}}`)
	os.WriteFile("go.mod", []byte("module calc\n\ngo 1.22\n"), 0o644)
	os.WriteFile("calc.go", []byte("package calc\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Sub(a, b int) int { return a - b }\n"), 0o644)
	os.WriteFile("calc_test.go", []byte("package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal()\n\t}\n}\n"), 0o644)
	os.WriteFile("e2e_test.go", []byte("//go:build e2e\n\npackage calc\n\nimport \"testing\"\n\nfunc TestSub(t *testing.T) {\n\tif Sub(3, 2) != 1 {\n\t\tt.Fatal()\n\t}\n}\n"), 0o644)

	if err := Test(nil); err != nil {
		t.Fatal(err, out)
	}
	err := Test([]string{"--coverage", "--html"})
	if code, _ := ExitCode(err); err == nil || code == 0 {
		t.Fatalf("50%% coverage passed a 90%% threshold: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join("build", "coverage.html")); err != nil {
		t.Error("no coverage.html:", err)
	}
	out.Reset()
	if err := Test([]string{"--e2e", "--coverage"}); err != nil { // the e2e tag covers Sub too
		t.Fatal(err, out)
	}
	if !strings.Contains(out.String(), "100.0%") {
		t.Errorf("output:\n%s", out)
	}
}

func TestVersionAndPublish(t *testing.T) {
	out := project(t, `{"name":"lib","version":"0.1.0"}`)
	wd, _ := os.Getwd()
	gitTest(t, wd)
	os.WriteFile("CHANGELOG.md", []byte("# Changelog\n\n## Unreleased\n\n- first\n"), 0o644)
	run(t, wd, "git", "add", ".")
	run(t, wd, "git", "commit", "-qm", "init")

	if err := Version([]string{"minor"}, "test"); err != nil {
		t.Fatal(err, out)
	}
	if v := load(t).Version; v != "0.2.0" {
		t.Fatalf("version %s", v)
	}
	if got := run(t, wd, "git", "describe", "--tags", "--exact-match"); got != "v0.2.0" {
		t.Fatalf("tag %q", got)
	}
	if st := run(t, wd, "git", "status", "--porcelain"); st != "" {
		t.Fatalf("not committed:\n%s", st)
	}
	if data, _ := os.ReadFile("CHANGELOG.md"); !strings.Contains(string(data), "## Unreleased\n\n## 0.2.0 — ") {
		t.Fatalf("CHANGELOG.md:\n%s", data)
	}
	if err := Version([]string{"0.1.5"}, "test"); err == nil {
		t.Fatal("went down a version")
	}
	os.WriteFile("x.txt", []byte("x"), 0o644)
	if err := Version([]string{"patch"}, "test"); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("dirty repository: %v", err)
	}
	os.Remove("x.txt")

	// Publish to a bare remote.
	remote := t.TempDir()
	run(t, remote, "git", "init", "-q", "--bare")
	run(t, wd, "git", "remote", "add", "origin", remote)
	out.Reset()
	if err := Publish([]string{"--skip-tests", "--dry-run"}); err != nil {
		t.Fatal(err, out)
	}
	if !strings.Contains(out.String(), "push --atomic origin main refs/tags/v0.2.0") {
		t.Fatalf("dry run:\n%s", out)
	}
	if err := Publish([]string{"--skip-tests"}); err != nil {
		t.Fatal(err, out)
	}
	if got := run(t, remote, "git", "tag"); got != "v0.2.0" {
		t.Fatalf("remote tags %q", got)
	}
	if err := Publish([]string{"--skip-tests"}); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("published twice: %v", err)
	}
	// A commit after the tag: the version must be bumped first.
	os.WriteFile("y.txt", []byte("y"), 0o644)
	run(t, wd, "git", "add", ".")
	run(t, wd, "git", "commit", "-qm", "more")
	if err := Publish([]string{"--skip-tests"}); err == nil || !strings.Contains(err.Error(), "not on HEAD") {
		t.Fatalf("untagged HEAD: %v", err)
	}
	// Local sources cannot be published.
	m := load(t)
	m.Dependencies = map[string]string{"util": "file:../util"}
	manifest.Save(manifest.FileName, m)
	if err := Publish([]string{"--skip-tests", "--dry-run"}); err == nil || !strings.Contains(err.Error(), "file:../util") {
		t.Fatalf("file: dependency: %v", err)
	}
}

func TestVersionTagInSubdirectory(t *testing.T) {
	out := project(t, "")
	wd, _ := os.Getwd()
	gitTest(t, wd)
	os.MkdirAll("drivers/mysql", 0o755)
	os.WriteFile("drivers/mysql/gtr.json", []byte(`{"name":"orm-mysql","version":"0.1.0"}`), 0o644)
	run(t, wd, "git", "add", ".")
	run(t, wd, "git", "commit", "-qm", "init")
	t.Chdir("drivers/mysql")
	if err := Version([]string{"patch"}, "test"); err != nil {
		t.Fatal(err, out)
	}
	if got := run(t, wd, "git", "tag"); got != "orm-mysql@0.1.1" {
		t.Fatalf("tag %q", got)
	}
	out.Reset()
	if err := Version(nil, "9.9.9"); err != nil || out.String() != "orm-mysql 0.1.1\ngtr-manager 9.9.9\n" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestLink(t *testing.T) {
	t.Setenv("GTR_HOME", t.TempDir())
	useGitHub(t, githubtestServer(t))
	base := t.TempDir()
	lib := filepath.Join(base, "strutil")
	os.MkdirAll(lib, 0o755)
	os.WriteFile(filepath.Join(lib, "gtr.json"), []byte(`{"name":"strutil","version":"1.0.0"}`), 0o644)
	os.WriteFile(filepath.Join(lib, "s.go"), []byte("package strutil\n"), 0o644)

	out := project(t, `{"name":"app","version":"1.0.0","dependencies":{"strutil":"github:o/strutil.go#^1.0"}}`)
	app, _ := os.Getwd()
	if err := Install(nil); err != nil {
		t.Fatal(err, out)
	}
	before, _ := os.ReadFile(lock.FileName)

	// Register in the package, link by name in the project.
	t.Chdir(lib)
	if err := Link(nil); err != nil {
		t.Fatal(err, out)
	}
	t.Chdir(app)
	if err := Link([]string{"strutil"}); err != nil {
		t.Fatal(err, out)
	}
	// link.Target, not EvalSymlinks: Go does not follow Windows junctions.
	if got := link.Target(filepath.Join(install.ModulesDir, "strutil")); !samePath(got, lib) {
		t.Fatalf("gtr_modules/strutil → %q, want %s", got, lib)
	}
	if after, _ := os.ReadFile(lock.FileName); string(after) != string(before) {
		t.Fatalf("gtr.lock changed:\n%s", after)
	}
	if data, _ := os.ReadFile(".gitignore"); !strings.Contains(string(data), ".gtr/") {
		t.Errorf(".gitignore:\n%s", data)
	}
	if err := Publish([]string{"--skip-tests", "--dry-run"}); err == nil {
		t.Fatal("published with links")
	}
	if err := CI(nil); err == nil || !strings.Contains(err.Error(), "gtr unlink") {
		t.Fatalf("gtr ci with links: %v", err)
	}
	if err := Unlink(nil); err != nil {
		t.Fatal(err, out)
	}
	if got := link.Target(filepath.Join(install.ModulesDir, "strutil")); samePath(got, lib) {
		t.Fatal("still linked after gtr unlink")
	}
	if _, err := os.Stat(filepath.FromSlash(install.LinksFile)); !os.IsNotExist(err) {
		t.Fatal("links.json left behind")
	}
	// By path, without registering.
	if err := Link([]string{lib}); err != nil {
		t.Fatal(err, out)
	}
	if err := Link([]string{"nothing"}); err == nil {
		t.Fatal("linked an unregistered name")
	}
}

func githubtestServer(t *testing.T) *githubtest.Server {
	gh := githubtest.New(t)
	gh.Commit("o/strutil.go", map[string]string{"gtr.json": `{"name":"strutil","version":"0"}`, "s.go": "package strutil"}, "v1.0.0")
	return gh
}

// samePath compares paths as the file system does (case-insensitively on Windows).
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
