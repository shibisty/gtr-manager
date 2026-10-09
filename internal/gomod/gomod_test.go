package gomod

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderAndCheck(t *testing.T) {
	p := Project{Module: "app", Go: "1.22",
		Direct:   []Require{{"orm-mysql", "v0.1.0"}, {"orm", "v0.1.0"}},
		Indirect: []Require{{"strutil", "v1.0.0"}},
		Replace:  map[string]string{"orm": "./gtr_modules/orm", "strutil": "./gtr_modules/strutil", "orm-mysql": "./gtr_modules/orm-mysql"}}
	out := string(p.Render())
	want := "module app\n\ngo 1.22\n\nrequire (\n\torm v0.1.0\n\torm-mysql v0.1.0\n)\n\nrequire strutil v1.0.0 // indirect\n\n" +
		"replace orm => ./gtr_modules/orm\n\nreplace orm-mysql => ./gtr_modules/orm-mysql\n\nreplace strutil => ./gtr_modules/strutil\n"
	if !strings.HasSuffix(out, want) || !strings.HasPrefix(out, headerLine+"\n"+hashPrefix) {
		t.Fatalf("got:\n%s", out)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "go.mod")
	if st, _ := Check(path); st != Missing {
		t.Fatal("missing")
	}
	os.WriteFile(path, []byte(out), 0o644)
	if st, _ := Check(path); st != Generated {
		t.Fatal("generated")
	}
	os.WriteFile(path, []byte(strings.Replace(out, "v1.0.0", "v1.0.1", 1)), 0o644)
	if st, _ := Check(path); st != Edited {
		t.Fatal("edited")
	}
	os.WriteFile(path, []byte("module x\n"), 0o644)
	if st, _ := Check(path); st != Foreign {
		t.Fatal("foreign")
	}
	one := Project{Module: "m", Go: "1.22", Direct: []Require{{"a", "v1.0.0"}}}
	if !strings.Contains(string(one.Render()), "\nrequire a v1.0.0\n") {
		t.Fatal("single require")
	}
}

func TestPackage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "go.mod")
	os.WriteFile(path, Package("orm-mysql", "1.22", []Require{{"strutil", "v0.0.0-0"}, {"orm", "v0.0.0-0"}}), 0o644)
	if !IsPackageGenerated(path) {
		t.Fatal("IsPackageGenerated")
	}
	if m, err := ModulePath(path); err != nil || m != "orm-mysql" {
		t.Fatal(m, err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "\torm v0.0.0-0\n\tstrutil v0.0.0-0\n") {
		t.Fatalf("%s", data)
	}
	os.WriteFile(path, []byte("module \"quoted/x\"\n"), 0o644)
	if m, _ := ModulePath(path); m != "quoted/x" || IsPackageGenerated(path) {
		t.Fatal(m)
	}
	os.WriteFile(path, []byte("go 1.22\n"), 0o644)
	if _, err := ModulePath(path); err == nil {
		t.Fatal("no module line")
	}
	for in, want := range map[string]string{">=1.22": "1.22", "^1.26.3": "1.26", "~1.24": "1.24", "": "1.22", "*": "1.22"} {
		if got := GoDirective(in, "1.22"); got != want {
			t.Errorf("GoDirective(%q) = %q", in, got)
		}
	}
}

