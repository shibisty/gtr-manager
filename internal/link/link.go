// Package link creates gtr_modules/<name> entries pointing into the store
// (ADR-0001, item 4): a symlink on Linux/macOS, a junction on Windows (no
// administrator rights needed), or a copy where neither works.
package link

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// copyMarker marks a directory that is a copy made by gtr; only such
// directories are ever deleted recursively.
const copyMarker = ".gtr-copy"

// Kind is how an entry was created.
type Kind string

const (
	Symlink  Kind = "symlink"
	Junction Kind = "junction"
	Copy     Kind = "copy"
)

// Make points link at target (an absolute directory), replacing whatever
// gtr created there before.
func Make(target, link string) (Kind, error) {
	if err := Remove(link); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		if err := junction(link, target); err == nil {
			return Junction, nil
		}
	} else if err := os.Symlink(target, link); err == nil {
		return Symlink, nil
	}
	if err := copyTree(target, link); err != nil {
		os.RemoveAll(link)
		return "", fmt.Errorf("link %s: %w", link, err)
	}
	return Copy, os.WriteFile(filepath.Join(link, copyMarker), nil, 0o644)
}

// IsCopy reports whether path is a copy made by Make.
func IsCopy(path string) bool {
	_, err := os.Stat(filepath.Join(path, copyMarker))
	st, lerr := os.Lstat(path)
	return err == nil && lerr == nil && st.IsDir() && st.Mode()&os.ModeSymlink == 0
}

// Target returns where an existing link points, or "" for copies and
// missing entries.
func Target(link string) string {
	t, err := os.Readlink(link)
	if err != nil {
		return ""
	}
	return t
}

// Remove deletes an entry made by Make. Links and junctions are removed
// without touching their target; a directory is deleted only if it carries
// the copy marker. Anything else is left in place and reported.
func Remove(link string) error {
	st, err := os.Lstat(link)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 || st.Mode()&os.ModeIrregular != 0 || !st.IsDir() {
		return os.Remove(link) // a junction is removed like an empty directory
	}
	if _, err := os.Stat(filepath.Join(link, copyMarker)); err == nil {
		return os.RemoveAll(link)
	}
	if err := os.Remove(link); err == nil { // a junction or an empty directory
		return nil
	}
	return fmt.Errorf("%s is a directory gtr did not create; move it away", link)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(out, data, info.Mode().Perm()|0o200)
	})
}
