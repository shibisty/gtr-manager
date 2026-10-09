// Package source lists versions of gtr packages and downloads them.
package source

import (
	"context"
	"errors"

	"gtr-manager/internal/manifest"
	"gtr-manager/internal/spec"
)

// Candidate is one installable version of a package.
type Candidate struct {
	Version string // normalized SemVer, e.g. "0.2.1"
	Tag     string // git tag, empty for file: and branch heads
	Commit  string // full commit SHA for git sources
	Head    bool   // the default branch of a repository without version tags
}

// ErrNoManifest means the package has no gtr.json.
var ErrNoManifest = errors.New("no gtr.json")

// Provider lists and fetches packages of one source kind.
type Provider interface {
	// Candidates returns versions, highest first. name is the dependency key
	// (used for monorepo tag prefixes); it may be empty when unknown.
	Candidates(ctx context.Context, name string, s spec.Spec) ([]Candidate, error)
	// Manifest returns the package's gtr.json at a version.
	Manifest(ctx context.Context, s spec.Spec, c Candidate) (*manifest.Manifest, error)
	// Fetch places the package files (only the subdirectory for monorepos)
	// into dst, which must not exist.
	Fetch(ctx context.Context, s spec.Spec, c Candidate, dst string) error
}
