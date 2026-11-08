package commands

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestParseDotenv(t *testing.T) {
	env := map[string]string{"HOME": "/home/x"}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	got, err := parseDotenv("\ufeff# comment\n"+
		"A=1\r\n"+
		"export B = two words  # trailing comment\n"+
		"C=\"line1\\nline2 ${A} $HOME \\$A \\\"q\\\"\"\n"+
		"D='raw $A \\n'\n"+
		"E=\"multi\nline\" # ok\n"+
		"F=\n"+
		"G=a#b\n"+
		"H=${MISSING}x\n"+
		"I.J=dotted\n", lookup)
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]string{
		{"A", "1"}, {"B", "two words"}, {"C", "line1\nline2 1 /home/x $A \"q\""}, {"D", `raw $A \n`},
		{"E", "multi\nline"}, {"F", ""}, {"G", "a#b"}, {"H", "x"}, {"I.J", "dotted"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	for _, bad := range []string{"novalue", "1A=x", "A=\"open", "A=\"x\" junk", "=x"} {
		if _, err := parseDotenv(bad, lookup); err == nil || !strings.Contains(err.Error(), "line 1") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestLoadDotenvAndDefaults(t *testing.T) {
	root, member := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(root, ".env"), []byte("DB=root\nNAME=root\nURL=db://${DB}\n"), 0o644)
	os.WriteFile(filepath.Join(root, ".env.local"), []byte("DB=local\n"), 0o644)
	os.WriteFile(filepath.Join(member, ".env"), []byte("NAME=member\nREF=${DB}\n"), 0o644)
	got, err := loadDotenv(root, member)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"DB=local", "NAME=member", "URL=db://root", "REF=local"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	merged := withDefaults([]string{"NAME=shell", "PATH=/bin"}, got)
	if !reflect.DeepEqual(merged, []string{"NAME=shell", "PATH=/bin", "DB=local", "URL=db://root", "REF=local"}) {
		t.Fatalf("merged %q", merged)
	}
	os.WriteFile(filepath.Join(member, ".env.local"), []byte("bad line\n"), 0o644)
	if _, err := loadDotenv(member); err == nil || !strings.Contains(err.Error(), ".env.local") {
		t.Fatalf("bad file: %v", err)
	}
}

func TestRunHooksAndDotenv(t *testing.T) {
	echoVar := "echo value=$GREETING"
	if runtime.GOOS == "windows" {
		echoVar = "echo value=%GREETING%"
	}
	out := project(t, `{"name":"app","scripts":{
		"pretest":"echo before","test":"echo testing","posttest":"echo after",
		"prebroken":"`+shell("exit 4")+`","broken":"echo never",
		"show":"`+echoVar+`"}}`)
	if err := Run([]string{"test", "--", "x"}); err != nil {
		t.Fatal(err)
	}
	o := out.String()
	b, m, a := strings.Index(o, "before"), strings.Index(o, "testing x"), strings.Index(o, "after")
	if b < 0 || m < b || a < m || strings.Contains(o, "before x") {
		t.Fatalf("hooks order or args:\n%s", o)
	}
	var exit *exec.ExitError
	out.Reset()
	if err := Run([]string{"broken"}); !errors.As(err, &exit) || !strings.Contains(err.Error(), "prebroken") || strings.Contains(out.String(), "never") {
		t.Fatalf("failing pre hook: %v\n%s", err, out)
	}

	os.WriteFile(".env", []byte("GREETING=hello\n"), 0o644)
	os.Mkdir("sub", 0o755)
	t.Chdir("sub") // the nearest gtr.json is in the parent; the script runs there
	out.Reset()
	if err := Run([]string{"show"}); err != nil || !strings.Contains(out.String(), "value=hello") {
		t.Fatalf(".env: %v\n%s", err, out)
	}
	t.Setenv("GREETING", "from-shell")
	out.Reset()
	if err := Run([]string{"show"}); err != nil || !strings.Contains(out.String(), "value=from-shell") {
		t.Fatalf("the environment wins: %v\n%s", err, out)
	}
	os.Unsetenv("GREETING")
	out.Reset()
	if err := Run([]string{"--no-env", "show"}); err != nil || strings.Contains(out.String(), "value=hello") {
		t.Fatalf("--no-env: %v\n%s", err, out)
	}
	t.Chdir(t.TempDir())
	if err := Run([]string{"show"}); err == nil || !strings.Contains(err.Error(), "no gtr.json") {
		t.Fatalf("outside a project: %v", err)
	}
}

func TestDotenvEnvironmentWinsInExpansion(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env"), []byte("HOST=localhost\nURL=http://${HOST}/x\n"), 0o644)
	t.Setenv("HOST", "prod")
	got, err := loadDotenv(dir)
	if err != nil || !reflect.DeepEqual(got, []string{"HOST=localhost", "URL=http://prod/x"}) {
		t.Fatalf("%q %v", got, err)
	}
	// HOST=localhost is dropped by withDefaults, so URL agrees with HOST.
}

func TestInitRejectsBadVersion(t *testing.T) {
	project(t, "")
	Stdin = strings.NewReader("app\n1.0\n")
	if err := Init(nil); err == nil || !strings.Contains(err.Error(), "SemVer") {
		t.Fatalf("version 1.0: %v", err)
	}
	if err := Update([]string{"--bogus"}); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("update --bogus: %v", err)
	}
	if err := Uninstall([]string{"--nope"}); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("uninstall --nope: %v", err)
	}
}
