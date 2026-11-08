package main

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gtr-manager/internal/source/githubtest"
)

// The end-to-end tests run the built gtr-manager on the projects in
// testdata/projects against a fake GitHub, the way a user would.

type e2e struct {
	t       *testing.T
	bin     string
	env     []string
	home    string // HOME: nothing may be written there
	gtrHome string
	gh      *githubtest.Server
}

func newE2E(t *testing.T) *e2e {
	t.Helper()
	if testing.Short() {
		t.Skip("end-to-end test")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not in PATH")
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "gtr-manager")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command(goBin, "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	e := &e2e{t: t, bin: bin, home: filepath.Join(tmp, "home"), gtrHome: filepath.Join(tmp, "gtrhome"), gh: githubtest.New(t)}
	os.MkdirAll(e.home, 0o755)
	gocache, _ := exec.Command(goBin, "env", "GOCACHE").Output()
	e.env = []string{
		"PATH=" + os.Getenv("PATH"), "SYSTEMROOT=" + os.Getenv("SYSTEMROOT"),
		"HOME=" + e.home, "USERPROFILE=" + e.home, "XDG_CONFIG_HOME=" + filepath.Join(e.home, ".config"),
		"APPDATA=" + filepath.Join(e.home, "AppData"), "LOCALAPPDATA=" + filepath.Join(e.home, "AppData", "Local"),
		"TMPDIR=" + filepath.Join(tmp, "tmp"), "TEMP=" + filepath.Join(tmp, "tmp"), "TMP=" + filepath.Join(tmp, "tmp"),
		"GTR_HOME=" + e.gtrHome, "GTR_GITHUB_API=" + e.gh.URL, "GTR_GOPROXY=http://127.0.0.1:1",
		"GOCACHE=" + strings.TrimSpace(string(gocache)), "GOPATH=" + filepath.Join(tmp, "gopath"),
		"GOMODCACHE=" + filepath.Join(tmp, "gopath", "pkg", "mod"), "GOFLAGS=", "GOTOOLCHAIN=local",
	}
	os.MkdirAll(filepath.Join(tmp, "tmp"), 0o755)
	e.gh.Commit("o/greet.go", map[string]string{
		"gtr.json": `{"name":"greet","version":"0.0.0"}`,
		"greet.go": "package greet\n\nfunc Hello(n string) string { return \"hello, \" + n }\n",
	}, "v1.0.0")
	t.Cleanup(e.checkHome)
	return e
}

// checkHome fails if anything but the go command's own telemetry was
// written to HOME.
func (e *e2e) checkHome() {
	filepath.WalkDir(e.home, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if rel, _ := filepath.Rel(e.home, p); !strings.Contains(filepath.ToSlash(rel), "go/telemetry") {
			e.t.Errorf("written outside the project and GTR_HOME: %s", rel)
		}
		return nil
	})
}

func (e *e2e) copy(project string) string {
	e.t.Helper()
	dst := filepath.Join(e.t.TempDir(), project)
	src := filepath.Join("testdata", "projects", project)
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return dst
}

