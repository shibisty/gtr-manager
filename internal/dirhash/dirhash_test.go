package dirhash

import (
	"os"
	"path/filepath"
	"testing"
)

// The expected value was computed with golang.org/x/mod/sumdb/dirhash.HashDir
// semantics: sorted "<sha256>  <prefix>/<path>\n" lines.
func TestHashDir(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "sub", "a.go"), []byte("package a\n"), 0o644)
	h, err := HashDir(dir, "m@v1.0.0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if h != expected(t) {
		t.Fatalf("got %s want %s", h, expected(t))
	}
	again, _ := HashDir(dir, "m@v1.0.0", nil)
	if again != h {
		t.Fatal("not deterministic")
	}
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644)
	if skipped, _ := HashDir(dir, "m@v1.0.0", func(rel string) bool { return rel == "go.mod" }); skipped != h {
		t.Fatal("skip must exclude go.mod")
	}
	if withMod, _ := HashDir(dir, "m@v1.0.0", nil); withMod == h {
		t.Fatal("a new file must change the hash")
	}
	if Short(h) == "00000000" || len(Short(h)) != 8 || Short("bad") != "00000000" {
		t.Fatal("Short")
	}
	if _, err := HashDir(filepath.Join(dir, "missing"), "x", nil); err == nil {
		t.Fatal("missing dir")
	}
}

func expected(t *testing.T) string {
	// sha256("package a\n") and sha256("package b\n"), lines sorted by name.
	return referenceHash(map[string]string{"m@v1.0.0/b.go": "package b\n", "m@v1.0.0/sub/a.go": "package a\n"})
}
