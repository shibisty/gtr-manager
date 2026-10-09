package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// New creates a project directory (or reuses an existing empty one) and runs
// Init in it. Flags (-y) are passed through to Init.
func New(args []string) error {
	var dir string
	var flags []string
	for _, a := range args {
		switch {
		case a == "--repo" || strings.HasPrefix(a, "--repo="):
			return fmt.Errorf("new --repo is not implemented yet (stage 3)")
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
	err = Init(append([]string{filepath.Base(dir)}, flags...))
	if cerr := os.Chdir(cwd); err == nil {
		err = cerr
	}
	if err != nil && created {
		os.RemoveAll(dir)
	}
	return err
}
