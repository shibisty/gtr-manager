package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tree(t *testing.T, s *Store, content string) string {
	d, err := s.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "a.go"), []byte(content), 0o644)
	return d
}

func TestPut(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: filepath.Join(root, "store"), Tmp: filepath.Join(root, "tmp")}
	writeMod := func(d string) error { return os.WriteFile(filepath.Join(d, "go.mod"), []byte("module a\n"), 0o644) }
	dir, integrity, err := s.Put(tree(t, s, "package a"), "pkg", "1.0.0", "", writeMod)
	if err != nil || !strings.HasPrefix(integrity, "h1:") || !s.Has("pkg", "1.0.0", integrity) {
		t.Fatal(dir, integrity, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		t.Fatal("prepare not applied")
	}
	// The generated go.mod is excluded from the integrity hash.
	if again, _ := Integrity(dir, "pkg", "1.0.0"); again != integrity {
		t.Fatal("integrity must ignore the generated go.mod")
	}
	// The same tree again: the existing directory is reused, the temp removed.
	src := tree(t, s, "package a")
	dir2, _, err := s.Put(src, "pkg", "1.0.0", integrity, nil)
	if err != nil || dir2 != dir {
		t.Fatal(dir2, err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("temp tree not removed")
	}
	// A different tree with the lock's integrity is rejected.
	if _, _, err := s.Put(tree(t, s, "package evil"), "pkg", "1.0.0", integrity, nil); err == nil || !strings.Contains(err.Error(), "integrity mismatch") {
		t.Fatalf("mismatch: %v", err)
	}
	if s.Has("pkg", "1.0.0", "") {
		t.Fatal("Has with empty integrity")
	}
}
