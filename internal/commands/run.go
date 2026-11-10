package commands

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"gtr-manager/internal/goenv"
	"gtr-manager/internal/install"
	"gtr-manager/internal/manifest"
)

// Run runs a script from gtr.json ("start" by default). Arguments after the
// script name (and after an optional "--") are passed to the script:
//
//	gtr-manager run test -- -run TestX
//	gtr-manager run -r test       in every workspace member that has "test"
//	gtr-manager run --no-env dev  without loading .env files
//
// The script runs in the directory of the nearest gtr.json. "pre<name>" runs
// before it and "post<name>" after it when gtr.json defines them (the
// arguments go to the script itself only). Variables from .env and
// .env.local in the project directory (in a workspace member: the root's,
// then the member's) are added to the environment; variables already set
// win.
//
// In a project whose go.mod gtr generated, the go command gets GOPROXY=off,
// GOWORK=off and -mod=mod (in a workspace: GOWORK=<root>/go.work, see
// goenv.Env), and its errors get gtr hints.
//
// If the script fails, an *exec.ExitError is returned and main exits with
// the same code.
func Run(args []string) error {
	recursive, dotenv := false, true
	for len(args) > 0 {
		switch args[0] {
		case "-r", "--recursive":
			recursive = true
		case "--no-env":
			dotenv = false
		default:
			goto parsed
		}
		args = args[1:]
	}
parsed:
	name := "start"
	if len(args) > 0 && args[0] != "--" {
		name, args = args[0], args[1:]
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if recursive {
		return runRecursive(name, args, dotenv)
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	dir := findManifestDir(wd)
	if dir == "" {
		return fmt.Errorf("no gtr.json in %s or above; create one with `gtr init`", wd)
	}
	m, err := manifest.Load(filepath.Join(dir, manifest.FileName))
	if err != nil {
		return err
	}
	if strings.TrimSpace(m.Scripts[name]) == "" {
		// No such script: a gtr command of that name is what was meant
		// (gtr run test → gtr test), as npm runs its own test without a script.
		if fn, ok := builtinScripts()[name]; ok {
			fmt.Fprintf(Stderr, "gtr: no script %q in gtr.json, running `gtr %s`\n", name, name)
			return fn(args)
		}
		msg := fmt.Sprintf("script %q is not defined in %s", name, filepath.Join(dir, manifest.FileName))
		if s := closest(name, scriptNames(m.Scripts)); s != "" {
			msg += fmt.Sprintf("; did you mean %q?", s)
		}
		return errors.New(msg + available(m.Scripts))
	}
	var env []string
	if dotenv {
		if env, err = loadDotenv(envDirs(dir)...); err != nil {
			return err
		}
	}
	runDir := dir
	if runDir == wd {
		runDir = "."
	}
	return runWithHooks(runDir, m.Scripts, name, args, env, "")
}

// runWithHooks runs pre<name>, name (with args) and post<name>.
func runWithHooks(dir string, scripts map[string]string, name string, args, env []string, label string) error {
	for _, step := range []struct {
		script string
		args   []string
	}{{"pre" + name, nil}, {name, args}, {"post" + name, nil}} {
		command := strings.TrimSpace(scripts[step.script])
		if command == "" {
			continue
		}
		if label != "" {
			fmt.Fprintf(Stderr, "[%s] ", label)
		}
		if err := runScript(dir, command, step.args, env); err != nil {
			if step.script != name {
				return fmt.Errorf("%s: %w", step.script, err)
			}
			return err
		}
	}
	return nil
}

// findManifestDir returns the directory of the nearest gtr.json at or above
// dir, or "".
func findManifestDir(dir string) string {
	for {
		if st, err := os.Stat(filepath.Join(dir, manifest.FileName)); err == nil && !st.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// envDirs lists the directories whose .env files apply to a script in dir:
// the workspace root first (if dir is a member), then dir.
func envDirs(dir string) []string {
	root, member, err := install.FindRoot(dir)
	if err != nil || member == "" || filepath.Clean(root) == filepath.Clean(dir) {
		return []string{dir}
	}
	return []string{root, dir}
}

// runRecursive runs a script in every workspace member that defines it,
// members before the members that depend on them (ADR-0009, item 6).
func runRecursive(name string, args []string, dotenv bool) error {
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, _, err := install.FindRoot(wd)
	if err != nil {
		return err
	}
	rm, err := manifest.Load(filepath.Join(root, manifest.FileName))
	if err != nil {
		return err
	}
	members, err := install.Members(root, rm, nil)
	if err != nil {
		return err
	}
	if len(members) == 0 {
		return fmt.Errorf("gtr run -r runs a script in every workspace member, but %s is not in a workspace", wd)
	}
	ran := 0
	for _, mem := range memberOrder(members) {
		if strings.TrimSpace(mem.Manifest.Scripts[name]) == "" {
			continue
		}
		var env []string
		if dotenv {
			if env, err = loadDotenv(root, mem.Abs); err != nil {
				return err
			}
		}
		if err := runWithHooks(mem.Abs, mem.Manifest.Scripts, name, args, env, mem.Dir); err != nil {
			return fmt.Errorf("%s: %w", mem.Dir, err)
		}
		ran++
	}
	if ran == 0 {
		return fmt.Errorf("no workspace member defines script %q", name)
	}
	return nil
}

// memberOrder sorts members so that a member comes after the members it
// depends on; otherwise by directory.
func memberOrder(members map[string]*install.Member) []*install.Member {
	var dirs []string
	byDir := map[string]*install.Member{}
	for _, m := range members {
		dirs = append(dirs, m.Dir)
		byDir[m.Dir] = m
	}
	sort.Strings(dirs)
	var out []*install.Member
	state := map[string]int{} // 1 visiting, 2 done
	var visit func(m *install.Member)
	visit = func(m *install.Member) {
		if state[m.Manifest.Name] != 0 { // done, or a cycle: keep the order found
			return
		}
		state[m.Manifest.Name] = 1
		var deps []string
		for _, d := range []map[string]string{m.Manifest.Dependencies, m.Manifest.DevDependencies, m.Manifest.PeerDependencies} {
			for dn := range d {
				if dep, ok := members[dn]; ok {
					deps = append(deps, dep.Dir)
				}
			}
		}
		sort.Strings(deps)
		for _, d := range deps {
			visit(byDir[d])
		}
		state[m.Manifest.Name] = 2
		out = append(out, m)
	}
	for _, d := range dirs {
		visit(byDir[d])
	}
	return out
}

// withoutKey drops key from an environment list.
func withoutKey(env []string, key string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); k != key {
			out = append(out, kv)
		}
	}
	return out
}

// runScript runs command (plus args) in dir; extra are variables from .env
// files, added unless the environment already has them.
func runScript(dir, command string, args, extra []string) error {
	for _, a := range args {
		command += " " + quote(a)
	}

	cmd := shellCommand(command)
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = Stdin, Stdout, Stderr
	cmd.Env = withDefaults(os.Environ(), extra)
	if dir != "." {
		// The go command takes its working directory from $PWD when it names
		// the directory it runs in, and from the resolved path otherwise. GOWORK
		// is found by walking up from dir as written, so give the script the
		// same spelling: with a symlink in the path (macOS /var → /private/var)
		// the go.work members would otherwise not contain the directory.
		if abs, err := filepath.Abs(dir); err == nil {
			cmd.Env = append(withoutKey(cmd.Env, "PWD"), "PWD="+abs)
		}
	}
	if work := goenv.FindWork(dir); work != "" {
		warnStaleWork(dir, work)
		cmd.Env = goenv.Env(cmd.Env, work) // a workspace member or root (ADR-0009)
	} else if goenv.Generated(dir) {
		cmd.Env = goenv.Env(cmd.Env, "")
	} else {
		warnNotInstalled(dir)
	}
	// Explain go errors in gtr terms, unless stderr is a terminal: a script
	// may run a program that behaves differently when it is not.
	var hints *goenv.Hints
	if f, ok := Stderr.(*os.File); !ok || !goenv.IsTerminal(f) {
		hints = goenv.NewHints(Stderr)
		cmd.Stderr = hints
		if Stdout == Stderr {
			cmd.Stdout = hints.Unfiltered()
		}
	}
	// A background process that keeps the hint pipe open must not keep
	// gtr waiting after the script exits.
	cmd.WaitDelay = 500 * time.Millisecond
	// Ctrl+C reaches the script from the terminal; gtr stays alive until it
	// exits, so its last output still passes through the hints pipe. (Not
	// signal.Ignore: the child would inherit an ignored SIGINT.)
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	fmt.Fprintf(Stderr, "> %s\n\n", command)
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil // the script succeeded; only its background output was cut off
	}
	if hints != nil {
		hints.Close()
	}
	return err
}

// builtinScripts are the gtr commands that `gtr run <name>` falls back to
// when gtr.json has no script of that name.
func builtinScripts() map[string]func([]string) error {
	return map[string]func([]string) error{
		"test": Test, "build": Build, "dev": Dev, "install": Install, "ci": CI,
	}
}

// HasScript reports whether the nearest gtr.json (at or above the working
// directory) defines a script: `gtr migrate` runs the script "migrate".
func HasScript(name string) bool {
	wd, err := os.Getwd()
	if err != nil {
		return false
	}
	dir := findManifestDir(wd)
	if dir == "" {
		return false
	}
	m, err := manifest.Load(filepath.Join(dir, manifest.FileName))
	return err == nil && strings.TrimSpace(m.Scripts[name]) != ""
}

// Suggest returns the closest of candidates to a mistyped name ("" when
// none is close): one or two edits away, or a prefix.
func Suggest(name string, candidates []string) string { return closest(name, candidates) }

func scriptNames(scripts map[string]string) []string {
	names := make([]string, 0, len(scripts))
	for n, c := range scripts {
		if strings.TrimSpace(c) != "" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

func closest(name string, candidates []string) string {
	best, bestDist := "", 3
	for _, c := range candidates {
		d := editDistance(strings.ToLower(name), strings.ToLower(c))
		if len(name) >= 3 && strings.HasPrefix(c, name) && d > 1 {
			d = 1 // "inst" → "install"
		}
		if d < bestDist || d == bestDist && c < best {
			best, bestDist = c, d
		}
	}
	if bestDist > 2 || bestDist > len(name)/2 {
		return ""
	}
	return best
}

// editDistance is the Damerau-Levenshtein distance (a swap of two
// neighbours counts as one edit: "tset" → "test").
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	d := make([][]int, len(ra)+1)
	for i := range d {
		d[i] = make([]int, len(rb)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(ra); i++ {
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(ra)][len(rb)]
}

func available(scripts map[string]string) string {
	names := make([]string, 0, len(scripts))
	for n, c := range scripts {
		if strings.TrimSpace(c) != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return "; available: " + strings.Join(names, ", ")
}

// quote escapes an argument for sh or cmd.exe.
func quote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\"'\\$`&|;<>()*?[]{}!#~%^") {
		return s
	}
	if runtime.GOOS == "windows" {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
