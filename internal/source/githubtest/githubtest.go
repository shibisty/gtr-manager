// Package githubtest is a fake GitHub API for tests: repositories with
// tagged commits, file contents and zipballs.
package githubtest

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// Server is a fake GitHub.
type Server struct {
	*httptest.Server
	mu      sync.Mutex
	repos   map[string]*repo
	Zipball int // number of zipball downloads
}

type repo struct {
	branch  string
	commits map[string]map[string]string // sha → path → content
	tags    []tagRef
	head    string
}

type tagRef struct{ name, sha string }

// New starts a fake GitHub that stops when the test ends.
func New(t testing.TB) *Server {
	s := &Server{repos: map[string]*repo{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Commit adds a commit with the given files to owner/repo and makes it the
// branch head; tags (if any) point at it. It returns the commit SHA.
func (s *Server) Commit(fullName string, files map[string]string, tags ...string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.repos[fullName]
	if !ok {
		r = &repo{branch: "main", commits: map[string]map[string]string{}}
		s.repos[fullName] = r
	}
	var keys []string
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha1.New()
	fmt.Fprintf(h, "%s%d", fullName, len(r.commits))
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s;", k, files[k])
	}
	sha := hex.EncodeToString(h.Sum(nil))
	r.commits[sha] = files
	r.head = sha
	for _, t := range tags {
		r.tags = append(r.tags, tagRef{t, sha})
	}
	return sha
}

// MoveTag points an existing tag at a new commit (a "force-pushed" tag).
func (s *Server) MoveTag(fullName, tag string, files map[string]string) {
	sha := s.Commit(fullName, files)
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.repos[fullName]
	for i := range r.tags {
		if r.tags[i].name == tag {
			r.tags[i].sha = sha
		}
	}
}

func (s *Server) serve(w http.ResponseWriter, req *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	parts := strings.Split(strings.TrimPrefix(req.URL.Path, "/repos/"), "/")
	if len(parts) < 2 {
		http.NotFound(w, req)
		return
	}
	r, ok := s.repos[parts[0]+"/"+parts[1]]
	if !ok {
		http.NotFound(w, req)
		return
	}
	rest := parts[2:]
	switch {
	case len(rest) == 0:
		json.NewEncoder(w).Encode(map[string]string{"default_branch": r.branch})
	case rest[0] == "tags":
		if req.URL.Query().Get("page") != "1" {
			w.Write([]byte("[]"))
			return
		}
		var out []map[string]any
		for i := len(r.tags) - 1; i >= 0; i-- {
			out = append(out, map[string]any{"name": r.tags[i].name, "commit": map[string]string{"sha": r.tags[i].sha}})
		}
		json.NewEncoder(w).Encode(out)
	case rest[0] == "commits" && len(rest) == 2 && rest[1] == r.branch:
		json.NewEncoder(w).Encode(map[string]string{"sha": r.head})
	case rest[0] == "contents":
		files, ok := r.commits[req.URL.Query().Get("ref")]
		content, ok2 := files[strings.Join(rest[1:], "/")]
		if !ok || !ok2 {
			http.NotFound(w, req)
			return
		}
		w.Write([]byte(content))
	case rest[0] == "zipball" && len(rest) == 2:
		files, ok := r.commits[rest[1]]
		if !ok {
			http.NotFound(w, req)
			return
		}
		s.Zipball++
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		prefix := strings.ReplaceAll(parts[0]+"-"+parts[1], ".", "-") + "-" + rest[1][:7] + "/"
		for name, content := range files {
			f, _ := zw.Create(prefix + name)
			f.Write([]byte(content))
		}
		zw.Close()
		w.Write(buf.Bytes())
	default:
		http.NotFound(w, req)
	}
}
