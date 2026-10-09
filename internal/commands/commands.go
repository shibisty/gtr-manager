// Package commands implements the gtr-manager commands. All of them operate on
// the current directory.
package commands

import (
	"context"
	"io"
	"os"

	"gtr-manager/internal/dependency"
)

// Installer downloads a package into the project directory and returns the
// ref (a branch or a tag).
type Installer interface {
	Install(ctx context.Context, dep *dependency.Dependency, project string) (string, error)
}

// I/O streams and package sources; tests replace them.
var (
	Stdin  io.Reader = os.Stdin
	Stdout io.Writer = os.Stdout
	Stderr io.Writer = os.Stderr

	// Sources maps a source type ("github") to its installer. Populated by main.
	Sources = map[string]func() (Installer, error){}
)
