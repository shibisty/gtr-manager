package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gtr-manager/internal/install"
	"gtr-manager/internal/manifest"
)

// Dev builds the package, runs it, and rebuilds and restarts it whenever a
// Go file, gtr.json or .env changes (in the package, or in any workspace
// member). Arguments after -- go to the program. A failed build keeps the
// running program and waits for the next change.
//
//	gtr dev                  build.entry of gtr.json (default .)
//	gtr dev -- --port 8080
func Dev(args []string) error {
	var progArgs []string
	dotenv := true
	for i, a := range args {
		switch {
		case a == "--":
			progArgs = args[i+1:]
		case a == "--no-env":
			dotenv = false
			continue
		default:
			return fmt.Errorf("unknown argument %q; usage: gtr dev [--no-env] [-- program arguments]", a)
		}
		break
	}
	pkgs, err := packages(false)
	if err != nil {
		return err
	}
	if len(pkgs) != 1 {
		return errors.New("gtr dev runs one package: run it in the member to develop (the workspace root has no Go code)")
	}
	p := pkgs[0]
	c, err := loadBuildConfig(p.M)
	if err != nil {
		return err
	}
	entry := c.Entry
	if entry == "" {
		entry = "."
	}
	if !strings.HasPrefix(entry, ".") {
		entry = "./" + entry
	}
	name := c.Output
	if name == "" {
		name = p.M.Name
	}
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binDir := filepath.Join(p.Dir, "build", "dev")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	// Watch the package and, in a workspace, the members it is built with.
	watch := []string{p.Dir}
	if root, member, err := install.FindRoot(p.Dir); err == nil && member != "" {
		if rm, err := manifest.Load(filepath.Join(root, manifest.FileName)); err == nil {
			if members, err := install.Members(root, rm, nil); err == nil {
				for _, mem := range members {
					if mem.Abs != p.Dir {
						watch = append(watch, mem.Abs)
					}
				}
			}
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var running *exec.Cmd
	var exited chan error
	build := func(n int) (string, bool) {
		bin := filepath.Join(binDir, fmt.Sprintf("%d-%s", n%2, name)) // alternate: Windows cannot overwrite a running .exe
		args := []string{"build", "-o", bin}
		if len(c.Tags) > 0 {
			args = append(args, "-tags", strings.Join(c.Tags, ","))
		}
		args = append(args, entry)
		cgo := "0"
		if c.Cgo {
			cgo = "1"
		}
		start := time.Now()
		if err := goCommand(ctx, p.Dir, []string{"CGO_ENABLED=" + cgo}, args...).Run(); err != nil {
			if ctx.Err() == nil {
				fmt.Fprintln(Stderr, "gtr: build failed; waiting for changes")
			}
			return "", false
		}
		fmt.Fprintf(Stderr, "gtr: built in %s\n", time.Since(start).Round(10*time.Millisecond))
		return bin, true
	}
	start := func(bin string) error {
		var env []string
		if dotenv {
			if env, err = loadDotenv(envDirs(p.Dir)...); err != nil {
				return err
			}
		}
		cmd := exec.Command(bin, progArgs...)
		cmd.Dir = p.Dir
		cmd.Stdin, cmd.Stdout, cmd.Stderr = Stdin, Stdout, Stderr
		cmd.Env = withDefaults(os.Environ(), env)
		if err := cmd.Start(); err != nil {
			return err
		}
		running = cmd
		exited = make(chan error, 1)
		go func(ch chan error) { ch <- cmd.Wait() }(exited)
		return nil
	}
	stopRunning := func() {
		if running == nil {
			return
		}
		select {
		case <-exited: // already gone
		default:
			if runtime.GOOS != "windows" {
				running.Process.Signal(os.Interrupt)
			} else {
				running.Process.Kill()
			}
			select {
			case <-exited:
			case <-time.After(5 * time.Second):
				running.Process.Kill()
				<-exited
			}
		}
		running = nil
	}
	defer stopRunning()

	// n counts successful builds: the next build writes the file the running
	// program is not using.
	n := 0
	base := snapshot(watch)
	if bin, ok := build(n); ok {
		n++
		if err := start(bin); err != nil {
			return err
		}
	}
	for {
		changes := make(chan []string, 1)
		go func(base map[string]stamp) {
			paths, err := waitForChange(ctx, watch, base)
			if err == nil {
				changes <- paths
			}
		}(base)
		select {
		case <-ctx.Done():
			fmt.Fprintln(Stderr, "\ngtr: stopped")
			return nil
		case err := <-exitedOrNil(running, exited):
			code := 0
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			}
			fmt.Fprintf(Stderr, "gtr: the program exited (code %d); waiting for changes\n", code)
			running = nil
			paths := <-changesOrDone(ctx, changes)
			if paths == nil {
				return nil
			}
			fmt.Fprintf(Stderr, "gtr: changed %s, rebuilding\n", relPaths(p.Dir, paths))
		case paths := <-changes:
			fmt.Fprintf(Stderr, "gtr: changed %s, rebuilding\n", relPaths(p.Dir, paths))
		}
		base = snapshot(watch)
		if bin, ok := build(n); ok {
			n++
			stopRunning()
			if err := start(bin); err != nil {
				return err
			}
		}
	}
}

// exitedOrNil is the exit channel of the running program, or a channel that
// never delivers when nothing runs.
func exitedOrNil(running *exec.Cmd, exited chan error) <-chan error {
	if running == nil {
		return nil
	}
	return exited
}

func changesOrDone(ctx context.Context, changes chan []string) <-chan []string {
	out := make(chan []string, 1)
	go func() {
		select {
		case p := <-changes:
			out <- p
		case <-ctx.Done():
			out <- nil
		}
	}()
	return out
}
