// Package lock reads and writes gtr.lock (ADR-0004).
package lock

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// FileName is the lock file name.
const FileName = "gtr.lock"

// Version is the current lockfile format.
const Version = 1

// Lock is the content of gtr.lock.
type Lock struct {
	LockfileVersion int              `json:"lockfileVersion"`
	Packages        map[string]Entry `json:"packages"`
	// Workspace lists the member directories of the last install of a
	// workspace (ADR-0009), so files generated for former members can be
	// cleaned up.
	Workspace []string `json:"workspace,omitempty"`
}

// Entry is one resolved package.
type Entry struct {
	Version      string            `json:"version"`
	Source       string            `json:"source"`
	Commit       string            `json:"commit,omitempty"`
	Integrity    string            `json:"integrity,omitempty"`
	GoMod        string            `json:"goMod,omitempty"` // h1 of an external module's go.mod
	Dependencies map[string]string `json:"dependencies,omitempty"`
}

// Load reads gtr.lock; a missing file is an empty lock.
func Load(path string) (*Lock, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Lock{LockfileVersion: Version, Packages: map[string]Entry{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var l Lock
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if l.LockfileVersion > Version {
		return nil, fmt.Errorf("%s was written by a newer gtr (lockfileVersion %d); update gtr-manager", path, l.LockfileVersion)
	}
	if l.Packages == nil {
		l.Packages = map[string]Entry{}
	}
	return &l, nil
}

// Marshal encodes the lock with sorted keys and 2-space indentation.
func Marshal(l *Lock) ([]byte, error) {
	l.LockfileVersion = Version
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
