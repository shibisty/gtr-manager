// Package store keeps downloaded packages in ~/.gtr/store (ADR-0005).
//
// A package directory store/gtr/<name>@<version>-<hash8> appears in one
// rename from a temporary directory and is never modified afterwards.
// <hash8> comes from the h1 integrity hash of the downloaded tree, so two
// different trees for the same version (a moved tag) never mix.
package store

import (
	"fmt"
	"os"
	"path/filepath"

	"gtr-manager/internal/dirhash"
)

// GeneratedFiles are written by gtr into package directories and excluded
// from the integrity hash.
var GeneratedFiles = map[string]bool{"go.mod": true}

// Store is a store root, e.g. ~/.gtr/store.
type Store struct {
	Root string
	Tmp  string // must be on the same volume as Root
}

// Dir returns the directory of a package with a known integrity.
func (s *Store) Dir(name, version, integrity string) string {
	return filepath.Join(s.Root, "gtr", fmt.Sprintf("%s@%s-%s", name, version, dirhash.Short(integrity)))
}

// Has reports whether the package is in the store.
func (s *Store) Has(name, version, integrity string) bool {
	if integrity == "" {
		return false
	}
	st, err := os.Stat(s.Dir(name, version, integrity))
	return err == nil && st.IsDir()
}

// Integrity hashes a package tree the way the store does.
func Integrity(dir, name, version string) (string, error) {
	return dirhash.HashDir(dir, name+"@v"+version, func(rel string) bool { return GeneratedFiles[rel] })
}

// Put moves the tree at src into the store and returns its directory and
// integrity. prepare is called on the tree before the move (to write the
// generated go.mod). If the same tree is already stored, src is removed and
// the existing directory is returned. If want is not empty, the tree must
// match it.
func (s *Store) Put(src, name, version, want string, prepare func(dir string) error) (dir, integrity string, err error) {
	integrity, err = Integrity(src, name, version)
	if err != nil {
		return "", "", err
	}
	if want != "" && want != integrity {
		return "", "", fmt.Errorf("%s@%s: integrity mismatch: gtr.lock has %s, the download has %s "+
			"(the tag was moved or the download was tampered with; run `gtr update %s` to accept the new code)",
			name, version, want, integrity, name)
	}
	dir = s.Dir(name, version, integrity)
	if _, err := os.Stat(dir); err == nil {
		os.RemoveAll(src)
		return dir, integrity, nil
	}
	if prepare != nil {
		if err := prepare(src); err != nil {
			return "", "", err
		}
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", "", err
	}
	if err := os.Rename(src, dir); err != nil {
		if _, serr := os.Stat(dir); serr == nil { // another process stored it first
			os.RemoveAll(src)
			return dir, integrity, nil
		}
		return "", "", err
	}
	return dir, integrity, nil
}

// TempDir returns a new empty directory path for a download (not created).
func (s *Store) TempDir() (string, error) {
	if err := os.MkdirAll(s.Tmp, 0o755); err != nil {
		return "", err
	}
	d, err := os.MkdirTemp(s.Tmp, "pkg-*")
	if err != nil {
		return "", err
	}
	if err := os.Remove(d); err != nil {
		return "", err
	}
	return d, nil
}
