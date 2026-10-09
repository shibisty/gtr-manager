package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTripKeepsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, FileName)
	os.WriteFile(p, []byte(`{"name":"app","description":"My café <app>","license":"MIT","private":true,
		"dependencies":{"github:a/b":"*"},"build":{"entry":"./cmd/app","targets":["linux/amd64"]}}`), 0o644)
	m, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "app" || m.Dependencies["github:a/b"] != "*" || m.Scripts == nil {
		t.Fatalf("loaded %+v", m)
	}
	m.Dependencies["github:c/d"] = "1.0.0"
	if err := Save(p, m); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	want := `{
    "name": "app",
    "version": "",
    "author": "",
    "website": "",
    "entrypoint": "",
    "engine": "",
    "dependencies": {
        "github:a/b": "*",
        "github:c/d": "1.0.0"
    },
    "scripts": {},
    "build": {
        "entry": "./cmd/app",
        "targets": [
            "linux/amd64"
        ]
    },
    "description": "My café <app>",
    "license": "MIT",
    "private": true
}
`
	if string(got) != want {
		t.Fatalf("saved:\n%s\nwant:\n%s", got, want)
	}
	again, err := Load(p)
	if err != nil || len(again.Extra) != 4 {
		t.Fatalf("reload: %v %v", again.Extra, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(filepath.Join(dir, FileName)); err == nil || !strings.Contains(err.Error(), "gtr-manager init") {
		t.Fatalf("missing: %v", err)
	}
	p := filepath.Join(dir, "bad.json")
	os.WriteFile(p, []byte(`{"name": 1}`), 0o644)
	if _, err := Load(p); err == nil {
		t.Fatal("wrong type must fail")
	}
	os.WriteFile(p, []byte(`{`), 0o644)
	if _, err := Load(p); err == nil {
		t.Fatal("broken JSON must fail")
	}
	if err := Save(filepath.Join(dir, "no", "such", "dir", FileName), &Manifest{}); err == nil {
		t.Fatal("save into missing dir must fail")
	}
}
