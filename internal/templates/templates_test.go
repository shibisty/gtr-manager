package templates

import (
	"bytes"
	"encoding/json"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTemplatesRender(t *testing.T) {
	if got := strings.Join(Names(), " "); got != "app cli lib orm-driver" {
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
				if bytes.Contains(data, []byte("{{")) || strings.Contains(p, "__pkg__") {
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
	if _, err := Write("nope", t.TempDir(), Data{}); err == nil || !strings.Contains(err.Error(), "available: app, cli, lib, orm-driver") {
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
