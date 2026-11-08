package source

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"gtr-manager/internal/manifest"
	"gtr-manager/internal/spec"
)

// Git reads packages from any git server with the git command
// (git+https://, git+ssh://, git+file://). Authentication is git's own:
// credential helpers, SSH keys. Versions are tags (TagVersion); a
// repository without version tags offers its default branch.
//
// Objects are fetched shallowly, one commit at a time, into bare
// repositories under Cache (one per URL), and packages are taken from there
// with `git archive`.
type Git struct {
	Bin   string // git executable; "git" if empty
	Cache string // ~/.gtr/cache/git
	Tmp   string

	mu    sync.Mutex
	refs  map[string]*remoteRefs
	locks map[string]*sync.Mutex
}

type remoteRefs struct {
	tags []TagRef
	head string // commit of HEAD
}

// NewGit returns a git source with its cache under home.
func NewGit(cache, tmp string) *Git { return &Git{Cache: cache, Tmp: tmp} }

func (g *Git) run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	bin := g.Bin
	if bin == "" {
		bin = "git"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	// Never wait for a password on the terminal: gtr may run in CI or under
	// an IDE. Credential helpers and SSH agents still work.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_ASKPASS=", "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if errors.Is(err, exec.ErrNotFound) || cmd.ProcessState == nil && errors.Is(err, fs.ErrNotExist) {
		return nil, errors.New("git sources need the git command; install git or use a github:/gitlab: source")
	}
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err() // killed because the operation was cancelled
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(msg, "\n"); i >= 0 && len(msg) > 400 {
			msg = msg[i+1:]
		}
		return nil, &gitError{args: args, msg: msg, err: err}
	}
	return stdout.Bytes(), nil
}

type gitError struct {
	args []string
	msg  string
	err  error
}

func (e *gitError) Unwrap() error { return e.err }

func (e *gitError) Error() string {
	if e.msg != "" {
		return "git " + e.args[0] + ": " + e.msg
	}
	return "git " + e.args[0] + ": " + e.err.Error()
}

func (g *Git) remote(ctx context.Context, u string) (*remoteRefs, error) {
	g.mu.Lock()
	if r, ok := g.refs[u]; ok {
		g.mu.Unlock()
		return r, nil
	}
	g.mu.Unlock()
	out, err := g.run(ctx, "", "ls-remote", u, "HEAD", "refs/tags/*")
	if err != nil {
		return nil, fmt.Errorf("git+%s: %w", u, err)
	}
	r := &remoteRefs{}
	peeled := map[string]string{}
	var order []string
	direct := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		sha, ref, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok || len(sha) < 40 {
			continue
		}
		switch {
		case ref == "HEAD":
			r.head = sha
		case strings.HasPrefix(ref, "refs/tags/") && strings.HasSuffix(ref, "^{}"):
			peeled[strings.TrimSuffix(strings.TrimPrefix(ref, "refs/tags/"), "^{}")] = sha
		case strings.HasPrefix(ref, "refs/tags/"):
			name := strings.TrimPrefix(ref, "refs/tags/")
			order = append(order, name)
			direct[name] = sha
		}
	}
	for _, name := range order {
		commit := direct[name]
		if p, ok := peeled[name]; ok { // an annotated tag: the commit it points at
			commit = p
		}
		r.tags = append(r.tags, TagRef{Name: name, Commit: commit})
	}
	g.mu.Lock()
	if g.refs == nil {
		g.refs = map[string]*remoteRefs{}
	}
	g.refs[u] = r
	g.mu.Unlock()
	return r, nil
}

// Candidates lists tagged versions, highest first, or the default branch.
func (g *Git) Candidates(ctx context.Context, name string, s spec.Spec) ([]Candidate, error) {
	r, err := g.remote(ctx, s.URL)
	if err != nil {
		return nil, err
	}
	if out := tagCandidates(r.tags, name, s.Subdir); len(out) > 0 {
		return out, nil
	}
	if r.head == "" {
		return nil, fmt.Errorf("git+%s: the repository has no commits", s.URL)
	}
	return []Candidate{{Version: "0.0.0-g" + r.head[:12], Commit: r.head, Head: true}}, nil
}

