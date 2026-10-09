// Package repositories downloads packages from external sources.
package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gtr-manager/internal/archive"
	"gtr-manager/internal/dependency"
	"gtr-manager/internal/home"
)

// DefaultGitHubAPI is the GitHub REST API base URL.
const DefaultGitHubAPI = "https://api.github.com"

// ErrVersionRange is returned for version ranges, which arrive with the resolver (stage 3).
var ErrVersionRange = errors.New("version ranges (^1, 1.2, >=1.0) are not supported yet; use * or an exact tag such as 1.2.0")

var exactVersionRe = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)

// GitHub downloads GitHub repositories. Token (GITHUB_TOKEN by default) is
// required for private repositories and lifts the 60 requests/hour limit.
type GitHub struct {
	API    string
	Token  string
	Client *http.Client
	Home   home.Layout
}

// NewGitHub creates a client with default settings.
func NewGitHub() (*GitHub, error) {
	l, err := home.New()
	if err != nil {
		return nil, err
	}
	// GTR_GITHUB_API: GitHub Enterprise or a mirror. Its token is
	// GTR_GITHUB_TOKEN, so a github.com token never goes to another server.
	api, token := os.Getenv("GTR_GITHUB_API"), os.Getenv("GTR_GITHUB_TOKEN")
	if api == "" {
		api, token = DefaultGitHubAPI, os.Getenv("GITHUB_TOKEN")
	}
	return &GitHub{API: api, Token: token, Client: &http.Client{Timeout: 5 * time.Minute}, Home: l}, nil
}

// PackageDir returns where the package is installed inside the project.
func PackageDir(project string, dep *dependency.Dependency) string {
	if dep.Type == "gtr" {
		return filepath.Join(project, "packages", filepath.FromSlash(dep.Repository))
	}
	return filepath.Join(project, "packages", dep.Type, filepath.FromSlash(dep.Repository))
}

// Install downloads dep into project/packages/github/owner/repo and returns
// the git ref the code was taken from (a branch or a tag).
func (g *GitHub) Install(ctx context.Context, dep *dependency.Dependency, project string) (string, error) {
	ref, err := g.resolve(ctx, dep)
	if err != nil {
		return "", err
	}
	zipPath, err := g.download(ctx, dep.Repository, ref)
	if err != nil {
		return "", err
	}
	if err := installTree(zipPath, PackageDir(project, dep)); err != nil {
		return "", err
	}
	return ref, nil
}

// resolve maps "*" to the default branch and an exact version to tag v1.2.0 or 1.2.0.
func (g *GitHub) resolve(ctx context.Context, dep *dependency.Dependency) (string, error) {
	if dep.Version == "*" || dep.Version == "" {
		var info struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := g.getJSON(ctx, "/repos/"+dep.Repository, dep.Repository, &info); err != nil {
			return "", err
		}
		if info.DefaultBranch == "" {
			return "", fmt.Errorf("github: %s has no default branch (empty repository?)", dep.Repository)
		}
		return info.DefaultBranch, nil
	}
	if !exactVersionRe.MatchString(dep.Version) {
		return "", fmt.Errorf("%s@%s: %w", dep.Key(), dep.Version, ErrVersionRange)
	}
	v := strings.TrimPrefix(dep.Version, "v")
	for _, tag := range []string{"v" + v, v} {
		var ref struct {
			Ref string `json:"ref"`
		}
		err := g.getJSON(ctx, "/repos/"+dep.Repository+"/git/ref/tags/"+url.PathEscape(tag), dep.Repository, &ref)
		if err == nil {
			return tag, nil
		}
		if !errors.Is(err, errNotFound) {
			return "", err
		}
	}
	return "", fmt.Errorf("github: %s has no tag v%s or %s", dep.Repository, v, v)
}

var errNotFound = errors.New("not found")

func (g *GitHub) request(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(g.API, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	return g.Client.Do(req)
}

// check turns a GitHub response into a readable error.
func (g *GitHub) check(resp *http.Response, repo string) error {
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		hint := ""
		if g.Token == "" {
			hint = " (for a private repository set GITHUB_TOKEN)"
		}
		return fmt.Errorf("github: %s: %w%s", repo, errNotFound, hint)
	case (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) &&
		resp.Header.Get("X-RateLimit-Remaining") == "0":
		hint := "set GITHUB_TOKEN to raise the limit"
		if g.Token != "" {
			hint = "try again later"
		}
		return fmt.Errorf("github: API rate limit exceeded; %s", hint)
	case resp.StatusCode == http.StatusUnauthorized:
		return errors.New("github: GITHUB_TOKEN is invalid or expired")
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("github: %s: server responded %s: %s", repo, resp.Status, strings.TrimSpace(string(body)))
	}
}

func (g *GitHub) getJSON(ctx context.Context, path, repo string, dest any) error {
	resp, err := g.request(ctx, path)
	if err != nil {
		return fmt.Errorf("github: %w", err)
	}
	defer resp.Body.Close()
	if err := g.check(resp, repo); err != nil {
		return err
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(dest); err != nil {
		return fmt.Errorf("github: bad response: %w", err)
	}
	return nil
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// download saves the zip archive of ref to the ~/.gtr/cache/downloads/github cache.
func (g *GitHub) download(ctx context.Context, repo, ref string) (string, error) {
	dir := filepath.Join(g.Home.Downloads(), "github")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := unsafeChars.ReplaceAllString(strings.ReplaceAll(repo, "/", "_")+"@"+ref, "_") + ".zip"
	dst := filepath.Join(dir, name)

	resp, err := g.request(ctx, "/repos/"+repo+"/zipball/"+url.PathEscape(ref))
	if err != nil {
		return "", fmt.Errorf("github: download %s: %w", repo, err)
	}
	defer resp.Body.Close()
	if err := g.check(resp, repo); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, name+".*.part")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, resp.Body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("github: download %s: %w", repo, err)
	}
	// Branches move, so a branch archive is always downloaded again and
	// replaces the old one; the cache is kept for re-extraction and debugging.
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	return dst, nil
}

// installTree extracts the archive next to dst and moves its single
// top-level directory (owner-repo-sha) into place at dst.
func installTree(zipPath, dst string) error {
	parent := filepath.Dir(dst)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".gtr-tmp-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := archive.Extract(zipPath, tmp); err != nil {
		return err
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return err
	}
	root := tmp
	if len(entries) == 1 && entries[0].IsDir() {
		root = filepath.Join(tmp, entries[0].Name())
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if root == tmp {
		// The archive has no common root: move the whole temporary directory.
		if err := os.Rename(tmp, dst); err != nil {
			return err
		}
		return nil
	}
	return os.Rename(root, dst)
}
