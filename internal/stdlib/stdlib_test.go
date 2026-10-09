package stdlib

import (
	"os/exec"
	"strings"
	"testing"
)

func TestNames(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	dir := t.TempDir()
	n, err := Names(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"errors", "net", "crypto", "os", "testing", "iter"} {
		if name == "iter" && !n["slices"] {
			continue
		}
		if !n[name] {
			t.Errorf("%s missing", name)
		}
	}
	if n["orm"] || n["github.com"] {
		t.Fatal("not stdlib")
	}
	if err := Check("errors", dir); err == nil || !strings.Contains(err.Error(), "standard library") {
		t.Fatalf("%v", err)
	}
	if err := Check("orm", dir); err != nil {
		t.Fatal(err)
	}
	// Cached on disk per version and in memory.
	cache = map[string]map[string]bool{}
	if n2, _ := Names(dir); !n2["errors"] {
		t.Fatal("disk cache")
	}
	if len(parse([]byte("a/b\nc\n\n"))) != 2 {
		t.Fatal("parse")
	}
}
