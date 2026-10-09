package source

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gtr-manager/internal/manifest"
	"gtr-manager/internal/semver"
	"gtr-manager/internal/spec"
)

// File serves file: dependencies: a local directory used in place.
type File struct {
	ProjectDir string // file: paths are relative to it
}

// Dir returns the absolute directory of a file: spec.
func (f *File) Dir(s spec.Spec) string {
	p := filepath.FromSlash(s.Path)
	if !filepath.IsAbs(p) {
		p = filepath.Join(f.ProjectDir, p)
	}
	return filepath.Clean(p)
}

// Candidates returns the one version the directory has (from its gtr.json).
func (f *File) Candidates(ctx context.Context, name string, s spec.Spec) ([]Candidate, error) {
	m, err := f.Manifest(ctx, s, Candidate{})
	if err != nil {
		return nil, err
	}
	v := "0.0.0"
	if m.Version != "" {
		pv, err := semver.Parse(m.Version)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid version %q", f.Dir(s), m.Version)
		}
		v = pv.String()
	}
	return []Candidate{{Version: v}}, nil
}

// Manifest reads gtr.json from the directory.
func (f *File) Manifest(_ context.Context, s spec.Spec, _ Candidate) (*manifest.Manifest, error) {
	dir := f.Dir(s)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("file:%s: directory %s not found", s.Path, dir)
	}
	m, err := manifest.Load(filepath.Join(dir, manifest.FileName))
	if err != nil {
		if _, serr := os.Stat(filepath.Join(dir, manifest.FileName)); errors.Is(serr, os.ErrNotExist) {
			return nil, fmt.Errorf("file:%s: %w", s.Path, ErrNoManifest)
		}
		return nil, err
	}
	return m, nil
}

// Fetch is not used: file: packages are linked, not copied.
func (f *File) Fetch(context.Context, spec.Spec, Candidate, string) error {
	return errors.New("file: packages are not fetched")
}
