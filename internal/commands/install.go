package commands

import (
	"context"
	"fmt"
	"os"

	"gtr-manager/internal/dependency"
	"gtr-manager/internal/manifest"
	"gtr-manager/internal/repositories"
)

// Install downloads a package and records it in gtr.json. gtr.json is only
// modified after a successful download.
func Install(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("package name is required, e.g. `gtr-manager install github:owner/repo`")
	}
	if len(args) > 1 {
		return fmt.Errorf("install one package at a time (got %d)", len(args))
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
	if version, ok := m.Dependencies[key]; ok {
		return fmt.Errorf("package %q is already installed (version %s)\nto change it run: gtr-manager uninstall %s && gtr-manager install %s",
			key, version, key, dep)
	}
	newSource, ok := Sources[dep.Type]
	if !ok {
		return fmt.Errorf("%s packages are not supported yet; supported: github:owner/repo", dep.Type)
	}
	src, err := newSource()
	if err != nil {
		return err
	}
	ref, err := src.Install(context.Background(), dep, ".")
	if err != nil {
		return err
	}
	m.Dependencies[key] = dep.Version
	if err := manifest.Save(manifest.FileName, m); err != nil {
		os.RemoveAll(repositories.PackageDir(".", dep))
		return err
	}
	fmt.Fprintf(Stdout, "Installed %s (%s)\n", key, ref)
	return nil
}
