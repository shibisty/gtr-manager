// Package manifest reads and writes gtr.json (schema: gtr/schema/gtr.schema.json).
//
// Fields the manifest does not model are kept as they are. Keys are written
// in schema order, unknown keys after them alphabetically. The file is
// written atomically: a temporary file next to it, then a rename.
//
// Files written by older gtr-manager versions are migrated on load, with a
// warning per change: "website" → "homepage", "engine" → "engines.go", and
// "entrypoint" is dropped (the schema has build.entry for that).
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

// Engines are the tool versions a package requires.
type Engines struct {
	Go  string `json:"go,omitempty"`
	Gtr string `json:"gtr,omitempty"`
}

// Manifest is the content of gtr.json.
type Manifest struct {
	Name             string
	Version          string
	Engines          Engines
	Dependencies     map[string]string
	DevDependencies  map[string]string
	PeerDependencies map[string]string
	Scripts          map[string]string

	// Warnings describes migrations applied while loading.
	Warnings []string

	raw map[string]json.RawMessage // every other field, as written
}

// order is the key order of the schema.
var order = []string{"$schema", "name", "version", "description", "keywords", "license", "author",
	"homepage", "repository", "private", "engines", "dependencies", "devDependencies",
	"peerDependencies", "workspaces", "scripts", "build", "test", "env", "gomod"}

// Load reads a manifest.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s not found: run `gtr init` first", path)
		}
		return nil, err
	}
	return Parse(data, path)
}

// Parse decodes a manifest; name is used in error messages.
func Parse(data []byte, name string) (*Manifest, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	m := &Manifest{raw: raw}
	fields := []struct {
		key  string
		dest any
	}{
		{"name", &m.Name}, {"version", &m.Version}, {"engines", &m.Engines},
		{"dependencies", &m.Dependencies}, {"devDependencies", &m.DevDependencies},
		{"peerDependencies", &m.PeerDependencies}, {"scripts", &m.Scripts},
	}
	for _, f := range fields {
		if v, ok := raw[f.key]; ok {
			if err := json.Unmarshal(v, f.dest); err != nil {
				return nil, fmt.Errorf("%s: field %q: %w", name, f.key, err)
			}
			delete(raw, f.key)
		}
	}
	m.migrate()
	for _, p := range []*map[string]string{&m.Dependencies, &m.DevDependencies, &m.PeerDependencies, &m.Scripts} {
		if *p == nil {
			*p = map[string]string{}
		}
	}
	return m, nil
}

// Workspaces returns the member directory patterns of a workspace root
// (ADR-0009), or nil.
func (m *Manifest) Workspaces() ([]string, error) {
	v, ok := m.raw["workspaces"]
	if !ok {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal(v, &out); err != nil {
		return nil, fmt.Errorf(`"workspaces" must be a list of directory patterns: %w`, err)
	}
	return out, nil
}

func (m *Manifest) migrate() {
	if v, ok := m.raw["engine"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil && m.Engines.Go == "" {
			m.Engines.Go = s
			m.Warnings = append(m.Warnings, fmt.Sprintf(`"engine": %q moved to "engines": {"go": %q}`, s, s))
		}
		delete(m.raw, "engine")
	}
	if v, ok := m.raw["website"]; ok {
		if _, has := m.raw["homepage"]; !has && string(v) != `""` {
			m.raw["homepage"] = v
			m.Warnings = append(m.Warnings, `"website" renamed to "homepage"`)
		}
		delete(m.raw, "website")
	}
	if v, ok := m.raw["entrypoint"]; ok {
		if string(v) != `""` && string(v) != `"main.go"` {
			m.Warnings = append(m.Warnings, fmt.Sprintf(`"entrypoint": %s removed; use "build": {"entry": "./cmd/..."} instead`, v))
		}
		delete(m.raw, "entrypoint")
	}
	if v, ok := m.raw["author"]; ok && string(v) == `""` {
		delete(m.raw, "author")
	}
}

// Raw returns an unmodelled field as written, or nil.
func (m *Manifest) Raw(key string) json.RawMessage { return m.raw[key] }

// SetRaw sets an unmodelled field (nil removes it).
func (m *Manifest) SetRaw(key string, v any) error {
	if m.raw == nil {
		m.raw = map[string]json.RawMessage{}
	}
	if v == nil {
		delete(m.raw, key)
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	m.raw[key] = data
	return nil
}

// Marshal encodes the manifest with 4-space indentation.
func Marshal(m *Manifest) ([]byte, error) {
	values := map[string]any{}
	values["name"] = m.Name
	values["version"] = m.Version
	if m.Engines != (Engines{}) {
		values["engines"] = m.Engines
	}
	values["dependencies"] = nonNil(m.Dependencies)
	if len(m.DevDependencies) > 0 {
		values["devDependencies"] = m.DevDependencies
	}
	if len(m.PeerDependencies) > 0 {
		values["peerDependencies"] = m.PeerDependencies
	}
	if len(m.Scripts) > 0 {
		values["scripts"] = m.Scripts
	}
	for k, v := range m.raw {
		values[k] = v
	}
	var keys []string
	seen := map[string]bool{}
	for _, k := range order {
		if _, ok := values[k]; ok {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	var extra []string
	for k := range values {
		if !seen[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	keys = append(keys, extra...)

	var buf bytes.Buffer
	buf.WriteString("{\n")
	for i, k := range keys {
		kb, _ := encode(k)
		var vb []byte
		var err error
		if raw, ok := values[k].(json.RawMessage); ok {
			var out bytes.Buffer
			if err = json.Indent(&out, raw, "    ", "    "); err == nil {
				vb = out.Bytes()
			}
		} else {
			vb, err = encode(values[k])
		}
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", k, err)
		}
		buf.WriteString("    ")
		buf.Write(kb)
		buf.WriteString(": ")
		buf.Write(vb)
		if i < len(keys)-1 {
			buf.WriteString(",")
		}
		buf.WriteString("\n")
	}
	buf.WriteString("}\n")
	return buf.Bytes(), nil
}

// Save writes the manifest atomically.
func Save(path string, m *Manifest) error {
	data, err := Marshal(m)
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, data)
}

// WriteFileAtomic writes data to a temporary file next to path and renames it.
func WriteFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after the rename
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
