package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gtr-manager/internal/templates"
)

func templateList() string {
	var b strings.Builder
	b.WriteString("templates:")
	for _, n := range templates.Names() {
		fmt.Fprintf(&b, "\n    %-12s %s", n, templates.Description[n])
	}
	b.WriteString("\n    none         gtr.json only")
	return b.String()
}

// New creates a project directory (or reuses an existing empty one), runs
// Init in it and writes the files of a template (--template, default "app";
// "none" writes gtr.json only). Flags (-y) are passed through to Init.
func New(args []string) error {
	var dir string
	var flags []string
	tmpl := templates.Default
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--repo" || strings.HasPrefix(a, "--repo="):
			return fmt.Errorf("new --repo is not implemented yet (stage 3)")
		case a == "--template" || a == "-t":
			if i+1 >= len(args) {
				return fmt.Errorf("%s needs a template: %s", a, templateList())
			}
			i++
			tmpl = args[i]
		case strings.HasPrefix(a, "--template="):
			tmpl = strings.TrimPrefix(a, "--template=")
		case strings.HasPrefix(a, "-"):
			flags = append(flags, a)
		case dir == "":
			dir = a
		default:
			return fmt.Errorf("unexpected argument %q", a)
		}
	}
	if dir == "" {
		return fmt.Errorf("project name is required")
	}
	if tmpl == "none" {
		tmpl = ""
	} else if _, err := templates.Fields(tmpl, templates.NewData(tmpl, "x", "0.1.0")); err != nil {
		return fmt.Errorf("%w\n%s", err, templateList())
	}

	created := false
	if st, err := os.Stat(dir); err == nil {
		if !st.IsDir() {
			return fmt.Errorf("%q is not a directory", dir)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			return fmt.Errorf("directory %q is not empty", dir)
		}
	} else if os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		created = true
	} else {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := os.Chdir(dir); err != nil {
		return err
	}
	err = initProject(append([]string{filepath.Base(dir)}, flags...), tmpl)
	if cerr := os.Chdir(cwd); err == nil {
		err = cerr
	}
	if err != nil && created {
		os.RemoveAll(dir)
	}
	return err
}
