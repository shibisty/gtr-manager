package commands

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gtr-manager/internal/home"
	"gtr-manager/internal/install"
	"gtr-manager/internal/manifest"
	"gtr-manager/internal/source"
	"gtr-manager/internal/source/githubtest"
)

// project changes into a temporary directory and replaces IO.
func project(t *testing.T, gtrJSON string) *bytes.Buffer {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	if gtrJSON != "" {
		os.WriteFile(manifest.FileName, []byte(gtrJSON), 0o644)
	}
	out := &bytes.Buffer{}
	oldIn, oldOut, oldErr := Stdin, Stdout, Stderr
	Stdin, Stdout, Stderr = strings.NewReader(""), out, out
	t.Cleanup(func() { Stdin, Stdout, Stderr = oldIn, oldOut, oldErr })
	return out
}

func useGitHub(t *testing.T, gh *githubtest.Server) {
	old := NewInstaller
	homeDir := t.TempDir()
	NewInstaller = func(dir string) (*install.Installer, error) {
		l := home.Layout{Root: homeDir}
		return &install.Installer{Dir: dir, Home: l, GitHub: &source.GitHub{API: gh.URL, Client: gh.Client(), Tmp: l.Tmp()}, Log: Stderr}, nil
	}
	t.Cleanup(func() { NewInstaller = old })
}

