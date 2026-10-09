package lock

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	l, err := Load(path)
	if err != nil || len(l.Packages) != 0 {
		t.Fatal(l, err)
	}
	l.Packages["zz"] = Entry{Version: "1.0.0", Source: "github:o/zz"}
	l.Packages["aa"] = Entry{Version: "0.1.0", Source: "file:../aa", Dependencies: map[string]string{"zz": "github:o/zz#^1"}}
	data, _ := Marshal(l)
	if !strings.HasPrefix(string(data), "{\n  \"lockfileVersion\": 1,") || strings.Index(string(data), `"aa"`) > strings.Index(string(data), `"zz"`) {
		t.Fatalf("%s", data)
	}
	os.WriteFile(path, data, 0o644)
	got, err := Load(path)
	if err != nil || got.Packages["aa"].Dependencies["zz"] != "github:o/zz#^1" {
		t.Fatal(got, err)
	}
	os.WriteFile(path, []byte(`{"lockfileVersion":99}`), 0o644)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "newer gtr") {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte(`{`), 0o644)
	if _, err := Load(path); err == nil {
		t.Fatal("broken lock")
	}
}