func TestGoBookkeepingIsNotAnEdit(t *testing.T) {
	p := Project{Module: "app", Go: "1.22", Direct: []Require{{"orm", "v0.1.0"}}, Indirect: []Require{{"strutil", "v1.0.0"}},
		Replace: map[string]string{"orm": "./gtr_modules/orm", "strutil": "./gtr_modules/strutil"}}
	out := string(p.Render())
	path := filepath.Join(t.TempDir(), "go.mod")
	// go moved strutil to the direct block and added a toolchain line.
	moved := strings.Replace(out, "require orm v0.1.0\n\nrequire strutil v1.0.0 // indirect", "require (\n\torm v0.1.0\n\tstrutil v1.0.0\n)\n\ntoolchain go1.24.7", 1)
	if moved == out {
		t.Fatal("test setup")
	}
	os.WriteFile(path, []byte(moved), 0o644)
	if st, _ := Check(path); st != Generated {
		t.Fatalf("go bookkeeping treated as an edit:\n%s", moved)
	}
	// A real change in versions is an edit.
	os.WriteFile(path, []byte(strings.Replace(out, "strutil v1.0.0", "strutil v1.0.1", 1)), 0o644)
	if st, _ := Check(path); st != Edited {
		t.Fatal("version change must be an edit")
	}
	os.WriteFile(path, Package("lib", "1.22", nil), 0o644)
	if st, _ := Check(path); st != PackageMod {
		t.Fatal("package go.mod")
	}
	if MaxGo("1.22", "1.23") != "1.23" || MaxGo("1.24", "1.9") != "1.24" || MaxGo("2.0", "1.30") != "2.0" {
		t.Fatal("MaxGo")
	}
	order := []string{"1.21.9", "1.22", "1.22beta1", "1.22rc1", "1.22rc2", "1.22.0", "1.22.1", "1.23"}
	for i := 1; i < len(order); i++ {
		if CompareGo(order[i-1], order[i]) >= 0 {
			t.Errorf("%s must sort before %s", order[i-1], order[i])
		}
	}
	if MaxGo("1.22", "1.22.0") != "1.22.0" {
		t.Fatal("1.22 < 1.22.0")
	}
}

func TestParse(t *testing.T) {
	f := Parse([]byte("// header\r\nmodule app\n\ngo 1.22.0\n\nrequire (\n\torm v0.1.0\n\tgithub.com/a/b v1.2.3 // indirect\n)\nrequire c.io/d v0.1.0\n\nreplace orm => ../orm.go\nreplace (\n\tx.io/y v1.0.0 => example.com/fork v1.0.1\n)\n"))
	if f.Module != "app" || f.Go != "1.22.0" || len(f.Require) != 3 || !f.Require[1].Indirect || f.Require[2].Path != "c.io/d" {
		t.Fatalf("%+v", f)
	}
	if f.Replace["orm"] != "../orm.go" || f.Replace["x.io/y"] != "example.com/fork v1.0.1" {
		t.Fatalf("%v", f.Replace)
	}
	f = Parse([]byte("module app\ntool golang.org/x/tools/cmd/stringer\nexclude (\n\ta.io/b v1.0.0\n)\ngodebug default=go1.21\nretract v1.0.0\ntoolchain go1.24.1\n"))
	if strings.Join(f.Unsupported, "|") != "tool golang.org/x/tools/cmd/stringer|exclude a.io/b v1.0.0|godebug default=go1.21" {
		t.Fatalf("unsupported: %q", f.Unsupported)
	}
	for target, want := range map[string][2]string{`"../my lib"`: {"../my lib", ""}, "../lib": {"../lib", ""}, "example.com/fork v1.0.1": {"example.com/fork", "v1.0.1"}} {
		if p, v := ReplaceTarget(target); p != want[0] || v != want[1] {
			t.Errorf("%s → %q %q", target, p, v)
		}
	}
}

func TestWork(t *testing.T) {
	w := Work{Go: "1.23", Use: []string{"./core", ".", "./drivers/mysql"}, Replace: map[string]string{"strutil": "./gtr_modules/strutil"}}
	data := w.Render()
	want := "go 1.23\n\nuse (\n\t.\n\t./core\n\t./drivers/mysql\n)\n\nreplace (\n\tstrutil => ./gtr_modules/strutil\n)\n"
	if !strings.HasSuffix(string(data), want) || !strings.HasPrefix(string(data), headerLine+"\n") {
		t.Fatalf("%s", data)
	}
	path := filepath.Join(t.TempDir(), "go.work")
	os.WriteFile(path, data, 0o644)
	if st, _ := Check(path); st != Generated {
		t.Fatal(st)
	}
	os.WriteFile(path, []byte(strings.Replace(string(data), "\t./core\n", "", 1)), 0o644)
	if st, _ := Check(path); st != Edited {
		t.Fatal(st)
	}
}