func load(t *testing.T) *manifest.Manifest {
	m, err := manifest.Load(manifest.FileName)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPackageCommands(t *testing.T) {
	gh := githubtest.New(t)
	gh.Commit("o/strutil.go", map[string]string{"gtr.json": `{"name":"strutil","version":"0"}`, "s.go": "package strutil"}, "v1.0.0")
	gh.Commit("o/testkit.go", map[string]string{"gtr.json": `{"name":"testkit","version":"0"}`, "t.go": "package testkit"}, "v0.3.0")
	useGitHub(t, gh)
	out := project(t, `{"name":"app","version":"1.0.0"}`)

	if err := Install([]string{"github:o/strutil.go"}); err != nil {
		t.Fatal(err)
	}
	if err := Add([]string{"-D", "github:o/testkit.go"}); err != nil {
		t.Fatal(err)
	}
	m := load(t)
	if m.Dependencies["strutil"] != "github:o/strutil.go#^1.0.0" || m.DevDependencies["testkit"] != "github:o/testkit.go#^0.3" {
		t.Fatalf("%v %v", m.Dependencies, m.DevDependencies)
	}
	if !strings.Contains(out.String(), "2 packages installed") {
		t.Fatalf("out: %s", out)
	}
	for name, fn := range map[string]func([]string) error{"install": Install, "update": Update, "ci": CI} {
		if err := fn(nil); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if err := Sync(nil); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if err := Sync([]string{"--force"}); err != nil {
		t.Fatal(err)
	}
	if err := Update([]string{"strutil"}); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall([]string{"testkit"}); err != nil || load(t).DevDependencies["testkit"] != "" {
		t.Fatalf("uninstall: %v", err)
	}
	for name, err := range map[string]error{
		"install --dev":  Install([]string{"--dev"}),
		"bad flag":       Add([]string{"--nope"}),
		"ci args":        CI([]string{"x"}),
		"sync args":      Sync([]string{"--force", "x"}),
		"uninstall none": Uninstall(nil),
	} {
		if err == nil {
			t.Errorf("%s must fail", name)
		}
	}
}

func TestInit(t *testing.T) {
	project(t, "")
	Stdin = strings.NewReader("my-app\n2.0.0\nAnn\n\n\n")
	if err := Init(nil); err != nil {
		t.Fatal(err)
	}
	m := load(t)
	if m.Name != "my-app" || m.Version != "2.0.0" || string(m.Raw("author")) != `"Ann"` || m.Engines.Go != ">=1.22" ||
		m.Scripts["test"] != "go test ./..." || m.Raw("homepage") != nil {
		t.Fatalf("%+v", m)
	}
	if gi, _ := os.ReadFile(".gitignore"); string(gi) != "gtr_modules/\ngo.mod\ngo.work\n" {
		t.Fatalf(".gitignore: %q", gi)
	}
	if err := Init(nil); err == nil || !strings.Contains(err.Error(), "already initialized") {
		t.Fatalf("second init: %v", err)
	}
}

func TestInitExistingGoProject(t *testing.T) {
	out := project(t, "")
	os.WriteFile("go.mod", []byte("module legacy-app\n\ngo 1.23\n"), 0o644)
	if err := Init([]string{"-y"}); err != nil {
		t.Fatal(err)
	}
	if load(t).Name != "legacy-app" || !strings.Contains(out.String(), "run `gtr sync`") || strings.Contains(out.String(), "differs") {
		t.Fatalf("%q %s", load(t).Name, out)
	}
	out = project(t, "")
	os.WriteFile("go.mod", []byte("module github.com/me/app\n"), 0o644)
	if err := Init([]string{"app", "-y"}); err != nil || !strings.Contains(out.String(), `imports of the project's own packages must use "app/..."`) {
		t.Fatalf("%v %s", err, out)
	}
}

func TestInitYesAndEOF(t *testing.T) {
	out := project(t, "")
	if err := Init([]string{"named", "-y"}); err != nil {
		t.Fatal(err)
	}
	if m := load(t); m.Name != "named" || m.Version != "1.0.0" || strings.Contains(out.String(), "?") {
		t.Fatalf("-y: %+v, out %q", m, out)
	}
	project(t, "")
	Stdin = strings.NewReader("only-name") // no trailing newline, then EOF
	if err := Init(nil); err != nil {
		t.Fatal(err)
	}
	if m := load(t); m.Name != "only-name" || m.Version != "1.0.0" {
		t.Fatalf("EOF: %+v", m)
	}
	project(t, "")
	if err := Init([]string{"Bad Name", "-y"}); err == nil || !strings.Contains(err.Error(), "import path") {
		t.Fatalf("invalid name: %v", err)
	}
	t.Setenv("GTR_GO_VERSION", "1.27.2")
	if err := Init([]string{"-y"}); err != nil || load(t).Engines.Go != ">=1.27" {
		t.Fatalf("engines from GTR_GO_VERSION: %v %+v", err, load(t).Engines)
	}
	if got := suggestName("My Project_2"); got != "my-project-2" {
		t.Fatalf("suggestName: %q", got)
	}
	if suggestName("__") != "app" || suggestName("9lives") != "lives" {
		t.Fatal("suggestName edge cases")
	}
	project(t, "")
	if err := Init([]string{"--bogus"}); err == nil {
		t.Fatal("unknown flag")
	}
	if err := Init([]string{"a", "b"}); err == nil {
		t.Fatal("too many args")
	}
}

func TestNew(t *testing.T) {
	project(t, "")
	if err := New([]string{"proj", "-y"}); err != nil {
		t.Fatal(err)
	}
	if m, err := manifest.Load(filepath.Join("proj", manifest.FileName)); err != nil || m.Name != "proj" {
		t.Fatalf("%+v %v", m, err)
	}
	if wd, _ := os.Getwd(); filepath.Base(wd) == "proj" {
		t.Fatal("New must return to the original directory")
	}
	for args, want := range map[string]string{
		"":                 "project name is required",
		"proj":             "not empty",
		"x --repo a/b":     "not implemented",
		"a b":              "unexpected argument",
		"proj/gtr.json -y": "not a directory",
	} {
		if err := New(strings.Fields(args)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("new %q: %v, want %q", args, err, want)
		}
	}
	// Init failed in the newly created directory, so the directory is removed.
	if err := New([]string{"fresh", "--bad"}); err == nil {
		t.Fatal("want error")
	}
	if _, err := os.Stat("fresh"); !os.IsNotExist(err) {
		t.Fatal("dir created by failed new must be removed")
	}
}

func shell(script string) string {
	if runtime.GOOS == "windows" {
		return strings.ReplaceAll(script, "exit ", "exit /b ")
	}
	return script
}

func TestRun(t *testing.T) {
	out := project(t, `{"name":"app","scripts":{"start":"echo started","fail":"`+shell("exit 3")+`","args":"echo","empty":""}}`)
	if err := Run(nil); err != nil || !strings.Contains(out.String(), "started") {
		t.Fatalf("start: %v %q", err, out)
	}
	out.Reset()
	if err := Run([]string{"args", "--", "a b", "c"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "a b") || !strings.Contains(out.String(), " c") { // cmd.exe prints the quotes
		t.Fatalf("args: %q", out)
	}
	var exit *exec.ExitError
	if err := Run([]string{"fail"}); !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("exit code: %v", err)
	}
	err := Run([]string{"missing"})
	if err == nil || !strings.Contains(err.Error(), `script "missing" is not defined`) || !strings.Contains(err.Error(), "available: args, fail, start") {
		t.Fatalf("missing: %v", err)
	}
	if err := Run([]string{"empty"}); err == nil {
		t.Fatal("empty script must be an error")
	}
}

func TestQuote(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	for in, want := range map[string]string{"plain": "plain", "a b": "'a b'", "it's": `'it'\''s'`, "": "''", "$HOME": "'$HOME'"} {
		if got := quote(in); got != want {
			t.Errorf("quote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestHelp(t *testing.T) {
	var b bytes.Buffer
	Help(&b, "1.0")
	if !strings.Contains(b.String(), "gtr-manager 1.0") || !strings.Contains(b.String(), "GITHUB_TOKEN") {
		t.Fatal(b.String())
	}
}

func TestRunGoEnvAndHints(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOFLAGS", "")
	out := project(t, `{"name":"app","scripts":{"env":"go env GOPROXY GOWORK GOFLAGS","build":"go build ./..."}}`)
	os.WriteFile("main.go", []byte("package main\n\nimport \"orm\"\n\nfunc main() { orm.Open() }\n"), 0o644)
	os.WriteFile("go.mod", []byte("module app\n\ngo 1.22\n"), 0o644)
	if err := Run([]string{"env"}); err != nil || strings.Contains(out.String(), "off\noff") {
		t.Fatalf("a foreign go.mod gets no gtr environment: %v\n%s", err, out)
	}
	out.Reset()
	os.WriteFile("go.mod", []byte("// generated by gtr from gtr.json — edit gtr.json or run `gtr sync`\n// gtr:hash sha256:x sem:y\nmodule app\n\ngo 1.22\n"), 0o644)
	if err := Run([]string{"env"}); err != nil || !strings.Contains(out.String(), "off\noff\n-mod=mod\n") {
		t.Fatalf("env: %v\n%s", err, out)
	}
	out.Reset()
	if err := Run([]string{"build"}); err == nil || !strings.Contains(out.String(), `gtr: package "orm" is not installed`) {
		t.Fatalf("hint: %v\n%s", err, out)
	}
}

func TestWorkspaceCommands(t *testing.T) {
	gh := githubtest.New(t)
	gh.Commit("o/strutil.go", map[string]string{"gtr.json": `{"name":"strutil","version":"0"}`, "s.go": "package strutil"}, "v1.0.0")
	useGitHub(t, gh)
	out := project(t, `{"name":"ws","private":true,"workspaces":["libs/*"],"scripts":{"env":"go env GOWORK GOFLAGS"}}`)
	root, _ := os.Getwd()
	for dir, body := range map[string]string{
		"libs/alpha/gtr.json": `{"name":"alpha","version":"0.1.0","scripts":{"hello":"echo hi-alpha"}}`,
		"libs/beta/gtr.json":  `{"name":"beta","version":"0.1.0","scripts":{"env":"go env GOWORK GOFLAGS","hello":"echo hi-beta"}}`,
		"libs/aaa/gtr.json":   `{"name":"gamma","version":"0.1.0","dependencies":{"beta":"workspace:*"},"scripts":{"hello":"echo hi-gamma"}}`,
	} {
		os.MkdirAll(filepath.Dir(dir), 0o755)
		os.WriteFile(dir, []byte(body), 0o644)
	}
	t.Chdir(filepath.Join(root, "libs", "beta"))
	if err := Add([]string{"alpha", "github:o/strutil.go"}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if m := load(t); m.Dependencies["alpha"] != "workspace:*" || m.Dependencies["strutil"] == "" {
		t.Fatalf("member gtr.json: %v", m.Dependencies)
	}
	for _, f := range []string{"go.work", "gtr.lock", "gtr_modules/strutil/s.go"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err != nil {
			t.Fatalf("%s at the workspace root: %v", f, err)
		}
	}
	if _, err := os.Stat("gtr.lock"); !os.IsNotExist(err) {
		t.Fatal("a member gets no gtr.lock")
	}
	// run -r: every member with the script, dependencies first.
	out.Reset()
	if err := Run([]string{"-r", "hello"}); err != nil {
		t.Fatal(err)
	}
	a, b, g := strings.Index(out.String(), "hi-alpha\n"), strings.Index(out.String(), "hi-beta\n"), strings.Index(out.String(), "hi-gamma\n")
	if a < 0 || b < a || g < b || !strings.Contains(out.String(), "[libs/aaa] > echo hi-gamma") {
		t.Fatalf("run -r order:\n%s", out)
	}
	if err := Run([]string{"-r", "nothing"}); err == nil || !strings.Contains(err.Error(), "no workspace member defines") {
		t.Fatalf("run -r missing: %v", err)
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOFLAGS", "")
	out.Reset()
	if err := Run([]string{"env"}); err != nil || !strings.Contains(out.String(), filepath.Join(root, "go.work")+"\n\n") {
		t.Fatalf("env in a member: %v\n%s", err, out)
	}
}
