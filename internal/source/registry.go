package source

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"gtr-manager/internal/manifest"
	"gtr-manager/internal/registry"
	"gtr-manager/internal/semver"
	"gtr-manager/internal/spec"
)

// Registry reads packages from a gtr registry: versions and their gtr.json
// come from the package index, files from the version's archive (checked
// against its SHA-256).
type Registry struct {
	Client *registry.Client

	mu      sync.Mutex
	indexes map[string]*registry.Index
}

// NewRegistry returns the provider of the registry in use.
func NewRegistry() *Registry { return &Registry{Client: registry.New()} }

func (r *Registry) index(ctx context.Context, name string) (*registry.Index, error) {
	r.mu.Lock()
	ix, ok := r.indexes[name]
	r.mu.Unlock()
	if ok {
		return ix, nil
	}
	ix, err := r.Client.Package(ctx, name)
	if registry.IsNotFound(err) {
		return nil, fmt.Errorf("there is no package %s in the registry %s", name, r.Client.URL)
	}
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.indexes == nil {
		r.indexes = map[string]*registry.Index{}
	}
	r.indexes[name] = ix
	r.mu.Unlock()
	return ix, nil
}

// Candidates lists the published versions, highest first; yanked ones are
// marked.
func (r *Registry) Candidates(ctx context.Context, name string, s spec.Spec) ([]Candidate, error) {
	if name == "" {
		return nil, fmt.Errorf("a registry package needs a name")
	}
	ix, err := r.index(ctx, name)
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, v := range ix.Versions {
		if _, err := semver.Parse(v.Version); err != nil {
			continue
		}
		out = append(out, Candidate{Version: v.Version, Tag: registryTag + name, Commit: "sha256:" + v.SHA256, SHA256: v.SHA256, Yanked: v.Yanked})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return semver.Compare(semver.MustParse(out[i].Version), semver.MustParse(out[j].Version)) > 0
	})
	return out, nil
}

func (r *Registry) version(ctx context.Context, name string, c Candidate) (*registry.Version, error) {
	ix, err := r.index(ctx, name)
	if err != nil {
		return nil, err
	}
	for i := range ix.Versions {
		if ix.Versions[i].Version == c.Version {
			return &ix.Versions[i], nil
		}
	}
	return nil, fmt.Errorf("%s has no version %s in the registry", name, c.Version)
}

// registryTag marks a registry candidate; the rest is the package name
// (Manifest and Fetch are not given the name).
const registryTag = "registry:"

func (r *Registry) nameOf(c Candidate) string {
	name, _ := strings.CutPrefix(c.Tag, registryTag)
	return name
}

// Manifest returns the version's gtr.json as published.
func (r *Registry) Manifest(ctx context.Context, s spec.Spec, c Candidate) (*manifest.Manifest, error) {
	name := r.nameOf(c)
	v, err := r.version(ctx, name, c)
	if err != nil {
		return nil, err
	}
	return manifest.Parse(v.Manifest, name+"@"+c.Version+" (registry)")
}

// Fetch downloads and unpacks the version's archive into dst.
func (r *Registry) Fetch(ctx context.Context, s spec.Spec, c Candidate, dst string) error {
	name := r.nameOf(c)
	if name == "" {
		return fmt.Errorf("version %s is not in a loaded index", c.Version)
	}
	var b bytes.Buffer
	if err := r.Client.Download(ctx, name, c.Version, c.SHA256, &b); err != nil {
		return err
	}
	if err := registry.Extract(&b, dst); err != nil {
		os.RemoveAll(dst)
		return err
	}
	return nil
}
