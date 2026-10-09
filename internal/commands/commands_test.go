package commands

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gtr-manager/internal/dependency"
	"gtr-manager/internal/manifest"
	"gtr-manager/internal/repositories"
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

type fakeSource struct {
	err  error
	deps []string
}

func (f *fakeSource) Install(_ context.Context, d *dependency.Dependency, p string) (string, error) {
	f.deps = append(f.deps, d.String())
	if f.err != nil {
		return "", f.err
	}
	dir := repositories.PackageDir(p, d)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x"), 0o644)
	return "main", nil
}

func useSource(t *testing.T, f *fakeSource) {
	old := Sources
	Sources = map[string]func() (Installer, error){"github": func() (Installer, error) { return f, nil }}
	t.Cleanup(func() { Sources = old })
}

func load(t *testing.T) *manifest.Manifest {
	m, err := manifest.Load(manifest.FileName)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestInstallAndUninstall(t *testing.T) {
	out := project(t, `{"name":"app","license":"MIT"}`)
	src := &fakeSource{}
	useSource(t, src)

	if err := Install([]string{"github:owner/repo@1.2.0"}); err != nil {
		t.Fatal(err)
	}
	m := load(t)
	if m.Dependencies["github:owner/repo"] != "1.2.0" || string(m.Extra["license"]) != `"MIT"` {
		t.Fatalf("manifest: %+v", m)
	}
	if !strings.Contains(out.String(), "Installed github:owner/repo (main)") {
		t.Fatalf("out: %s", out)
	}
	// A repeat install gives a clear error without downloading.
	err := Install([]string{"github:owner/repo"})
	if err == nil || !strings.Contains(err.Error(), "already installed (version 1.2.0)") || len(src.deps) != 1 {
		t.Fatalf("duplicate: %v, %v", err, src.deps)
	}

	if err := Uninstall([]string{"github:owner/repo"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := load(t).Dependencies["github:owner/repo"]; ok {
		t.Fatal("dependency not removed")
	}
	if _, err := os.Stat("packages"); !os.IsNotExist(err) {
		t.Fatalf("empty package dirs must be removed: %v", err)
	}
	if err := Uninstall([]string{"github:owner/repo"}); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("second uninstall: %v", err)
	}
}

func TestInstallFailureKeepsManifest(t *testing.T) {
	project(t, `{"name":"app"}`)
	useSource(t, &fakeSource{err: errors.New("network down")})
	before, _ := os.ReadFile(manifest.FileName)
	if err := Install([]string{"github:owner/repo"}); err == nil || err.Error() != "network down" {
		t.Fatalf("got %v", err)
	}
	after, _ := os.ReadFile(manifest.FileName)
	if !bytes.Equal(before, after) {
		t.Fatal("gtr.json must not change when download fails")
	}
}

func TestInstallErrors(t *testing.T) {
	project(t, `{"name":"app"}`)
	useSource(t, &fakeSource{})
	for args, want := range map[string]string{
		"":                      "package name is required",
		"orm":                   "gtr packages are not supported yet",
		"gitlab:a/b":            "gitlab packages are not supported yet",
		"github:../x":           "invalid github repository",
		"github:a/b github:c/d": "one package at a time",
	} {
		var a []string
		if args != "" {
			a = strings.Fields(args)
		}
		if err := Install(a); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("install %q: %v, want %q", args, err, want)
		}
	}
	if len(load(t).Dependencies) != 0 {
		t.Fatal("failed installs must not touch gtr.json")
	}
	os.Remove(manifest.FileName)
	if err := Install([]string{"github:a/b"}); err == nil || !strings.Contains(err.Error(), "init") {
		t.Fatalf("no gtr.json: %v", err)
	}
	if err := Uninstall(nil); err == nil {
		t.Fatal("uninstall without name")
	}
	if err := Uninstall([]string{"github:../x"}); err == nil {
		t.Fatal("uninstall with bad name")
	}
}

// uninstall removes only its own directory; sibling packages stay.
func TestUninstallKeepsNeighbours(t *testing.T) {
	project(t, `{"name":"app","dependencies":{"github:owner/a":"*","github:owner/b":"*","legacy":"*"}}`)
	for _, p := range []string{"packages/github/owner/a", "packages/github/owner/b", "packages/legacy"} {
		os.MkdirAll(p, 0o755)
	}
	if err := Uninstall([]string{"github:owner/a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("packages/github/owner/b"); err != nil {
		t.Fatal("neighbour removed")
	}
	if err := Uninstall([]string{"legacy"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("packages/legacy"); !os.IsNotExist(err) {
		t.Fatal("gtr package dir not removed")
	}
}

func TestInit(t *testing.T) {
	project(t, "")
	Stdin = strings.NewReader("my-app\n2.0.0\nAnn\n\n\n")
	if err := Init(nil); err != nil {
		t.Fatal(err)
	}
	m := load(t)
	if m.Name != "my-app" || m.Version != "2.0.0" || m.Author != "Ann" || m.Entrypoint != "main.go" || m.Scripts["test"] != "go test ./..." {
		t.Fatalf("%+v", m)
	}
	if err := Init(nil); err == nil || !strings.Contains(err.Error(), "already initialized") {
		t.Fatalf("second init: %v", err)
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
