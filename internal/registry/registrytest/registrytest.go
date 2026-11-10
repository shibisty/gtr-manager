// Package registrytest is an in-memory gtr registry for tests: the API of
// gtr-registry (/api/v1) over httptest.
package registrytest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"gtr-manager/internal/registry"
	"gtr-manager/internal/semver"
)

// Server is a fake registry. Tokens: "gtr_<email>" for users that exist.
type Server struct {
	*httptest.Server
	mu       sync.Mutex
	pkgs     map[string]*pkg
	Users    map[string]string // email → password
	Requests []string          // "METHOD path"
}

type pkg struct {
	owners   []string
	versions map[string]*version
}

type version struct {
	info    registry.Version
	archive []byte
}

// New starts a fake registry, closed when the test ends.
func New(t testing.TB) *Server {
	s := &Server{pkgs: map[string]*pkg{}, Users: map[string]string{"ann@example.com": "secret"}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Client is a registry client of the server with ann's token.
func (s *Server) Client() *registry.Client {
	return &registry.Client{URL: s.URL, Token: "gtr_ann@example.com"}
}

// Tarball makes a package archive of files (gtr.json among them).
func Tarball(files map[string]string) []byte {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(files[n])), Typeflag: tar.TypeReg})
		tw.Write([]byte(files[n]))
	}
	tw.Close()
	gz.Close()
	return b.Bytes()
}

// Add publishes a version as ann: files must hold gtr.json.
func (s *Server) Add(t testing.TB, files map[string]string) {
	t.Helper()
	var m struct{ Name, Version string }
	if err := json.Unmarshal([]byte(files["gtr.json"]), &m); err != nil {
		t.Fatal(err)
	}
	if err := s.publish("ann@example.com", m.Name, m.Version, Tarball(files)); err != "" {
		t.Fatal(err)
	}
}

// Yank marks a version yanked.
func (s *Server) Yank(name, v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pkgs[name].versions[v].info.Yanked = true
}

