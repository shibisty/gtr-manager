package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gtr-manager/internal/dirhash"
	"gtr-manager/internal/lock"
	"gtr-manager/internal/manifest"
	"gtr-manager/internal/semver"
	"gtr-manager/internal/source"
	"gtr-manager/internal/spec"
	"gtr-manager/internal/store"
)

// ErrOffline means something is not in the store or the cache and --offline
// forbids downloading it.
var ErrOffline = errors.New("not available offline")

// storeSource serves git packages (github:, gitlab:, git+https:) from the
// store when installing --offline. A version is offered if the store has
// it; its commit is known when gtr.lock recorded it.
type storeSource struct {
	root string // ~/.gtr/store
	lock *lock.Lock

	once  sync.Once
	byPkg map[string][]storedVersion
	err   error
}

type storedVersion struct {
	version string
	dir     string
	hash8   string
	mtime   int64
	sources []string // where it was downloaded from (store.Sources)
}

// offlineTag marks a candidate served from the store; the rest is its
// directory.
const offlineTag = "store:"

func (s *storeSource) scan() {
	s.byPkg = map[string][]storedVersion{}
	entries, err := os.ReadDir(filepath.Join(s.root, "gtr"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.err = err
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		at := strings.Index(e.Name(), "@")
		dash := strings.LastIndex(e.Name(), "-")
		if at <= 0 || dash <= at {
			continue
		}
		name, version, hash8 := e.Name()[:at], e.Name()[at+1:dash], e.Name()[dash+1:]
		if _, err := semver.Parse(version); err != nil {
			continue
		}
		var mtime int64
		if info, err := e.Info(); err == nil {
			mtime = info.ModTime().UnixNano()
		}
		dir := filepath.Join(s.root, "gtr", e.Name())
		s.byPkg[name] = append(s.byPkg[name], storedVersion{version: version, dir: dir, hash8: hash8, mtime: mtime, sources: store.Sources(dir)})
	}
}

// nameOf finds the package name for a source when the caller does not know
// it (gtr add github:o/r --offline): from gtr.lock.
func (s *storeSource) nameOf(sp spec.Spec) string {
	if s.lock == nil {
		return ""
	}
	src := sourceString(sp)
	for _, name := range sortedKeys(s.lock.Packages) {
		if s.lock.Packages[name].Source == src {
			return name
		}
	}
	return ""
}

func (s *storeSource) Candidates(_ context.Context, name string, sp spec.Spec) ([]source.Candidate, error) {
	s.once.Do(s.scan)
	if s.err != nil {
		return nil, s.err
	}
	if name == "" {
		if name = s.nameOf(sp); name == "" {
			return nil, fmt.Errorf("the package name of %s is unknown: it is not in gtr.lock, and the store is indexed by name; %w (run the command once with the network)", sp.String(), ErrOffline)
		}
	}
	var locked lock.Entry
	if s.lock != nil {
		locked = s.lock.Packages[name]
		if locked.Source != "" && locked.Source != sourceString(sp) {
			locked = lock.Entry{} // gtr.lock has it from another source
		}
	}
	// A directory qualifies if it was downloaded from this source, or (stores
	// filled before sources were recorded) if it is the tree gtr.lock pins.
	src := sourceString(sp)
	isLocked := func(sv storedVersion) bool {
		return sv.version == locked.Version && locked.Integrity != "" && sv.hash8 == dirhash.Short(locked.Integrity)
	}
	best := map[string]storedVersion{}
	for _, sv := range s.byPkg[name] {
		if !contains(sv.sources, src) && !isLocked(sv) {
			continue
		}
		cur, seen := best[sv.version]
		switch {
		case !seen:
			best[sv.version] = sv
		case isLocked(sv):
			best[sv.version] = sv // the tree gtr.lock recorded
		case !isLocked(cur) && sv.mtime > cur.mtime:
			best[sv.version] = sv
		}
	}
	if len(best) == 0 {
		return nil, fmt.Errorf("%s is not in the store: %w (run the command once with the network)", name, ErrOffline)
	}
	var out []source.Candidate
	for v, sv := range best {
		c := source.Candidate{Version: v, Tag: offlineTag + sv.dir, Head: strings.HasPrefix(v, "0.0.0-")}
		if v == locked.Version {
			c.Commit = locked.Commit
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := semver.Parse(out[i].Version)
		b, _ := semver.Parse(out[j].Version)
		return semver.Compare(a, b) > 0
	})
	return out, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (s *storeSource) dir(c source.Candidate) (string, error) {
	dir, ok := strings.CutPrefix(c.Tag, offlineTag)
	if !ok {
		return "", fmt.Errorf("version %s: %w", c.Version, ErrOffline)
	}
	return dir, nil
}

func (s *storeSource) Manifest(_ context.Context, sp spec.Spec, c source.Candidate) (*manifest.Manifest, error) {
	dir, err := s.dir(c)
	if err != nil {
		return nil, err
	}
	m, err := manifest.Load(filepath.Join(dir, manifest.FileName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, source.ErrNoManifest
	}
	return m, err
}

// Fetch copies the stored tree (without the generated go.mod); the store
// recognizes it by its hash and keeps using the existing directory.
func (s *storeSource) Fetch(_ context.Context, sp spec.Spec, c source.Candidate, dst string) error {
	dir, err := s.dir(c)
	if err != nil {
		return err
	}
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if store.GeneratedFiles[filepath.ToSlash(rel)] || !d.Type().IsRegular() {
			return nil
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm()|0o200)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}