// gtr runs gtr-manager in dir and returns its output; want is the expected
// success.
func (e *e2e) gtr(dir string, ok bool, args ...string) string {
	e.t.Helper()
	cmd := exec.Command(e.bin, args...)
	cmd.Dir, cmd.Env = dir, e.env
	out, err := cmd.CombinedOutput()
	if (err == nil) != ok {
		e.t.Fatalf("gtr %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

func (e *e2e) goCmd(dir string, args ...string) string {
	e.t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(append([]string(nil), e.env...), "GOPROXY=off", "GOWORK=off", "GOFLAGS=-mod=mod")
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestE2EProject(t *testing.T) {
	e := newE2E(t)
	app := e.copy("app")

	e.gtr(app, true, "add", "github:o/greet.go")
	if !strings.Contains(read(t, filepath.Join(app, "gtr.json")), `"greet": "github:o/greet.go#^1.0.0"`) {
		t.Fatalf("gtr.json:\n%s", read(t, filepath.Join(app, "gtr.json")))
	}
	out := e.gtr(app, true, "run", "test")
	if !strings.Contains(out, "go vet ./...") || !strings.Contains(out, "ok  \tapp") {
		t.Fatalf("run test (with the pretest hook):\n%s", out)
	}
	exe := filepath.Join(app, "out", "app")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	e.goCmd(app, "build", "-o", exe, ".")
	if got, _ := exec.Command(exe).Output(); string(got) != "hello, e2e\n" {
		t.Fatalf("binary printed %q", got)
	}
	if out := e.gtr(app, true, "list"); !strings.Contains(out, "greet@1.0.0  github:o/greet.go") {
		t.Fatalf("list:\n%s", out)
	}
	if out := e.gtr(app, true, "why", "greet"); !strings.Contains(out, "app → greet") {
		t.Fatalf("why:\n%s", out)
	}

	// gtr ci with an empty cache reproduces the same install.
	lockBefore := read(t, filepath.Join(app, "gtr.lock"))
	os.RemoveAll(e.gtrHome)
	os.RemoveAll(filepath.Join(app, "gtr_modules"))
	os.Remove(filepath.Join(app, "go.mod"))
	e.gtr(app, true, "ci")
	if read(t, filepath.Join(app, "gtr.lock")) != lockBefore {
		t.Fatal("gtr ci changed gtr.lock")
	}
	e.goCmd(app, "test", "./...")
	e.gtr(app, true, "ci", "--offline")

	// A new release: outdated, then update.
	e.gh.Commit("o/greet.go", map[string]string{"gtr.json": `{"name":"greet","version":"0.0.0"}`, "greet.go": "package greet\n\nfunc Hello(n string) string { return \"hello, \" + n }\n\nconst V = 2\n"}, "v1.1.0")
	if out := e.gtr(app, true, "outdated"); !strings.Contains(out, "greet") || !strings.Contains(out, "1.1.0") {
		t.Fatalf("outdated:\n%s", out)
	}
	out = e.gtr(app, true, "update")
	if !strings.Contains(read(t, filepath.Join(app, "gtr.lock")), `"version": "1.1.0"`) {
		t.Fatalf("update did not move to 1.1.0:\n%s\n%s", out, read(t, filepath.Join(app, "gtr.lock")))
	}

	e.gtr(app, true, "remove", "greet")
	if strings.Contains(read(t, filepath.Join(app, "gtr.json")), "greet") {
		t.Fatal("still in gtr.json")
	}
	if _, err := os.Stat(filepath.Join(app, "gtr_modules", "greet")); err == nil {
		t.Fatal("gtr_modules/greet left behind")
	}
	e.gtr(app, false, "run", "test") // main.go still imports greet
}

func TestE2EWorkspace(t *testing.T) {
	e := newE2E(t)
	ws := e.copy("workspace")

	e.gtr(filepath.Join(ws, "drivers", "fast"), true, "install") // from a member: installs the workspace
	for _, f := range []string{"go.work", "gtr.lock", "core/go.mod", "drivers/fast/go.mod", "drivers/slow/go.mod"} {
		if _, err := os.Stat(filepath.Join(ws, filepath.FromSlash(f))); err != nil {
			t.Fatalf("%s not generated", f)
		}
	}
	if _, err := os.Stat(filepath.Join(ws, "drivers", "fast", "gtr_modules")); err == nil {
		t.Fatal("a member got its own gtr_modules")
	}
	out := e.gtr(ws, true, "run", "-r", "test")
	if strings.Index(out, "[core]") > strings.Index(out, "[drivers/fast]") || strings.Count(out, "ok  \t") != 2 {
		t.Fatalf("run -r test:\n%s", out)
	}
	out = e.gtr(ws, true, "list")
	if !strings.Contains(out, "store-fast@0.1.0  drivers/fast") || !strings.Contains(out, "store@0.1.0  workspace:core") {
		t.Fatalf("list:\n%s", out)
	}
	if out := e.gtr(ws, true, "why", "greet"); !strings.Contains(out, "store-dev → store@0.1.0 → greet") && !strings.Contains(out, "store → greet") {
		t.Fatalf("why:\n%s", out)
	}
	e.gtr(ws, true, "ci")
}
