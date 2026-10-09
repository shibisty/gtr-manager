package link

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMakeAndRemove(t *testing.T) {
	target := t.TempDir()
	os.WriteFile(filepath.Join(target, "a.go"), []byte("package a"), 0o644)
	l := filepath.Join(t.TempDir(), "gtr_modules", "a")
	kind, err := Make(target, l)
	if err != nil || kind == "" {
		t.Fatal(kind, err)
	}
	if got, _ := os.ReadFile(filepath.Join(l, "a.go")); string(got) != "package a" {
		t.Fatal("link does not reach the target")
	}
	if kind != Copy && Target(l) != target {
		t.Fatalf("Target = %q", Target(l))
	}
	// Re-making replaces the link; removing never touches the target.
	if _, err := Make(target, l); err != nil {
		t.Fatal(err)
	}
	if err := Remove(l); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "a.go")); err != nil {
		t.Fatal("Remove deleted the target")
	}
	if err := Remove(l); err != nil {
		t.Fatal("removing a missing entry must succeed")
	}
}

func TestCopyAndForeignDirs(t *testing.T) {
	target := t.TempDir()
	os.MkdirAll(filepath.Join(target, "sub"), 0o755)
	os.WriteFile(filepath.Join(target, "sub", "x.go"), []byte("x"), 0o644)
	dst := filepath.Join(t.TempDir(), "copy")
	if err := copyTree(target, dst); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dst, copyMarker), nil, 0o644)
	if err := Remove(dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("a marked copy must be removed")
	}
	// A real directory without the marker is never deleted.
	user := filepath.Join(t.TempDir(), "mine")
	os.MkdirAll(user, 0o755)
	os.WriteFile(filepath.Join(user, "work.go"), []byte("precious"), 0o644)
	if err := Remove(user); err == nil {
		t.Fatal("a foreign directory must not be removed")
	}
	if _, err := Make(target, user); err == nil {
		t.Fatal("Make must not overwrite a foreign directory")
	}
	if got, _ := os.ReadFile(filepath.Join(user, "work.go")); string(got) != "precious" {
		t.Fatal("foreign directory was modified")
	}
}