func (s *Server) publish(user, name, v string, archive []byte) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return "not gzipped"
	}
	tr := tar.NewReader(gz)
	var manifest []byte
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		if h.Name == "gtr.json" {
			manifest, _ = io.ReadAll(tr)
		}
	}
	var m struct{ Name, Version string }
	if json.Unmarshal(manifest, &m) != nil || m.Name != name || m.Version != v {
		return "the archive's gtr.json does not match"
	}
	p := s.pkgs[name]
	if p == nil {
		p = &pkg{owners: []string{user}, versions: map[string]*version{}}
		s.pkgs[name] = p
	} else if !contains(p.owners, user) {
		return "forbidden"
	}
	if _, ok := p.versions[v]; ok {
		return "exists"
	}
	sum := sha256.Sum256(archive)
	p.versions[v] = &version{info: registry.Version{Version: v, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(archive)),
		PublishedAt: time.Unix(1, 0).UTC(), Manifest: manifest}, archive: archive}
	return ""
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (s *Server) user(r *http.Request) string {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer gtr_")
	if _, exists := s.Users[tok]; ok && exists {
		return tok
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.Requests = append(s.Requests, r.Method+" "+r.URL.Path)
	s.mu.Unlock()
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/"), "/")
	switch {
	case r.Method == "POST" && r.URL.Path == "/api/v1/tokens":
		var in struct{ Email, Password string }
		json.NewDecoder(r.Body).Decode(&in)
		if pw, ok := s.Users[in.Email]; !ok || pw != in.Password {
			fail(w, 401, "These credentials do not match our records.")
			return
		}
		writeJSON(w, 201, map[string]string{"token": "gtr_" + in.Email, "name": "Ann", "email": in.Email})
	case r.Method == "GET" && r.URL.Path == "/api/v1/me":
		if u := s.user(r); u != "" {
			writeJSON(w, 200, map[string]string{"name": "Ann", "email": u})
			return
		}
		fail(w, 401, "an API token is needed")
	case r.Method == "DELETE" && r.URL.Path == "/api/v1/tokens/current":
		w.WriteHeader(204)
	case r.Method == "GET" && r.URL.Path == "/api/v1/search":
		q := r.URL.Query().Get("q")
		s.mu.Lock()
		out := []registry.Summary{}
		for name, p := range s.pkgs {
			if strings.Contains(name, q) {
				out = append(out, registry.Summary{Name: name, Latest: latest(p)})
			}
		}
		s.mu.Unlock()
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		writeJSON(w, 200, map[string]any{"packages": out})
	case len(parts) >= 2 && parts[0] == "packages":
		s.packages(w, r, parts[1:])
	default:
		fail(w, 404, "not found")
	}
}

func latest(p *pkg) string {
	best := ""
	for v, x := range p.versions {
		sv := semver.MustParse(v)
		if x.info.Yanked || sv.Pre != "" {
			continue
		}
		if best == "" || semver.Compare(sv, semver.MustParse(best)) > 0 {
			best = v
		}
	}
	return best
}

func (s *Server) packages(w http.ResponseWriter, r *http.Request, parts []string) {
	name := parts[0]
	s.mu.Lock()
	p := s.pkgs[name]
	s.mu.Unlock()
	switch {
	case len(parts) == 1 && r.Method == "GET":
		if p == nil {
			fail(w, 404, "not found")
			return
		}
		s.mu.Lock()
		ix := registry.Index{Name: name, Latest: latest(p), Owners: p.owners}
		for _, v := range p.versions {
			ix.Versions = append(ix.Versions, v.info)
		}
		s.mu.Unlock()
		sort.Slice(ix.Versions, func(i, j int) bool {
			return semver.Compare(semver.MustParse(ix.Versions[i].Version), semver.MustParse(ix.Versions[j].Version)) > 0
		})
		writeJSON(w, 200, ix)
	case len(parts) == 4 && parts[1] == "versions" && parts[3] == "archive" && r.Method == "GET":
		if p == nil || p.versions[parts[2]] == nil {
			fail(w, 404, "not found")
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		w.Write(p.versions[parts[2]].archive)
	case len(parts) == 3 && parts[1] == "versions" && r.Method == "PUT":
		u := s.user(r)
		if u == "" {
			fail(w, 401, "an API token is needed: run `gtr login`")
			return
		}
		data, _ := io.ReadAll(r.Body)
		switch msg := s.publish(u, name, parts[2], data); msg {
		case "":
			s.mu.Lock()
			info := s.pkgs[name].versions[parts[2]].info
			s.mu.Unlock()
			writeJSON(w, 201, info)
		case "exists":
			fail(w, 409, "this version is published already")
		case "forbidden":
			fail(w, 403, "you are not an owner of this package")
		default:
			fail(w, 422, msg)
		}
	case len(parts) == 4 && parts[1] == "versions" && parts[3] == "yank":
		if s.user(r) == "" {
			fail(w, 401, "an API token is needed")
			return
		}
		if p == nil || p.versions[parts[2]] == nil {
			fail(w, 404, "not found")
			return
		}
		s.mu.Lock()
		p.versions[parts[2]].info.Yanked = r.Method == "POST"
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"yanked": r.Method == "POST"})
	case len(parts) >= 2 && parts[1] == "owners":
		if p == nil {
			fail(w, 404, "not found")
			return
		}
		s.mu.Lock()
		switch r.Method {
		case "PUT":
			var in struct{ Email string }
			json.NewDecoder(r.Body).Decode(&in)
			if !contains(p.owners, in.Email) {
				p.owners = append(p.owners, in.Email)
			}
		case "DELETE":
			if len(p.owners) <= 1 {
				s.mu.Unlock()
				fail(w, 422, "a package keeps at least one owner")
				return
			}
			var keep []string
			for _, o := range p.owners {
				if o != parts[2] {
					keep = append(keep, o)
				}
			}
			p.owners = keep
		}
		owners := append([]string(nil), p.owners...)
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"owners": owners})
	default:
		fail(w, 404, "not found")
	}
}
