// Package registry talks to a gtr package registry (gtr-registry): package
// indexes, archives, publishing, yanking, owners, search and API tokens.
//
// The registry is GTR_REGISTRY, else the one in ~/.gtr/registry.json (set by
// gtr login), else DefaultURL; its token is GTR_REGISTRY_TOKEN, else the one
// gtr login saved for that URL.
package registry

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"gtr-manager/internal/home"
)

// DefaultURL is the registry used when none is configured: a gtr-registry
// running locally, until a public one exists.
const DefaultURL = "http://localhost:4873"

// ConfigFile is the file of registry settings in the gtr home. It is not
// config.json, which belongs to the gtr launcher (default versions, links).
const ConfigFile = "registry.json"

// Config is ~/.gtr/registry.json.
type Config struct {
	Registry string            `json:"registry,omitempty"`
	Tokens   map[string]string `json:"tokens,omitempty"` // registry URL → API token
	path     string
}

// LoadConfig reads ~/.gtr/registry.json (missing: empty).
func LoadConfig() (*Config, error) {
	dir, err := home.Dir()
	if err != nil {
		return nil, err
	}
	c := &Config{path: filepath.Join(dir, ConfigFile)}
	data, err := os.ReadFile(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("%s: %w", c.path, err)
	}
	return c, nil
}

// Save writes the config, readable by the user only (it holds tokens).
func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(c.path, append(data, '\n'), 0o600)
}

// Normalize trims a registry URL ("localhost:4873/" → "http://localhost:4873").
func Normalize(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	if u != "" && !strings.Contains(u, "://") {
		u = "http://" + u
	}
	return u
}

// URL is the registry in use.
func URL() string {
	if u := os.Getenv("GTR_REGISTRY"); u != "" {
		return Normalize(u)
	}
	if c, err := LoadConfig(); err == nil && c.Registry != "" {
		return Normalize(c.Registry)
	}
	return DefaultURL
}

// Token is the API token for registry u ("" when there is none).
func Token(u string) string {
	if t := os.Getenv("GTR_REGISTRY_TOKEN"); t != "" {
		return t
	}
	if c, err := LoadConfig(); err == nil {
		return c.Tokens[Normalize(u)]
	}
	return ""
}

// Client is an HTTP client of one registry.
type Client struct {
	URL   string
	Token string
	HTTP  *http.Client
}

// New returns a client of the registry in use, with its token.
func New() *Client {
	u := URL()
	return &Client{URL: u, Token: Token(u)}
}

// Error is an error answer of the registry.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	switch e.Status {
	case http.StatusUnauthorized:
		return "the registry refused the request: " + e.Message
	}
	return fmt.Sprintf("the registry answered %d: %s", e.Status, e.Message)
}

// IsNotFound reports whether err is a 404 of the registry.
func IsNotFound(err error) bool {
	var re *Error
	return errors.As(err, &re) && re.Status == http.StatusNotFound
}

