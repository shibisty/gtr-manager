package commands

import (
	"fmt"
	"os"
	"path/filepath"

	"gtr-manager/internal/dependency"
	"gtr-manager/internal/manifest"
	"gtr-manager/internal/repositories"
)

// Uninstall removes a package from gtr.json and from packages/.
func Uninstall(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("package name is required")
	}
	dep, err := dependency.Parse(args[0])
	if err != nil {
		return err
	}
	m, err := manifest.Load(manifest.FileName)
	if err != nil {
		return err
	}
	key := dep.Key()
	version, ok := m.Dependencies[key]
	if !ok {
		return fmt.Errorf("package %q is not installed", key)
	}
	delete(m.Dependencies, key)
	if err := manifest.Save(manifest.FileName, m); err != nil {
		return err
	}
	if dep.Type != "git" { // git sources have no directory yet
		dir := repositories.PackageDir(".", dep)
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		removeEmptyParents(filepath.Dir(dir), ".")
	}
	fmt.Fprintf(Stdout, "Removed %s (%s)\n", key, version)
	return nil
}

// removeEmptyParents removes now-empty directories up to, but not including,
// stop, so removing the last package leaves no empty packages/github/owner.
func removeEmptyParents(dir, stop string) {
	for dir != stop && dir != "." && dir != string(filepath.Separator) {
		if os.Remove(dir) != nil { // not empty or no permission: stop
			return
		}
		dir = filepath.Dir(dir)
	}
}
