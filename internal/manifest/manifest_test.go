package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, FileName)
	os.WriteFile(p, []byte(`{"zzz":1,"scripts":{"test":"go test ./..."},"name":"app","description":"My café <app>",
		"dependencies":{"orm":"github:shibisty/orm.go#^0.1"},"build":{"entry":"./cmd/app"},"version":"1.0.0",
		"engines":{"go":">=1.22"},"license":"MIT"}`), 0o644)
	m, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "app" || m.Engines.Go != ">=1.22" || m.Dependencies["orm"] == "" || len(m.Warnings) != 0 {
		t.Fatalf("%+v", m)
	}
	m.DevDependencies["faker"] = "file:../faker.go"
	if err := Save(p, m); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	want := `{
    "name": "app",
    "version": "1.0.0",
    "description": "My café <app>",
    "license": "MIT",
    "engines": {
        "go": ">=1.22"
    },
    "dependencies": {
        "orm": "github:shibisty/orm.go#^0.1"
    },
    "devDependencies": {
        "faker": "file:../faker.go"
    },
    "scripts": {
        "test": "go test ./..."
    },
    "build": {
        "entry": "./cmd/app"
    },
    "zzz": 1
}
`
	if string(got) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("temp file left: %v", entries)
	}
}

func TestMigrateLegacy(t *testing.T) {
	m, err := Parse([]byte(`{"name":"old","version":"1.0.0","author":"","website":"https://x.dev","entrypoint":"cmd/app/main.go",
		"engine":">=1.26","dependencies":{"github:a/b":"*"},"scripts":{}}`), "gtr.json")
	if err != nil {
		t.Fatal(err)
	}
	if m.Engines.Go != ">=1.26" || string(m.Raw("homepage")) != `"https://x.dev"` || m.Raw("website") != nil ||
		m.Raw("entrypoint") != nil || m.Raw("author") != nil || len(m.Warnings) != 3 {
		t.Fatalf("%+v %v", m, m.Warnings)
	}
	out, _ := Marshal(m)
	for _, gone := range []string{"website", "entrypoint", `"engine"`, `"author"`} {
		if strings.Contains(string(out), gone) {
			t.Errorf("%s still written:\n%s", gone, out)
		}
	}
	// A default entrypoint is dropped silently.
	m, _ = Parse([]byte(`{"name":"x","version":"1.0.0","entrypoint":"main.go"}`), "gtr.json")
	if len(m.Warnings) != 0 {
		t.Fatal(m.Warnings)
	}
}

func TestErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(filepath.Join(dir, FileName)); err == nil || !strings.Contains(err.Error(), "gtr init") {
		t.Fatalf("missing: %v", err)
	}
	for _, bad := range []string{`{`, `{"name":1}`, `{"dependencies":[]}`} {
		if _, err := Parse([]byte(bad), "x"); err == nil {
			t.Errorf("%s must fail", bad)
		}
	}
	if err := Save(filepath.Join(dir, "no", FileName), &Manifest{}); err == nil {
		t.Fatal("missing dir")
	}
	m := &Manifest{}
	m.SetRaw("private", true)
	m.SetRaw("gone", nil)
	if string(m.Raw("private")) != "true" {
		t.Fatal("SetRaw")
	}
}
