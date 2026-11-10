package templates

import (
	"bytes"
	"encoding/json"
	"go/format"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

func TestTemplatesRender(t *testing.T) {
	if got := strings.Join(Names(), " "); got != "app cli fullstack lib orm-driver" {
		t.Fatalf("names: %s", got)
	}
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			if Description[name] == "" {
				t.Error("no description")
			}
			pkg := "my-thing"
			if name == "orm-driver" {
				pkg = "orm-cockroach"
			}
			dir := t.TempDir()
			fields, err := Write(name, dir, NewData(name, pkg, "0.1.0"))
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := fields["scripts"]; !ok {
				t.Error("no scripts")
			}
			goFiles := 0
			filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return err
				}
				data, _ := os.ReadFile(p)
				// HTML views keep their own {{...}}; everything else is rendered.
				if !strings.HasSuffix(p, ".html") && bytes.Contains(data, []byte("{{")) || strings.Contains(p, "__pkg__") || strings.Contains(p, "__dot__") {
					t.Errorf("%s: unrendered template", p)
				}
				if strings.HasSuffix(p, ".go") {
					goFiles++
					formatted, err := format.Source(data)
					if err != nil {
						t.Errorf("%s: %v", p, err)
					} else if !bytes.Equal(formatted, data) {
						t.Errorf("%s is not gofmt-formatted", p)
					}
				}
				return nil
			})
			if goFiles < 2 {
				t.Errorf("%d Go files", goFiles)
			}
			if _, err := Write(name, dir, NewData(name, pkg, "0.1.0")); err == nil || !strings.Contains(err.Error(), "already exists") {
				t.Errorf("overwrite: %v", err)
			}
		})
	}
	if _, err := Write("nope", t.TempDir(), Data{}); err == nil || !strings.Contains(err.Error(), "available: app, cli, fullstack, lib, orm-driver") {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := Fields("", Data{}); err == nil {
		t.Fatal("empty template name")
	}
}

func TestNewData(t *testing.T) {
	for _, c := range []struct{ tmpl, name, pkg, env string }{
		{"lib", "str-util", "strutil", "STR_UTIL"},
		{"orm-driver", "orm-cockroach-db", "cockroachdb", "COCKROACH_DB"},
		{"orm-driver", "pgx", "pgx", "PGX"},
		{"app", "blog", "main", "BLOG"},
		{"fullstack", "my-site", "main", "MY_SITE"},
	} {
		d := NewData(c.tmpl, c.name, "1.0.0")
		if d.Package != c.pkg || d.EnvName != c.env || d.Name != c.name {
			t.Errorf("%+v: %+v", c, d)
		}
	}
	f, _ := Fields("orm-driver", NewData("orm-driver", "orm-x", "0.1.0"))
	var peers map[string]string
	json.Unmarshal(f["peerDependencies"], &peers)
	if peers["orm"] != "^0.1" {
		t.Fatalf("peers: %v", peers)
	}
}

func TestFullstackTemplate(t *testing.T) {
	dir := t.TempDir()
	if _, err := Write("fullstack", dir, NewData("fullstack", "my-site", "0.1.0")); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]string{
		"main.go":                           `"my-site/bootstrap"`,
		"bootstrap/app.go":                  `"my-site/database/migrations"`,
		"resources/views/pages/login.html":  `{{.CSRFField}}`, // copied as is
		".env.example":                      "APP_NAME=my-site",
		".gitignore":                        "gtr_modules/",
		"storage/.gitkeep":                  "",
		"tests/app_test.go":                 `"APP_NAME": "my-site"`,
		"database/migrations/migrations.go": "func register(",
	} {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file)))
		if err != nil {
			t.Errorf("%s: %v", file, err)
			continue
		}
		if !strings.Contains(string(data), want) {
			t.Errorf("%s lacks %q", file, want)
		}
	}
}

// Go files of a template must end in .tmpl: a plain .go file there would be
// a package of gtr-manager itself, which go build ./... and go test ./...
// try to compile (with the template's imports).
func TestNoPlainGoFiles(t *testing.T) {
	fs.WalkDir(files, "files", func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".go") {
			t.Errorf("%s: name it %s.tmpl", p, path.Base(p))
		}
		return err
	})
}
