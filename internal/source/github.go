package source

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
	"sort"
	"strings"
	"sync"
	"time"

	"gtr-manager/internal/archive"
	"gtr-manager/internal/manifest"
	"gtr-manager/internal/semver"
	"gtr-manager/internal/spec"
)

// DefaultGitHubAPI is the GitHub REST API.
const DefaultGitHubAPI = "https://api.github.com"

// GitHub reads packages from GitHub repositories. Versions are git tags
// (ADR-0003, rule 2): for the repository root "v1.2.3" or "1.2.3"; for a
// monorepo subdirectory "<name>@1.2.3", then "<subdir>/v1.2.3" (Go style).
type GitHub struct {
	API    string
	Token  string // sent only to the API host
	Client *http.Client
	Tmp    string // directory for downloads and unpacking

	mu    sync.Mutex
	tags  map[string][]tag
	heads map[string]Candidate
}

type tag struct {
	Name   string `json:"name"`
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

// NewGitHub returns a client. GTR_GITHUB_API selects GitHub Enterprise or a
// mirror; its token is GTR_GITHUB_TOKEN, so a github.com token never goes to
// another server.
func NewGitHub(tmp string) *GitHub {
	api, token := os.Getenv("GTR_GITHUB_API"), os.Getenv("GTR_GITHUB_TOKEN")
	if api == "" {
		api, token = DefaultGitHubAPI, os.Getenv("GITHUB_TOKEN")
	}
	return &GitHub{API: api, Token: token, Client: &http.Client{Timeout: 5 * time.Minute}, Tmp: tmp}
}

func (g *GitHub) get(ctx context.Context, path, accept, repo string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(g.API, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	resp, err := g.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github %s: %w", repo, err)
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, &notFound{repo: repo, path: path, token: g.Token != ""}
	case (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) &&
		resp.Header.Get("X-RateLimit-Remaining") == "0":
		return nil, errors.New("github: API rate limit exceeded; set GITHUB_TOKEN to raise it")
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, errors.New("github: the token is invalid or expired")
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("github %s: server responded %s: %s", repo, resp.Status, strings.TrimSpace(string(body)))
	}
}

type notFound struct {
	repo, path string
	token      bool
}

func (e *notFound) Error() string {
	hint := ""
	if !e.token {
		hint = " (for a private repository set GITHUB_TOKEN)"
	}
	return fmt.Sprintf("github: %s not found%s", e.repo, hint)
}

func isNotFound(err error) bool {
	var nf *notFound
	return errors.As(err, &nf)
}

func (g *GitHub) listTags(ctx context.Context, repo string) ([]tag, error) {
	g.mu.Lock()
	if g.tags == nil {
		g.tags = map[string][]tag{}
	}
	if t, ok := g.tags[repo]; ok {
		g.mu.Unlock()
		return t, nil
	}
	g.mu.Unlock()
	var all []tag
	for page := 1; page <= 20; page++ {
		resp, err := g.get(ctx, fmt.Sprintf("/repos/%s/tags?per_page=100&page=%d", repo, page), "application/vnd.github+json", repo)
		if err != nil {
			return nil, err
		}
		var batch []tag
		err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&batch)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("github %s: bad tags response: %w", repo, err)
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
	}
	g.mu.Lock()
	g.tags[repo] = all
	g.mu.Unlock()
	return all, nil
}

// TagVersion returns the version a tag stands for, or "" if the tag does
// not belong to this package.
func TagVersion(tagName, name, subdir string) string {
	var rest string
	switch {
	case subdir == "":
		rest = tagName
	case name != "" && strings.HasPrefix(tagName, name+"@"):
		rest = strings.TrimPrefix(tagName, name+"@")
	case strings.HasPrefix(tagName, subdir+"/"):
		rest = strings.TrimPrefix(tagName, subdir+"/")
	default:
		return ""
	}
	if strings.Contains(rest, "/") || strings.Contains(rest, "@") {
		return ""
	}
	return strictSemVer(rest)
}

var strictRe = regexp.MustCompile(`^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*))*))?$`)

// strictSemVer accepts only full SemVer 2.0 versions (without build
// metadata) — exactly what Go accepts in go.mod — and normalizes them.
func strictSemVer(s string) string {
	if !strictRe.MatchString(s) {
		return ""
	}
	return strings.TrimPrefix(s, "v")
}