func (c *Client) do(ctx context.Context, method, p string, body io.Reader, contentType string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.URL+"/api/v1"+p, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Minute}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("the registry %s is not reachable (set it with GTR_REGISTRY or `gtr login --registry <url>`): %w", c.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var e struct {
			Error   string              `json:"error"`
			Message string              `json:"message"`
			Errors  map[string][]string `json:"errors"`
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		msg := strings.TrimSpace(string(data))
		if json.Unmarshal(data, &e) == nil {
			switch {
			case e.Message != "":
				msg = e.Message
			case e.Error != "":
				msg = e.Error
			}
			for f, list := range e.Errors {
				msg += "; " + f + ": " + strings.Join(list, ", ")
			}
		}
		return &Error{Status: resp.StatusCode, Message: msg}
	}
	if out == nil {
		return nil
	}
	if w, ok := out.(io.Writer); ok {
		_, err = io.Copy(w, resp.Body)
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Version is a version in a package index.
type Version struct {
	Version     string          `json:"version"`
	SHA256      string          `json:"sha256"`
	Size        int64           `json:"size"`
	Yanked      bool            `json:"yanked,omitempty"`
	PublishedAt time.Time       `json:"published_at"`
	Manifest    json.RawMessage `json:"manifest"`
}

// Index is a package's index.
type Index struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Repository  string    `json:"repository,omitempty"`
	Homepage    string    `json:"homepage,omitempty"`
	License     string    `json:"license,omitempty"`
	Latest      string    `json:"latest,omitempty"`
	Downloads   int64     `json:"downloads"`
	Owners      []string  `json:"owners,omitempty"`
	Versions    []Version `json:"versions"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Package returns a package's index.
func (c *Client) Package(ctx context.Context, name string) (*Index, error) {
	var ix Index
	if err := c.do(ctx, "GET", "/packages/"+url.PathEscape(name), nil, "", &ix); err != nil {
		return nil, err
	}
	return &ix, nil
}

// Download writes a version's archive to w and checks its SHA-256.
func (c *Client) Download(ctx context.Context, name, version, sha string, w io.Writer) error {
	h := sha256.New()
	if err := c.do(ctx, "GET", "/packages/"+url.PathEscape(name)+"/versions/"+url.PathEscape(version)+"/archive", nil, "", io.MultiWriter(w, h)); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); sha != "" && got != sha {
		return fmt.Errorf("%s@%s: the archive's SHA-256 is %s, the index says %s", name, version, got, sha)
	}
	return nil
}

// Publish uploads a version's archive.
func (c *Client) Publish(ctx context.Context, name, version string, archive []byte) (*Version, error) {
	var v Version
	err := c.do(ctx, "PUT", "/packages/"+url.PathEscape(name)+"/versions/"+url.PathEscape(version), bytes.NewReader(archive), "application/gzip", &v)
	return &v, err
}

// Yank hides a version from new installs (or brings it back).
func (c *Client) Yank(ctx context.Context, name, version string, yank bool) error {
	method := "POST"
	if !yank {
		method = "DELETE"
	}
	return c.do(ctx, method, "/packages/"+url.PathEscape(name)+"/versions/"+url.PathEscape(version)+"/yank", nil, "", nil)
}

// Owners lists a package's owners.
func (c *Client) Owners(ctx context.Context, name string) ([]string, error) {
	var out struct{ Owners []string }
	err := c.do(ctx, "GET", "/packages/"+url.PathEscape(name)+"/owners", nil, "", &out)
	return out.Owners, err
}

// AddOwner lets another user publish the package.
func (c *Client) AddOwner(ctx context.Context, name, email string) ([]string, error) {
	var out struct{ Owners []string }
	body, _ := json.Marshal(map[string]string{"email": email})
	err := c.do(ctx, "PUT", "/packages/"+url.PathEscape(name)+"/owners", bytes.NewReader(body), "application/json", &out)
	return out.Owners, err
}

// RemoveOwner takes the package from a user.
func (c *Client) RemoveOwner(ctx context.Context, name, email string) ([]string, error) {
	var out struct{ Owners []string }
	err := c.do(ctx, "DELETE", "/packages/"+url.PathEscape(name)+"/owners/"+url.PathEscape(email), nil, "", &out)
	return out.Owners, err
}

// Summary is a package in search results.
type Summary struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Latest      string `json:"latest,omitempty"`
	Downloads   int64  `json:"downloads"`
}

// Search finds packages.
func (c *Client) Search(ctx context.Context, q string, limit int) ([]Summary, error) {
	var out struct{ Packages []Summary }
	err := c.do(ctx, "GET", fmt.Sprintf("/search?q=%s&limit=%d", url.QueryEscape(q), limit), nil, "", &out)
	return out.Packages, err
}

// Login exchanges an email and a password for a new API token.
func (c *Client) Login(ctx context.Context, email, password, tokenName string) (token, name string, err error) {
	body, _ := json.Marshal(map[string]string{"email": email, "password": password, "name": tokenName})
	var out struct{ Token, Name string }
	err = c.do(ctx, "POST", "/tokens", bytes.NewReader(body), "application/json", &out)
	return out.Token, out.Name, err
}

// Logout revokes the client's token.
func (c *Client) Logout(ctx context.Context) error {
	return c.do(ctx, "DELETE", "/tokens/current", nil, "", nil)
}

// Me is who the token belongs to.
func (c *Client) Me(ctx context.Context) (name, email string, err error) {
	var out struct{ Name, Email string }
	err = c.do(ctx, "GET", "/me", nil, "", &out)
	return out.Name, out.Email, err
}

// Extract unpacks a package archive into dst (which must not exist):
// regular files and directories only, no path outside dst.
func Extract(archive io.Reader, dst string) error {
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("the archive is not gzipped: %w", err)
	}
	tr := tar.NewReader(gz)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if name == "." || strings.HasPrefix(name, "../") || name == ".." || path.IsAbs(name) || strings.Contains(name, `\`) {
			return fmt.Errorf("the archive has an unsafe path %q", h.Name)
		}
		target := filepath.Join(dst, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("the archive may hold only files and directories, not %q", h.Name)
		}
	}
}

// Pack makes a package archive of files (slash paths relative to dir).
func Pack(dir string, files []string) ([]byte, error) {
	var b bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f)))
		if err != nil {
			return nil, err
		}
		// fixed metadata: the same files give the same archive
		if err := tw.WriteHeader(&tar.Header{Name: f, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg,
			ModTime: time.Unix(0, 0), Format: tar.FormatPAX}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
