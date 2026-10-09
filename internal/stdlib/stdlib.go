// Package stdlib knows the names taken by the Go standard library: a gtr
// package named like a standard package makes imports ambiguous (ADR-0002,
// item 5). The list comes from `go list std` of the active Go version and
// is cached per version.
package stdlib

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

var (
	mu    sync.Mutex
	cache = map[string]map[string]bool{}
)

// Names returns the first path elements of the standard library of the go
// command in PATH ("errors", "net", "crypto", …). cacheDir, if not empty,
// keeps the list per Go version. It returns (nil, nil) when go is not
// available: the check is skipped rather than blocking work.
func Names(cacheDir string) (map[string]bool, error) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		return nil, nil
	}
	vcmd := exec.Command(goBin, "env", "GOVERSION")
	vcmd.Dir = os.TempDir()
	ver, err := vcmd.Output()
	if err != nil {
		return nil, nil
	}
	version := strings.TrimSpace(string(ver))
	mu.Lock()
	defer mu.Unlock()
	if n, ok := cache[version]; ok {
		return n, nil
	}
	var file string
	if cacheDir != "" && version != "" && !strings.ContainsAny(version, `/\ `) {
		file = filepath.Join(cacheDir, "std-"+version+".txt")
		if data, err := os.ReadFile(file); err == nil {
			n := parse(data)
			cache[version] = n
			return n, nil
		}
	}
	cmd := exec.Command(goBin, "list", "std")
	// Outside any module: a project go.mod must not influence the list.
	cmd.Dir = os.TempDir()
	cmd.Env = append(os.Environ(), "GOFLAGS=", "GOWORK=off", "GO111MODULE=off")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list std: %w", err)
	}
	n := parse(out)
	if file != "" {
		if os.MkdirAll(cacheDir, 0o755) == nil {
			var names []string
			for k := range n {
				names = append(names, k)
			}
			os.WriteFile(file, []byte(strings.Join(names, "\n")+"\n"), 0o644)
		}
	}
	cache[version] = n
	return n, nil
}

func parse(data []byte) map[string]bool {
	n := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		first, _, _ := strings.Cut(strings.TrimSpace(sc.Text()), "/")
		if first != "" {
			n[first] = true
		}
	}
	return n
}

// Check returns an error if name is taken by the standard library.
func Check(name, cacheDir string) error {
	names, err := Names(cacheDir)
	if err != nil || names == nil {
		return err
	}
	if names[name] {
		return fmt.Errorf("the name %q is taken by the Go standard library; imports of it would be ambiguous (ADR-0002)", name)
	}
	return nil
}
