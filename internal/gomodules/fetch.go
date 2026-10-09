package gomodules

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"gtr-manager/internal/archive"
	"gtr-manager/internal/dirhash"
)

// Store keeps extracted modules in <root>/<escaped path>@<version>
// (~/.gtr/store/go), immutable once written.
type Store struct {
	Root string
	Tmp  string
}

// Dir returns the directory of a module version.
func (s *Store) Dir(modPath, version string) (string, error) {
	d, err := DirName(modPath, version)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.Root, filepath.FromSlash(d)), nil
}

// AddedGoMod is the suffix of a sidecar file next to a module directory
// (outside the tree, so a zip cannot fake it) saying that gtr wrote the
// go.mod itself because the zip had none.
const AddedGoMod = ".gtr-added-go.mod"

// Hash computes the h1 hash of an extracted module, as in go.sum.
func Hash(dir, modPath, version string) (string, error) {
	_, err := os.Stat(filepath.Clean(dir) + AddedGoMod)
	added := err == nil
	return dirhash.HashDir(dir, modPath+"@"+version, func(rel string) bool { return added && rel == "go.mod" })
}

// check compares a hash with gtr.lock when the module is locked (no network,
// like go.sum), otherwise with the checksum database.
func (s *Store) check(ctx context.Context, db *SumDB, modPath, version, want, got string) error {
	if want != "" {
		if got != want {
			return fmt.Errorf("SECURITY ERROR: %s@%s hash %s does not match gtr.lock (%s)", modPath, version, got, want)
		}
		return nil
	}
	if db != nil && !db.IsPrivate(modPath) {
		dbZip, _, err := db.Lookup(ctx, modPath, version)
		if err != nil {
			return err
		}
		if got != dbZip {
			return fmt.Errorf("SECURITY ERROR: %s@%s hash %s does not match the checksum database (%s)", modPath, version, got, dbZip)
		}
	}
	return nil
}

// Fetch makes a module version available in the store and returns its
// directory and h1 hash; p must be the proxy for the module. The tree (new
// or already stored) must match want from gtr.lock, or the checksum
// database when the module is not locked yet. goMod (already verified) is
// written as go.mod when the zip has none (modules from before Go modules).
func (s *Store) Fetch(ctx context.Context, p *Proxy, db *SumDB, modPath, version, want string, goMod []byte) (string, string, error) {
	dir, err := s.Dir(modPath, version)
	if err != nil {
		return "", "", err
	}
	if _, err := os.Stat(dir); err == nil {
		// Re-hash the stored copy: locally against gtr.lock, or against the
		// checksum database when nothing is locked yet.
		got, err := Hash(dir, modPath, version)
		if err != nil {
			return "", "", err
		}
		if err := s.check(ctx, db, modPath, version, want, got); err != nil {
			return "", "", fmt.Errorf("%w; the store copy %s may have been modified: delete it", err, dir)
		}
		return dir, got, nil
	}
	if err := os.MkdirAll(s.Tmp, 0o755); err != nil {
		return "", "", err
	}
	work, err := os.MkdirTemp(s.Tmp, "gomod-*")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(work)
	zipPath := filepath.Join(work, "m.zip")
	if err := p.Zip(ctx, modPath, version, zipPath); err != nil {
		return "", "", err
	}
	tree := filepath.Join(work, "tree")
	if err := archive.Extract(zipPath, tree); err != nil {
		return "", "", fmt.Errorf("%s@%s: %w", modPath, version, err)
	}
	root := filepath.Join(tree, filepath.FromSlash(modPath+"@"+version))
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return "", "", fmt.Errorf("%s@%s: the zip does not contain %s@%s/", modPath, version, modPath, version)
	}
	got, err := Hash(root, modPath, version)
	if err != nil {
		return "", "", err
	}
	if err := s.check(ctx, db, modPath, version, want, got); err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", "", err
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); os.IsNotExist(err) {
		if err := os.WriteFile(filepath.Join(root, "go.mod"), goMod, 0o644); err != nil {
			return "", "", err
		}
		if err := os.WriteFile(filepath.Clean(dir)+AddedGoMod, nil, 0o644); err != nil {
			return "", "", err
		}
	}
	if err := os.Rename(root, dir); err != nil {
		if _, serr := os.Stat(dir); serr != nil {
			return "", "", err
		}
	}
	return dir, got, nil
}
