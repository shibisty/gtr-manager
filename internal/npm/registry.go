// Package npm installs frontend packages from the npm registry without npm
// (`"preact": "npm:^10.19"` in gtr.json): versions by npm's range rules,
// tarballs checked against their integrity hash, a node_modules tree with
// hoisting like npm's, and no install scripts.
package npm

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DefaultRegistry is the public npm registry.
const DefaultRegistry = "https://registry.npmjs.org"

// Registry fetches package metadata and tarballs.
type Registry struct {
	URL    string       // DefaultRegistry when empty (GTR_NPM_REGISTRY)
	Token  string       // a bearer token for private registries (GTR_NPM_TOKEN)
	Client *http.Client // nil: a client with a 60-second timeout

	mu    sync.Mutex
	cache map[string]*Packument
}

// Packument is the registry document of a package (abbreviated form).
type Packument struct {
	Name     string             `json:"name"`
	DistTags map[string]string  `json:"dist-tags"`
	Versions map[string]Release `json:"versions"`
}

// Release is one version of a package.
type Release struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	Dependencies         map[string]string `json:"dependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
	OS                   []string          `json:"os"`
	CPU                  []string          `json:"cpu"`
	Deprecated           any               `json:"deprecated"`
	Dist                 struct {
		Tarball   string `json:"tarball"`
		Integrity string `json:"integrity"`
		Shasum    string `json:"shasum"`
	} `json:"dist"`
}

var nameRe = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._~-]*/)?[a-z0-9][a-z0-9._~-]*$`)

// ValidName checks an npm package name ("preact", "@preact/signals").
func ValidName(name string) error {
	if len(name) > 214 || !nameRe.MatchString(name) {
		return fmt.Errorf("invalid npm package name %q", name)
	}
	return nil
}

func (r *Registry) base() string {
	if r.URL == "" {
		return DefaultRegistry
	}
	return strings.TrimSuffix(r.URL, "/")
}

func (r *Registry) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (r *Registry) get(ctx context.Context, u, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if r.Token != "" && strings.HasPrefix(u, r.base()) {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%s: not found", u)
		}
		return nil, fmt.Errorf("%s: %s", u, resp.Status)
	}
	return resp, nil
}

// Packument returns the metadata of a package (cached for the Registry's
// lifetime).
func (r *Registry) Packument(ctx context.Context, name string) (*Packument, error) {
	if err := ValidName(name); err != nil {
		return nil, err
	}
	r.mu.Lock()
	if p, ok := r.cache[name]; ok {
		r.mu.Unlock()
		return p, nil
	}
	r.mu.Unlock()
	u := r.base() + "/" + strings.Replace(url.PathEscape(name), "%40", "@", 1)
	resp, err := r.get(ctx, u, "application/vnd.npm.install-v1+json; q=1.0, application/json; q=0.8")
	if err != nil {
		return nil, fmt.Errorf("npm: %s: %w", name, err)
	}
	defer resp.Body.Close()
	var p Packument
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<20)).Decode(&p); err != nil {
		return nil, fmt.Errorf("npm: %s: reading the registry metadata: %w", name, err)
	}
	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]*Packument{}
	}
	r.cache[name] = &p
	r.mu.Unlock()
	return &p, nil
}

// Best picks the version of p for a range: the "latest" tag when it
// matches (as npm does), else the highest matching version.
func (p *Packument) Best(rng Range) (Release, bool) {
	if tag, ok := p.DistTags["latest"]; ok {
		if rel, ok := p.Versions[tag]; ok {
			if v, err := ParseVersion(tag); err == nil && rng.Match(v) {
				return rel, true
			}
		}
	}
	var best Release
	var bestV Version
	found := false
	for s, rel := range p.Versions {
		v, err := ParseVersion(s)
		if err != nil || !rng.Match(v) {
			continue
		}
		if !found || Compare(v, bestV) > 0 {
			best, bestV, found = rel, v, true
		}
	}
	return best, found
}

func encodeB64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// integrity returns the hash to check a tarball with: the SRI value
// ("sha512-<base64>"), or the legacy hex sha1 shasum.
func integrity(sri, shasum string) (algo string, sum []byte, h hash.Hash, err error) {
	for _, part := range strings.Fields(sri) { // several hashes: the strongest we know
		a, b64, ok := strings.Cut(part, "-")
		if !ok {
			continue
		}
		if a == "sha512" {
			sum, err = base64.StdEncoding.DecodeString(b64)
			return a, sum, sha512.New(), err
		}
	}
	for _, part := range strings.Fields(sri) {
		if a, b64, ok := strings.Cut(part, "-"); ok && a == "sha1" {
			sum, err = base64.StdEncoding.DecodeString(b64)
			return a, sum, sha1.New(), err
		}
	}
	if shasum != "" {
		sum, err = hex.DecodeString(shasum)
		return "sha1", sum, sha1.New(), err
	}
	return "", nil, nil, errors.New("no integrity hash")
}

// Fetch downloads a tarball, checks its integrity and extracts it into dir
// (the package's files, without the tarball's top directory).
func (r *Registry) Fetch(ctx context.Context, tarball, sri, shasum, dir string) error {
	algo, want, h, err := integrity(sri, shasum)
	if err != nil {
		return fmt.Errorf("%s: %w", tarball, err)
	}
	resp, err := r.get(ctx, tarball, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dir), ".npm-*.tgz")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, 1<<30)); err != nil {
		return fmt.Errorf("%s: %w", tarball, err)
	}
	if got := h.Sum(nil); string(got) != string(want) {
		return fmt.Errorf("%s: %s integrity mismatch (the registry or a proxy returned other content)", tarball, algo)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return extract(tmp, dir)
}

// extract unpacks a .tgz, dropping the first path element ("package/"),
// refusing paths that leave dir; links and special files are skipped.
func extract(r io.Reader, dir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("npm: not a gzip tarball: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("npm: reading the tarball: %w", err)
		}
		name := path.Clean(strings.TrimPrefix(strings.ReplaceAll(hdr.Name, `\`, "/"), "/"))
		if name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("npm: unsafe path %q in the tarball", hdr.Name)
		}
		_, rest, ok := strings.Cut(name, "/")
		if !ok || rest == "" || rest == "." {
			continue
		}
		if strings.HasPrefix(rest, "../") || rest == ".." || path.IsAbs(rest) {
			return fmt.Errorf("npm: unsafe path %q in the tarball", hdr.Name)
		}
		dst := filepath.Join(dir, filepath.FromSlash(rest))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if hdr.Mode&0o111 != 0 {
				mode = 0o755
			}
			f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, io.LimitReader(tr, 512<<20))
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		}
	}
}
