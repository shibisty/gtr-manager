// Package commands implements the gtr-manager commands. They work on the
// current directory.
package commands

import (
	"io"
	"os"
	"path/filepath"

	"gtr-manager/internal/gomodules"
	"gtr-manager/internal/home"
	"gtr-manager/internal/install"
	"gtr-manager/internal/source"
	"gtr-manager/internal/stdlib"
)

// IO and the installer factory; tests replace them.
var (
	Stdin  io.Reader = os.Stdin
	Stdout io.Writer = os.Stdout
	Stderr io.Writer = os.Stderr

	// NewInstaller returns the installer for a project directory.
	NewInstaller = func(dir string) (*install.Installer, error) {
		l, err := home.New()
		if err != nil {
			return nil, err
		}
		return &install.Installer{Dir: dir, Home: l, GitHub: source.NewGitHub(l.Tmp()), SumDB: gomodules.NewSumDB(), Log: Stderr,
			CheckName: CheckName}, nil
	}
)

// CheckName rejects a package name taken by the standard library; tests
// replace it.
var CheckName = func(name string) error {
	l, err := home.New()
	if err != nil {
		return nil
	}
	return stdlib.Check(name, filepath.Join(l.Root, "cache"))
}

// installer works on the project in the current directory; in a workspace
// member, on the whole workspace, with add and remove changing the member's
// gtr.json (ADR-0009).
func installer() (*install.Installer, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	root, member, err := install.FindRoot(wd)
	if err != nil {
		return nil, err
	}
	in, err := NewInstaller(root)
	if err != nil {
		return nil, err
	}
	in.Member = member
	return in, nil
}
