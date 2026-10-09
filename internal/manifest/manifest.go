// Package manifest reads and writes gtr.json.
//
// Fields not covered by Manifest (description, license, custom fields) are
// preserved on save. The file is written atomically: first to a temporary
// file next to it, then renamed, so a failure leaves the old gtr.json intact.
package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// FileName is the manifest file name.
const FileName = "gtr.json"

// Manifest is the content of gtr.json.
type Manifest struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Author       string            `json:"author"`
	Website      string            `json:"website"`
	Entrypoint   string            `json:"entrypoint"`
	Engine       string            `json:"engine"`
	Dependencies map[string]string `json:"dependencies"`
	Scripts      map[string]string `json:"scripts"`

	// Extra holds the remaining fields of the file as is.
	Extra map[string]json.RawMessage `json:"-"`
}

var known = []string{"name", "version", "author", "website", "entrypoint", "engine", "dependencies", "scripts"}

// Load reads a manifest.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s not found: run `gtr-manager init` first", path)
		}
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, k := range known {
		delete(all, k)
	}
	if len(all) > 0 {
		m.Extra = all
	}
	if m.Dependencies == nil {
		m.Dependencies = map[string]string{}
	}
	if m.Scripts == nil {
		m.Scripts = map[string]string{}
	}
	return &m, nil
}

// Marshal returns JSON indented with 4 spaces: known fields first, then the
// rest in alphabetical order.
func Marshal(m *Manifest) ([]byte, error) {
	values := []any{m.Name, m.Version, m.Author, m.Website, m.Entrypoint, m.Engine, nonNil(m.Dependencies), nonNil(m.Scripts)}
	var buf bytes.Buffer
	buf.WriteString("{\n")
	first := true
	field := func(key string, raw []byte) {
		if !first {
			buf.WriteString(",\n")
		}
		first = false
		k, _ := encode(key)
		buf.WriteString("    ")
		buf.Write(k)
		buf.WriteString(": ")
		buf.Write(raw)
	}
	for i, k := range known {
		raw, err := encode(values[i])
		if err != nil {
			return nil, err
		}
		field(k, raw)
	}
	extra := make([]string, 0, len(m.Extra))
	for k := range m.Extra {
		extra = append(extra, k)
	}
	sort.Strings(extra)
	for _, k := range extra {
		var out bytes.Buffer
		if err := json.Indent(&out, m.Extra[k], "    ", "    "); err != nil {
			return nil, fmt.Errorf("field %q: %w", k, err)
		}
		field(k, out.Bytes())
	}
	buf.WriteString("\n}\n")
	return buf.Bytes(), nil
}

// Save writes a manifest atomically.
func Save(path string, m *Manifest) error {
	data, err := Marshal(m)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".gtr.json.*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("    ", "    ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
