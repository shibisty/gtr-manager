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
	"strings"
	"sync"
	"time"

	"gtr-manager/internal/manifest"
	"gtr-manager/internal/spec"
)

// DefaultGitLab is gitlab.com.
const DefaultGitLab = "https://gitlab.com"

// GitLab reads packages from GitLab projects through the REST API (v4).
// Versions are tags, as for GitHub (TagVersion); a project without version
// tags offers its default branch.
type GitLab struct {
	URL    string // server, e.g. https://gitlab.com or a self-hosted GitLab
	Token  string // PRIVATE-TOKEN, sent only to URL
	Client *http.Client
	Tmp    string

	mu    sync.Mutex
	tags  map[string][]TagRef
	heads map[string]Candidate
}

// NewGitLab configures GitLab from GTR_GITLAB_URL and GTR_GITLAB_TOKEN (or
// GITLAB_TOKEN for gitlab.com).
func NewGitLab(tmp string) *GitLab {
	u, token := os.Getenv("GTR_GITLAB_URL"), os.Getenv("GTR_GITLAB_TOKEN")
	if u == "" {
		u = DefaultGitLab
		if token == "" {
			token = os.Getenv("GITLAB_TOKEN")
		}
	}
	return &GitLab{URL: u, Token: token, Client: &http.Client{Timeout: 5 * time.Minute}, Tmp: tmp}
}

func (g *GitLab) api(project, rest string) string {
	return strings.TrimRight(g.URL, "/") + "/api/v4/projects/" + url.PathEscape(project) + rest
}

func (g *GitLab) get(ctx context.Context, u, project string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	client := g.Client
	if g.Token != "" {
		req.Header.Set("PRIVATE-TOKEN", g.Token)
		// Go drops Authorization on a redirect to another host, but not custom
		// headers: an archive served from object storage must not get the token.
		c := *g.Client
		c.CheckRedirect = func(r *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			if !strings.EqualFold(r.URL.Host, via[0].URL.Host) || r.URL.Scheme != via[0].URL.Scheme {
				r.Header.Del("PRIVATE-TOKEN")
			}
			return nil
		}
		client = &c
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab %s: %w", project, err)
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		hint := " (for a private project set GTR_GITLAB_TOKEN)"
		if g.Token != "" {
			hint = " (or the token cannot read it)"
		}
		return nil, &gitlabNotFound{msg: fmt.Sprintf("gitlab: %s not found%s", project, hint)}
	case http.StatusUnauthorized:
		return nil, errors.New("gitlab: the token is invalid or expired")
	case http.StatusTooManyRequests:
		return nil, errors.New("gitlab: rate limit exceeded; try again later or set GTR_GITLAB_TOKEN")
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("gitlab %s: server responded %s: %s", project, resp.Status, strings.TrimSpace(string(body)))
	}
}

type gitlabNotFound struct{ msg string }

func (e *gitlabNotFound) Error() string { return e.msg }

func (g *GitLab) getJSON(ctx context.Context, u, project string, dest any) error {
	resp, err := g.get(ctx, u, project)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(dest); err != nil {
		return fmt.Errorf("gitlab %s: bad response: %w", project, err)
	}
	return nil
}

func (g *GitLab) listTags(ctx context.Context, project string) ([]TagRef, error) {
	g.mu.Lock()
	if t, ok := g.tags[project]; ok {
		g.mu.Unlock()
		return t, nil
	}
	g.mu.Unlock()
	var all []TagRef
	for page := 1; page <= 20; page++ {
		var batch []struct {
			Name   string `json:"name"`
			Commit struct {
				ID string `json:"id"`
			} `json:"commit"`
		}
		if err := g.getJSON(ctx, g.api(project, fmt.Sprintf("/repository/tags?per_page=100&page=%d", page)), project, &batch); err != nil {
			return nil, err
		}
		for _, t := range batch {
			all = append(all, TagRef{Name: t.Name, Commit: t.Commit.ID})
		}
		if len(batch) < 100 {
			break
		}
	}
	g.mu.Lock()
	if g.tags == nil {
		g.tags = map[string][]TagRef{}
	}
	g.tags[project] = all
	g.mu.Unlock()
	return all, nil
}

// Candidates lists tagged versions, highest first, or the default branch.
func (g *GitLab) Candidates(ctx context.Context, name string, s spec.Spec) ([]Candidate, error) {
	tags, err := g.listTags(ctx, s.Repo)
	if err != nil {
		return nil, err
	}
	if out := tagCandidates(tags, name, s.Subdir); len(out) > 0 {
		return out, nil
	}
	g.mu.Lock()
	if c, ok := g.heads[s.Repo]; ok {
		g.mu.Unlock()
		return []Candidate{c}, nil
	}
	g.mu.Unlock()
	var info struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := g.getJSON(ctx, g.api(s.Repo, ""), s.Repo, &info); err != nil {
		return nil, err
	}
	if info.DefaultBranch == "" {
		return nil, fmt.Errorf("gitlab %s: the project has no commits", s.Repo)
	}
	var commit struct {
		ID string `json:"id"`
	}
	if err := g.getJSON(ctx, g.api(s.Repo, "/repository/commits/"+url.PathEscape(info.DefaultBranch)), s.Repo, &commit); err != nil {
		return nil, err
	}
	if len(commit.ID) < 12 {
		return nil, fmt.Errorf("gitlab %s: bad commit response", s.Repo)
	}
	c := Candidate{Version: "0.0.0-g" + commit.ID[:12], Commit: commit.ID, Head: true}
	g.mu.Lock()
	if g.heads == nil {
		g.heads = map[string]Candidate{}
	}
	g.heads[s.Repo] = c
	g.mu.Unlock()
	return []Candidate{c}, nil
}

// Manifest reads gtr.json at the candidate's commit.
func (g *GitLab) Manifest(ctx context.Context, s spec.Spec, c Candidate) (*manifest.Manifest, error) {
	p := "gtr.json"
	if s.Subdir != "" {
		p = s.Subdir + "/gtr.json"
	}
	resp, err := g.get(ctx, g.api(s.Repo, "/repository/files/"+url.PathEscape(p)+"/raw?ref="+url.QueryEscape(c.Commit)), s.Repo)
	var nf *gitlabNotFound
	if errors.As(err, &nf) {
		return nil, fmt.Errorf("gitlab:%s %s: %w", s.Repo, c.Version, ErrNoManifest)
	}
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return manifest.Parse(data, "gitlab:"+s.Repo+"/"+p)
}

// Fetch downloads the project archive at the candidate's commit.
func (g *GitLab) Fetch(ctx context.Context, s spec.Spec, c Candidate, dst string) error {
	resp, err := g.get(ctx, g.api(s.Repo, "/repository/archive.zip?sha="+url.QueryEscape(c.Commit)), s.Repo)
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
	_, err = io.Copy(f, io.LimitReader(resp.Body, 1<<30))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("gitlab %s: download: %w", s.Repo, err)
	}
	return placeArchive(zipPath, work, s.Subdir, dst, "gitlab:"+s.Repo, c.Version)
}
