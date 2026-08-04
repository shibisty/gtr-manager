package manifest

import (
    "encoding/json"
    "os"
)

type Manifest struct {
    Name         string            `json:"name"`
    Version      string            `json:"version"`
    Author       string            `json:"author"`
    Website      string            `json:"website"`
    Entrypoint   string            `json:"entrypoint"`
    Engine       string            `json:"engine"`
    Dependencies map[string]string `json:"dependencies"`
    Scripts      map[string]string `json:"scripts"`
}

func Save(path string, m *Manifest) error {

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "    ")
	enc.SetEscapeHTML(false)

	if err := enc.Encode(m); err != nil {
		return err
	}

	return nil
}

func Load(path string) (*Manifest, error) {

    data, err := os.ReadFile(path)
    if err != nil {
        return nil, err
    }

    var m Manifest

    if err := json.Unmarshal(data, &m); err != nil {
        return nil, err
    }

    if m.Dependencies == nil {
        m.Dependencies = make(map[string]string)
    }

    if m.Scripts == nil {
        m.Scripts = make(map[string]string)
    }

    return &m, nil
}