// repo returns the bare cache repository for a URL, holding its lock.
func (g *Git) repo(ctx context.Context, u string) (string, func(), error) {
	sum := sha256.Sum256([]byte(u))
	dir := filepath.Join(g.Cache, hex.EncodeToString(sum[:8])+".git")
	g.mu.Lock()
	if g.locks == nil {
		g.locks = map[string]*sync.Mutex{}
	}
	l, ok := g.locks[dir]
	if !ok {
		l = &sync.Mutex{}
		g.locks[dir] = l
	}
	g.mu.Unlock()
	l.Lock()
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		if err := os.MkdirAll(g.Cache, 0o755); err != nil {
			l.Unlock()
			return "", nil, err
		}
		if _, err := g.run(ctx, "", "init", "--bare", "-q", dir); err != nil {
			l.Unlock()
			return "", nil, err
		}
	}
	return dir, l.Unlock, nil
}

// ensure fetches the candidate's commit into the cache repository.
func (g *Git) ensure(ctx context.Context, s spec.Spec, c Candidate) (string, func(), error) {
	dir, unlock, err := g.repo(ctx, s.URL)
	if err != nil {
		return "", nil, err
	}
	if _, err := g.run(ctx, dir, "cat-file", "-e", c.Commit+"^{commit}"); err == nil {
		return dir, unlock, nil
	}
	ref := "HEAD"
	if c.Tag != "" {
		ref = "refs/tags/" + c.Tag
	}
	if _, err := g.run(ctx, dir, "fetch", "-q", "--depth=1", "--no-tags", s.URL, ref); err != nil {
		unlock()
		return "", nil, fmt.Errorf("git+%s: %w", s.URL, err)
	}
	if _, err := g.run(ctx, dir, "cat-file", "-e", c.Commit+"^{commit}"); err != nil {
		unlock()
		return "", nil, fmt.Errorf("git+%s: %s no longer points at %s (the tag was moved or the branch advanced); run the command again", s.URL, ref, c.Commit)
	}
	return dir, unlock, nil
}

// Manifest reads gtr.json at the candidate's commit.
func (g *Git) Manifest(ctx context.Context, s spec.Spec, c Candidate) (*manifest.Manifest, error) {
	dir, unlock, err := g.ensure(ctx, s, c)
	if err != nil {
		return nil, err
	}
	defer unlock()
	p := "gtr.json"
	if s.Subdir != "" {
		p = s.Subdir + "/gtr.json"
	}
	data, err := g.run(ctx, dir, "show", c.Commit+":"+p)
	if err != nil {
		var ge *gitError
		if errors.As(err, &ge) && (strings.Contains(ge.msg, "does not exist") || strings.Contains(ge.msg, "exists on disk, but not in")) {
			return nil, fmt.Errorf("git+%s %s: %w", s.URL, c.Version, ErrNoManifest)
		}
		return nil, err
	}
	return manifest.Parse(data, "git+"+s.URL+"/"+p)
}

// Fetch exports the candidate's tree with git archive.
func (g *Git) Fetch(ctx context.Context, s spec.Spec, c Candidate, dst string) error {
	dir, unlock, err := g.ensure(ctx, s, c)
	if err != nil {
		return err
	}
	defer unlock()
	if err := os.MkdirAll(g.Tmp, 0o755); err != nil {
		return err
	}
	work, err := os.MkdirTemp(g.Tmp, "fetch-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	zipPath := filepath.Join(work, "src.zip")
	if _, err := g.run(ctx, dir, "archive", "--format=zip", "--prefix=pkg/", "-o", zipPath, c.Commit); err != nil {
		return fmt.Errorf("git+%s: %w", s.URL, err)
	}
	return placeArchive(zipPath, work, s.Subdir, dst, "git+"+s.URL, c.Version)
}
