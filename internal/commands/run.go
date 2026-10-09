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
//
// In a project whose go.mod gtr generated, the go command gets GOPROXY=off,
// GOWORK=off and -mod=mod (in a workspace: GOWORK=<root>/go.work, see
// goenv.Env), and its errors get gtr hints.
//
// If the script fails, an *exec.ExitError is returned and main exits with
// the same code.
func Run(args []string) error {
	recursive := false
	if len(args) > 0 && (args[0] == "-r" || args[0] == "--recursive") {
		recursive, args = true, args[1:]
	}
	name := "start"
	if len(args) > 0 {
		name, args = args[0], args[1:]
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if recursive {
		return runRecursive(name, args)
	}
	m, err := manifest.Load(manifest.FileName)
	if err != nil {
		return err
	}
	command := strings.TrimSpace(m.Scripts[name])
	if command == "" {
		return fmt.Errorf("script %q is not defined in gtr.json%s", name, available(m.Scripts))
	}
	return runScript(".", command, args)
}

// runRecursive runs a script in every workspace member that defines it,
// members before the members that depend on them (ADR-0009, item 6).
func runRecursive(name string, args []string) error {
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
		command := strings.TrimSpace(mem.Manifest.Scripts[name])
		if command == "" {
			continue
		}
		fmt.Fprintf(Stderr, "[%s] ", mem.Dir)
		if err := runScript(mem.Abs, command, args); err != nil {
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

// runScript runs command (plus args) in dir.
func runScript(dir, command string, args []string) error {
	for _, a := range args {
		command += " " + quote(a)
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = Stdin, Stdout, Stderr
	cmd.Env = os.Environ()
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
		cmd.Env = goenv.Env(cmd.Env, work) // a workspace member or root (ADR-0009)
	} else if goenv.Generated(dir) {
		cmd.Env = goenv.Env(cmd.Env, "")
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
