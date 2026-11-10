// Package templates holds the project templates of `gtr new --template`.
package templates

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
)

//go:embed all:files
var files embed.FS

// Default is the template of `gtr new` without --template.
const Default = "app"

// Data fills the templates.
type Data struct {
	Name    string // package name = import path ("orm-mysql")
	Package string // Go package identifier ("mysql")
	Version string // initial version
	EnvName string // for environment variables ("MYSQL")
}

// Names lists the templates.
func Names() []string {
	entries, _ := fs.ReadDir(files, "files")
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// Description of each template, for help and errors.
var Description = map[string]string{
	"app":        "an application: main.go with a test",
	"cli":        "a command-line tool: flags, version, a testable run()",
	"lib":        "a library package with a test and an example",
	"orm-driver": "an orm driver on database/sql (peer orm ^0.1, drivertest)",
	"fullstack":  "a fullstack web application: SQLite, users, login, migrations, views, tests",
}

// NewData derives the template data from a package name.
func NewData(tmpl, name, version string) Data {
	ident := strings.ReplaceAll(name, "-", "")
	if tmpl == "orm-driver" {
		ident = strings.ReplaceAll(strings.TrimPrefix(name, "orm-"), "-", "")
	}
	if tmpl == "app" || tmpl == "cli" || tmpl == "fullstack" {
		ident = "main"
	}
	return Data{Name: name, Package: ident, Version: version, EnvName: strings.ToUpper(strings.ReplaceAll(strings.TrimPrefix(name, "orm-"), "-", "_"))}
}

// Fields returns the gtr.json fields template tmpl sets, without writing
// anything (and checks that the template exists).
func Fields(tmpl string, data Data) (map[string]json.RawMessage, error) {
	root := path.Join("files", tmpl)
	if _, err := fs.Stat(files, root); err != nil || tmpl == "" {
		return nil, fmt.Errorf("unknown template %q; available: %s", tmpl, strings.Join(Names(), ", "))
	}
	src, err := fs.ReadFile(files, root+"/gtr.json.tmpl")
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := template.Must(template.New("gtr.json").Option("missingkey=error").Parse(string(src))).Execute(&out, data); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	return fields, json.Unmarshal(out.Bytes(), &fields)
}

// Write renders template tmpl into dir (which must not contain the files
// yet) and returns the gtr.json fields the template sets.
func Write(tmpl, dir string, data Data) (map[string]json.RawMessage, error) {
	root := path.Join("files", tmpl)
	if _, err := fs.Stat(files, root); err != nil {
		return nil, fmt.Errorf("unknown template %q; available: %s", tmpl, strings.Join(Names(), ", "))
	}
	var fields map[string]json.RawMessage
	err := fs.WalkDir(files, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		// Files ending in .tmpl are text/templates; others (HTML views with
		// their own {{...}}) are copied as they are.
		rel, isTmpl := strings.CutSuffix(strings.TrimPrefix(p, root+"/"), ".tmpl")
		rel = strings.ReplaceAll(rel, "__pkg__", data.Package)
		rel = strings.ReplaceAll(rel, "__dot__", ".") // .env.example, .gitignore: dotfiles are not embedded
		src, err := fs.ReadFile(files, p)
		if err != nil {
			return err
		}
		content := src
		if isTmpl {
			t, err := template.New(rel).Option("missingkey=error").Parse(string(src))
			if err != nil {
				return err
			}
			var out bytes.Buffer
			if err := t.Execute(&out, data); err != nil {
				return err
			}
			content = out.Bytes()
		}
		if rel == "gtr.json" {
			return json.Unmarshal(content, &fields)
		}
		if strings.HasSuffix(rel, ".go") {
			// Names of different lengths change gofmt's alignment.
			if formatted, err := format.Source(content); err == nil {
				content = formatted
			}
		}
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if _, err := os.Stat(dst); err == nil {
			return fmt.Errorf("%s already exists", dst)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, content, 0o644)
	})
	return fields, err
}
