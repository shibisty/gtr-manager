package gomodules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultProxy is the public module proxy.
const DefaultProxy = "https://proxy.golang.org"

// ErrNotFound means the proxy does not have the module or version.
var ErrNotFound = errors.New("not found")

// Proxy talks to a GOPROXY-compatible server and caches .mod files.
type Proxy struct {
	URL    string
	Client *http.Client
	Cache  string // directory for verified .mod files ("" = no cache)
}

// ProxyURL picks the public proxy: GTR_GOPROXY, else the first http(s)
// entry of GOPROXY, else DefaultProxy. GOPROXY=off, or a GOPROXY with only
// "direct", is an error: gtr does not fetch from version control.
func ProxyURL() (string, error) {
	if u := os.Getenv("GTR_GOPROXY"); u != "" {
		return u, nil
	}
	env := os.Getenv("GOPROXY")
	for _, e := range strings.FieldsFunc(env, func(r rune) bool { return r == ',' || r == '|' }) {
		if strings.HasPrefix(e, "https://") || strings.HasPrefix(e, "http://") {
			return e, nil
		}
	}
	if strings.TrimSpace(env) != "" {
		return "", fmt.Errorf("GOPROXY=%s has no proxy URL; gtr downloads modules only from a proxy (set GTR_GOPROXY)", env)
	}
	return DefaultProxy, nil
}

// Proxies routes modules to the public proxy or, for GOPRIVATE/GONOPROXY
// modules, to GTR_GOPROXY_PRIVATE — private paths are never sent to the
// public proxy.
type Proxies struct {
	Public  *Proxy // nil when misconfigured (PublicErr says why)
	Private *Proxy
	// PrivatePatterns are GONOPROXY/GOPRIVATE globs.
	PrivatePatterns []string
	PublicErr       error
}

// NewProxies configures proxies from the environment.
func NewProxies(cache string) *Proxies {
	ps := &Proxies{PrivatePatterns: patterns(os.Getenv("GONOPROXY"), os.Getenv("GOPRIVATE"))}
	if url, err := ProxyURL(); err != nil {
		ps.PublicErr = err
	} else {
		ps.Public = NewProxy(url, cache)
	}
	if u := os.Getenv("GTR_GOPROXY_PRIVATE"); u != "" {
		ps.Private = NewProxy(u, cache)
	}
	return ps
}

// For returns the proxy for a module.
func (ps *Proxies) For(modPath string) (*Proxy, error) {
	if err := CheckPath(modPath); err != nil {
		return nil, err
	}
	if matchPatterns(ps.PrivatePatterns, modPath) {
		if ps.Private == nil {
			return nil, fmt.Errorf("%s is private (GOPRIVATE/GONOPROXY); set GTR_GOPROXY_PRIVATE to a proxy that serves it", modPath)
		}
		return ps.Private, nil
	}
	if ps.Public == nil {
		return nil, ps.PublicErr
	}
	return ps.Public, nil
}

func patterns(values ...string) []string {
	var out []string
	for _, v := range values {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// NewProxy returns a proxy client with a timeout.
func NewProxy(url, cache string) *Proxy {
	return &Proxy{URL: url, Client: &http.Client{Timeout: 5 * time.Minute}, Cache: cache}
}

func (p *Proxy) get(ctx context.Context, modPath, suffix string) (*http.Response, error) {
	ep, err := Escape(modPath)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(p.URL, "/") + "/" + ep + "/" + suffix
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("module proxy: %w", err)
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return nil, fmt.Errorf("%s: %w on %s (a private module? set GTR_GOPROXY and GONOSUMDB)", modPath, ErrNotFound, p.URL)
	}
	return nil, fmt.Errorf("module proxy %s: %s responded %s", p.URL, url, resp.Status)
}

// List returns the tagged versions of a module (unsorted, as served).
func (p *Proxy) List(ctx context.Context, modPath string) ([]string, error) {
	resp, err := p.get(ctx, modPath, "@v/list")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, v := range strings.Fields(string(data)) {
		if ValidVersion(modPath, v) {
			out = append(out, v)
		}
	}
	return out, nil
}

// Latest returns the version @latest resolves to (may be a pseudo-version
// for modules without tags).
func (p *Proxy) Latest(ctx context.Context, modPath string) (string, error) {
	resp, err := p.get(ctx, modPath, "@latest")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var info struct{ Version string }
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info); err != nil {
		return "", fmt.Errorf("%s@latest: bad response: %w", modPath, err)
	}
	if !ValidVersion(modPath, info.Version) {
		return "", fmt.Errorf("%s@latest: invalid version %q", modPath, info.Version)
	}
	return info.Version, nil
}

func (p *Proxy) cachePath(modPath, version string) string {
	if p.Cache == "" {
		return ""
	}
	dir, err := DirName(modPath, version)
	if err != nil {
		return ""
	}
	return filepath.Join(p.Cache, filepath.FromSlash(dir)+".mod")
}

// Mod returns a module's go.mod. verify is called on every use, including
// files from the cache.
func (p *Proxy) Mod(ctx context.Context, modPath, version string, verify func([]byte) error) ([]byte, error) {
	if c := p.cachePath(modPath, version); c != "" {
		if data, err := os.ReadFile(c); err == nil {
			if err := verify(data); err != nil {
				return nil, fmt.Errorf("%w (cached file %s)", err, c)
			}
			return data, nil
		}
	}
	ev, err := Escape(version)
	if err != nil {
		return nil, err
	}
	resp, err := p.get(ctx, modPath, "@v/"+ev+".mod")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if err := verify(data); err != nil {
		return nil, err
	}
	if c := p.cachePath(modPath, version); c != "" {
		if os.MkdirAll(filepath.Dir(c), 0o755) == nil {
			tmp := c + ".tmp"
			if os.WriteFile(tmp, data, 0o644) == nil {
				os.Rename(tmp, c)
			}
		}
	}
	return data, nil
}

// Zip downloads the module zip to dst.
func (p *Proxy) Zip(ctx context.Context, modPath, version, dst string) error {
	ev, err := Escape(version)
	if err != nil {
		return err
	}
	resp, err := p.get(ctx, modPath, "@v/"+ev+".zip")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, io.LimitReader(resp.Body, 500<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
