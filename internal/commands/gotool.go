package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gtr-manager/internal/goenv"
	"gtr-manager/internal/install"
	"gtr-manager/internal/manifest"
)

// goCommand prepares `go args...` in dir with the environment gtr gives the
// go command there (ADR-0001; a workspace: ADR-0009), like a script run by
// gtr run. extra are KEY=VALUE pairs added on top (GOOS, CGO_ENABLED).
func goCommand(ctx context.Context, dir string, extra []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, Stdout, Stderr
	cmd.Env = os.Environ()
	if abs, err := filepath.Abs(dir); err == nil {
		cmd.Env = append(withoutKey(cmd.Env, "PWD"), "PWD="+abs) // see runScript
	}
	if work := goenv.FindWork(dir); work != "" {
		warnStaleWork(dir, work)
		cmd.Env = goenv.Env(cmd.Env, work)
	} else if goenv.Generated(dir) {
		cmd.Env = goenv.Env(cmd.Env, "")
	} else {
		warnNotInstalled(dir)
	}
	for _, kv := range extra {
		k, _, _ := strings.Cut(kv, "=")
		cmd.Env = append(withoutKey(cmd.Env, k), kv)
	}
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

// goOutput runs `go args...` in dir and returns its trimmed standard output.
func goOutput(dir string, args ...string) (string, error) {
	cmd := goCommand(context.Background(), dir, nil, args...)
	cmd.Stdout, cmd.Stderr = nil, nil
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return "", fmt.Errorf("go %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("go %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// pkgDir is a package gtr build/test/version act on: the project, or a
// workspace member.
type pkgDir struct {
	Dir   string // absolute
	Label string // "." or the member directory ("drivers/mysql")
	M     *manifest.Manifest
}

// packages returns what a command acts on from the current directory: the
// nearest project or member; in a workspace root, or with recursive, every
// member (dependencies first) and the root itself if it has Go code.
func packages(recursive bool) ([]pkgDir, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	dir := findManifestDir(wd)
	if dir == "" {
		return nil, fmt.Errorf("no gtr.json in %s or above; create one with `gtr init`", wd)
	}
	root, member, err := install.FindRoot(dir)
	if err != nil {
		return nil, err
	}
	rm, err := manifest.Load(filepath.Join(root, manifest.FileName))
	if err != nil {
		return nil, err
	}
	members, err := install.Members(root, rm, nil)
	if err != nil {
		return nil, err
	}
	if len(members) == 0 || (!recursive && member != "") {
		m, err := manifest.Load(filepath.Join(dir, manifest.FileName))
		if err != nil {
			return nil, err
		}
		label := "."
		if member != "" {
			label = member
		}
		return []pkgDir{{Dir: dir, Label: label, M: m}}, nil
	}
	var out []pkgDir
	for _, mem := range memberOrder(members) {
		out = append(out, pkgDir{Dir: mem.Abs, Label: mem.Dir, M: mem.Manifest})
	}
	if own, err := install.HasOwnGo(root, members); err != nil {
		return nil, err
	} else if own {
		out = append(out, pkgDir{Dir: root, Label: ".", M: rm})
	}
	return out, nil
}

// watchSkip are directories never watched: dependencies and build output.
var watchSkip = map[string]bool{install.ModulesDir: true, "build": true, "dist": true, "vendor": true, "node_modules": true}

type stamp struct {
	mod  time.Time
	size int64
}

// snapshot records the Go sources and project files under dirs.
func snapshot(dirs []string) map[string]stamp {
	out := map[string]stamp{}
	for _, d := range dirs {
		filepath.WalkDir(d, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			name := e.Name()
			if e.IsDir() {
				// build/, dist/, vendor/ only at the top: internal/build is code.
				if p != d && (strings.HasPrefix(name, ".") || name == install.ModulesDir || name == "node_modules" ||
					watchSkip[name] && filepath.Dir(p) == d) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(name, ".go") || name == manifest.FileName || name == "go.mod" || name == "go.work" ||
				strings.HasPrefix(name, ".env") || strings.HasSuffix(name, ".tmpl") || strings.HasSuffix(name, ".html") {
				if info, err := e.Info(); err == nil {
					out[p] = stamp{info.ModTime(), info.Size()}
				}
			}
			return nil
		})
	}
	return out
}

// changed lists the paths that differ between two snapshots.
func changed(a, b map[string]stamp) []string {
	var out []string
	for p, s := range b {
		if a[p] != s {
			out = append(out, p)
		}
	}
	for p := range a {
		if _, ok := b[p]; !ok {
			out = append(out, p)
		}
	}
	return out
}

// pollInterval is how often gtr dev and gtr test --watch look for changes.
var pollInterval = 400 * time.Millisecond

// waitForChange blocks until a file under dirs changes (and stays unchanged
// for one more interval, so an editor's save settles) or ctx is done.
// before is the snapshot taken when the last build or test run started, so
// edits made while it ran count.
func waitForChange(ctx context.Context, dirs []string, before map[string]stamp) ([]string, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
		now := snapshot(dirs)
		if diff := changed(before, now); len(diff) > 0 {
			for {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(pollInterval):
				}
				settled := snapshot(dirs)
				if len(changed(now, settled)) == 0 {
					return changed(before, settled), nil
				}
				now = settled
			}
		}
	}
}

// relPaths shortens paths for messages.
func relPaths(base string, paths []string) string {
	var out []string
	for i, p := range paths {
		if i == 3 {
			out = append(out, fmt.Sprintf("and %d more", len(paths)-3))
			break
		}
		if r, err := filepath.Rel(base, p); err == nil {
			p = r
		}
		out = append(out, filepath.ToSlash(p))
	}
	return strings.Join(out, ", ")
}

// warnStaleWork tells when the package in dir has a gtr.json but is not in
// go.work: a workspace member added since the last `gtr install`, which go
// would reject with "directory prefix . does not contain modules listed in
// go.work".
func warnStaleWork(dir, work string) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return
	}
	pkg := findManifestDir(abs)
	root := filepath.Dir(work)
	if pkg == "" || pkg == root || !strings.HasPrefix(pkg, root+string(filepath.Separator)) {
		return
	}
	data, err := os.ReadFile(work)
	if err != nil {
		return
	}
	for _, use := range workUses(string(data)) {
		if filepath.Clean(filepath.Join(root, filepath.FromSlash(use))) == pkg {
			return
		}
	}
	rel, _ := filepath.Rel(root, pkg)
	fmt.Fprintf(Stderr, "gtr: %s is not in go.work (a new workspace member?); run `gtr install` in %s\n", filepath.ToSlash(rel), root)
}

// workUses lists the directories of the use directives of a go.work.
func workUses(text string) []string {
	var out []string
	inBlock := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case inBlock && line == ")":
			inBlock = false
		case inBlock && line != "":
			out = append(out, strings.Trim(line, `"`))
		case line == "use (":
			inBlock = true
		case strings.HasPrefix(line, "use "):
			out = append(out, strings.Trim(strings.TrimSpace(line[4:]), `"`))
		}
	}
	return out
}

// warnNotInstalled tells when a gtr package has neither a go.mod nor a
// go.work above it: `gtr install` has not run yet, and go would fail with
// "go.mod file not found".
func warnNotInstalled(dir string) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return
	}
	pkg := findManifestDir(abs)
	if pkg == "" {
		return
	}
	if _, err := os.Stat(filepath.Join(pkg, "go.mod")); err == nil {
		return // a go.mod of its own (committed, or not made by gtr)
	}
	where := pkg
	if root, member, err := install.FindRoot(pkg); err == nil && member != "" {
		where = root
	}
	fmt.Fprintf(Stderr, "gtr: dependencies are not installed (no go.mod or go.work); run `gtr install` in %s\n", where)
}
