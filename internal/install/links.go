package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gtr-manager/internal/manifest"
)

// LinksFile is where a project keeps its links (gtr link): package name →
// absolute directory of a local copy used instead of its source. It is
// personal, so .gtr/ is ignored by git; gtr.json and gtr.lock stay as they
// are.
const LinksFile = ".gtr/links.json"

// LoadLinks reads a links file; a missing file is no links.
func LoadLinks(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	links := map[string]string{}
	if err := json.Unmarshal(data, &links); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return links, nil
}

// SaveLinks writes a links file, or removes it when there are none.
func SaveLinks(path string, links map[string]string) error {
	if len(links) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		os.Remove(filepath.Dir(path)) // .gtr/, if now empty
		return nil
	}
	data, err := json.MarshalIndent(links, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return manifest.WriteFileAtomic(path, append(data, '\n'))
}

// links reads the project's links and checks them against the members.
func (in *Installer) links(members map[string]*Member) (map[string]string, error) {
	links, err := LoadLinks(in.path(filepath.FromSlash(LinksFile)))
	if err != nil {
		return nil, err
	}
	for _, name := range sortedKeys(links) {
		if _, member := members[name]; member {
			return nil, fmt.Errorf("%s is a workspace member and cannot be linked; run `gtr unlink %s`", name, name)
		}
		m, err := manifest.Load(filepath.Join(links[name], manifest.FileName))
		if err != nil {
			return nil, fmt.Errorf("link %s → %s: %w; run `gtr unlink %s`", name, links[name], err, name)
		}
		if m.Name != name {
			return nil, fmt.Errorf("link %s → %s: the package there is called %q", name, links[name], m.Name)
		}
	}
	return links, nil
}

func linkNames(links map[string]string) []string {
	names := make([]string, 0, len(links))
	for n := range links {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
