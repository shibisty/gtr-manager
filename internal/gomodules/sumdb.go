package gomodules

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"
)

// DefaultSumDB is the public checksum database.
const DefaultSumDB = "https://sum.golang.org"

// SumDB looks up module hashes in the checksum database.
//
// It reads the lookup endpoint over HTTPS and compares the hashes it
// returns with the downloaded files. The transparency-log inclusion proof
// that the go command additionally checks is not verified yet.
type SumDB struct {
	URL     string
	Client  *http.Client
	Private []string // GONOSUMDB/GOPRIVATE patterns: not looked up

	mu    sync.Mutex
	cache map[string][2]string // "path@version" → zip hash, go.mod hash
}

// NewSumDB configures the database from the environment: GTR_GOSUMDB (a URL
// or "off"), else GOSUMDB ("off", "sum.golang.org", "name+key url" or a
// host name), with GONOSUMDB and GOPRIVATE excluded. It returns nil when
// checking is turned off.
func NewSumDB() *SumDB {
	url := os.Getenv("GTR_GOSUMDB")
	if url == "" {
		switch f := strings.Fields(os.Getenv("GOSUMDB")); {
		case len(f) == 0:
			url = DefaultSumDB
		case f[0] == "off":
			url = "off"
		case len(f) >= 2:
			url = f[1]
		default:
			name, _, _ := strings.Cut(f[0], "+")
			url = "https://" + name
		}
	}
	if url == "off" {
		return nil
	}
	return &SumDB{URL: url, Client: &http.Client{Timeout: time.Minute}, Private: patterns(os.Getenv("GONOSUMDB"), os.Getenv("GOPRIVATE"))}
}

// IsPrivate reports whether a module matches GONOSUMDB/GOPRIVATE: a pattern
// matches the path or any prefix of it, element by element, with globs.
func (s *SumDB) IsPrivate(modPath string) bool {
	if s == nil {
		return true
	}
	return matchPatterns(s.Private, modPath)
}

func matchPatterns(pats []string, modPath string) bool {
	elems := strings.Split(modPath, "/")
	for _, pat := range pats {
		pe := strings.Split(strings.TrimSuffix(pat, "/"), "/")
		if len(pe) > len(elems) {
			continue
		}
		match := true
		for i, p := range pe {
			if ok, _ := path.Match(p, elems[i]); !ok {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// Lookup returns the h1 hashes of a module's zip and go.mod.
func (s *SumDB) Lookup(ctx context.Context, modPath, version string) (zip, mod string, err error) {
	key := modPath + "@" + version
	s.mu.Lock()
	if s.cache == nil {
		s.cache = map[string][2]string{}
	}
	if h, ok := s.cache[key]; ok {
		s.mu.Unlock()
		return h[0], h[1], nil
	}
	s.mu.Unlock()
	ep, err := Escape(modPath)
	if err != nil {
		return "", "", err
	}
	ev, err := Escape(version)
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.URL, "/")+"/lookup/"+ep+"@"+ev, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("checksum database: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("checksum database: %s@%s: %s %s (for a private module set GONOSUMDB=%s)",
			modPath, version, resp.Status, strings.TrimSpace(string(body)), modPath)
	}
	for _, line := range strings.Split(string(body), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[0] != modPath {
			continue
		}
		switch f[1] {
		case version:
			zip = f[2]
		case version + "/go.mod":
			mod = f[2]
		}
	}
	if zip == "" || mod == "" {
		return "", "", fmt.Errorf("checksum database: no hashes for %s@%s", modPath, version)
	}
	s.mu.Lock()
	s.cache[key] = [2]string{zip, mod}
	s.mu.Unlock()
	return zip, mod, nil
}

// HashMod returns the h1 hash of a go.mod file as recorded in go.sum.
func HashMod(data []byte) string {
	fileSum := sha256.Sum256(data)
	line := fmt.Sprintf("%x  go.mod\n", fileSum)
	sum := sha256.Sum256([]byte(line))
	return "h1:" + base64.StdEncoding.EncodeToString(sum[:])
}