// Candidates lists tagged versions, highest first. A repository without any
// version tags offers its default branch as a single "head" candidate.
func (g *GitHub) Candidates(ctx context.Context, name string, s spec.Spec) ([]Candidate, error) {
	tags, err := g.listTags(ctx, s.Repo)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Candidate
	for _, t := range tags {
		v := TagVersion(t.Name, name, s.Subdir)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, Candidate{Version: v, Tag: t.Name, Commit: t.Commit.SHA})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return semver.Compare(semver.MustParse(out[i].Version), semver.MustParse(out[j].Version)) > 0
	})
	if len(out) > 0 {
		return out, nil
	}
	head, err := g.head(ctx, s.Repo)
	if err != nil {
		return nil, err
	}
	return []Candidate{head}, nil
}

// head returns the tip of the default branch as version 0.0.0-<sha12>.
func (g *GitHub) head(ctx context.Context, repo string) (Candidate, error) {
	g.mu.Lock()
	if c, ok := g.heads[repo]; ok {
		g.mu.Unlock()
		return c, nil
	}
	g.mu.Unlock()
	var info struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := g.getJSON(ctx, "/repos/"+repo, repo, &info); err != nil {
		return Candidate{}, err
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	if err := g.getJSON(ctx, "/repos/"+repo+"/commits/"+url.PathEscape(info.DefaultBranch), repo, &commit); err != nil {
		return Candidate{}, err
	}
	if len(commit.SHA) < 12 {
		return Candidate{}, fmt.Errorf("github %s: bad commit response", repo)
	}
	// "g" keeps the pre-release alphanumeric: an all-digit SHA prefix with a
	// leading zero would not be valid SemVer (and Go rejects it in go.mod).
	c := Candidate{Version: "0.0.0-g" + commit.SHA[:12], Commit: commit.SHA, Head: true}
	g.mu.Lock()
	if g.heads == nil {
		g.heads = map[string]Candidate{}
	}
	g.heads[repo] = c
	g.mu.Unlock()
	return c, nil
}

func (g *GitHub) getJSON(ctx context.Context, path, repo string, dest any) error {
	resp, err := g.get(ctx, path, "application/vnd.github+json", repo)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(dest); err != nil {
		return fmt.Errorf("github %s: bad response: %w", repo, err)
	}
	return nil
}

// Manifest reads gtr.json at the candidate's commit.
func (g *GitHub) Manifest(ctx context.Context, s spec.Spec, c Candidate) (*manifest.Manifest, error) {
	p := "gtr.json"
	if s.Subdir != "" {
		p = s.Subdir + "/gtr.json"
	}
	resp, err := g.get(ctx, "/repos/"+s.Repo+"/contents/"+p+"?ref="+url.QueryEscape(c.Commit), "application/vnd.github.raw+json", s.Repo)
	if isNotFound(err) {
		return nil, fmt.Errorf("github:%s %s: %w", s.Repo+subdirSuffix(s), c.Version, ErrNoManifest)
	}
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return manifest.Parse(data, "github:"+s.Repo+subdirSuffix(s)+"/gtr.json")
}

func subdirSuffix(s spec.Spec) string {
	if s.Subdir == "" {
		return ""
	}
	return "/" + s.Subdir
}

// Fetch downloads the repository at the candidate's commit and places the
// package directory at dst.
func (g *GitHub) Fetch(ctx context.Context, s spec.Spec, c Candidate, dst string) error {
	resp, err := g.get(ctx, "/repos/"+s.Repo+"/zipball/"+url.PathEscape(c.Commit), "application/vnd.github+json", s.Repo)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := os.MkdirAll(g.Tmp, 0o755); err != nil {
		return err
	}
	work, err := os.MkdirTemp(g.Tmp, "fetch-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	zipPath := filepath.Join(work, "src.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("github %s: download: %w", s.Repo, err)
	}
	tree := filepath.Join(work, "tree")
	if err := archive.Extract(zipPath, tree); err != nil {
		return err
	}
	entries, err := os.ReadDir(tree)
	if err != nil {
		return err
	}
	root := tree
	if len(entries) == 1 && entries[0].IsDir() {
		root = filepath.Join(tree, entries[0].Name()) // owner-repo-<sha>/
	}
	if s.Subdir != "" {
		root = filepath.Join(root, filepath.FromSlash(s.Subdir))
		if st, err := os.Stat(root); err != nil || !st.IsDir() {
			return fmt.Errorf("github:%s at %s has no directory %s", s.Repo, c.Version, s.Subdir)
		}
	}
	return os.Rename(root, dst)
}
