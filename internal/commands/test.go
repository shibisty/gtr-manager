package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gtr-manager/internal/install"
	"gtr-manager/internal/manifest"
)

// testConfig is the "test" block of gtr.json.
type testConfig struct {
	Coverage *float64 `json:"coverage"` // minimum, %
	Race     *bool    `json:"race"`
	Timeout  string   `json:"timeout"`
}

func loadTestConfig(m *manifest.Manifest) (testConfig, error) {
	var c testConfig
	if raw := m.Raw("test"); raw != nil {
		if err := json.Unmarshal(raw, &c); err != nil {
			return c, fmt.Errorf("gtr.json: test: %w", err)
		}
	}
	return c, nil
}

type testOptions struct {
	tags      []string // integration, e2e
	race      *bool
	run       string
	coverage  bool
	html      bool
	min       *float64
	watch     bool
	recursive bool
	verbose   bool
	short     bool
	count     string
	timeout   string
	goArgs    []string // after --
}

const testUsage = "usage: gtr test [--unit | --integration | --e2e] [--coverage [--html] [--min N]] [--race | --no-race] [--run REGEXP] [-r] [-v] [--watch] [-- go test flags]"

func parseTestArgs(args []string) (testOptions, error) {
	var o testOptions
	next := func(i *int, flag string) (string, error) {
		if *i+1 >= len(args) {
			return "", fmt.Errorf("%s needs a value; %s", flag, testUsage)
		}
		*i++
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, hasValue := strings.Cut(a, "=")
		var err error
		switch name {
		case "--":
			o.goArgs = append(o.goArgs, args[i+1:]...)
			i = len(args)
		case "--unit":
			// the default: no build tags
		case "--integration":
			o.tags = append(o.tags, "integration")
		case "--e2e":
			o.tags = append(o.tags, "e2e")
		case "--tags":
			if !hasValue {
				value, err = next(&i, name)
			}
			for _, t := range strings.Split(value, ",") {
				if t = strings.TrimSpace(t); t != "" {
					o.tags = append(o.tags, t)
				}
			}
		case "--race":
			t := true
			o.race = &t
		case "--no-race":
			f := false
			o.race = &f
		case "--run":
			if !hasValue {
				value, err = next(&i, name)
			}
			o.run = value
		case "--coverage", "--cover":
			o.coverage = true
		case "--html":
			o.html, o.coverage = true, true
		case "--min":
			if !hasValue {
				value, err = next(&i, name)
			}
			if err == nil {
				var f float64
				f, err = strconv.ParseFloat(strings.TrimSuffix(value, "%"), 64)
				if err != nil || f < 0 || f > 100 {
					err = fmt.Errorf("--min: %q is not a percentage", value)
				}
				o.min, o.coverage = &f, true
			}
		case "--watch", "-w":
			o.watch = true
		case "-r", "--recursive":
			o.recursive = true
		case "-v", "--verbose":
			o.verbose = true
		case "--short":
			o.short = true
		case "--count":
			if !hasValue {
				value, err = next(&i, name)
			}
			o.count = value
		case "--timeout":
			if !hasValue {
				value, err = next(&i, name)
			}
			o.timeout = value
		default:
			err = fmt.Errorf("unknown flag %s; %s", a, testUsage)
		}
		if err != nil {
			return o, err
		}
	}
	return o, nil
}

// Test runs go test in the project, a member, or every workspace member:
//
//	gtr test                       go test ./... (unit tests: no build tags)
//	gtr test --integration         with -tags integration (also --e2e, --tags a,b)
//	gtr test --coverage            coverage, checked against test.coverage in gtr.json
//	gtr test --coverage --html     and build/coverage.html
//	gtr test --watch               again on every change
//	gtr test -- -bench . -benchmem  anything else go test takes
//
// The race detector is on when test.race is true (the default) and a C
// compiler is available; --race and --no-race decide explicitly.
func Test(args []string) error {
	o, err := parseTestArgs(args)
	if err != nil {
		return err
	}
	pkgs, err := packages(o.recursive)
	if err != nil {
		return err
	}
	if !o.watch {
		return runTests(context.Background(), pkgs, o)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var dirs []string
	for _, p := range pkgs {
		dirs = append(dirs, p.Dir)
	}
	for {
		base := snapshot(dirs)
		if err := runTests(ctx, pkgs, o); err != nil {
			if _, silent := ExitCode(err); !silent {
				fmt.Fprintf(Stderr, "gtr: %v\n", err)
			}
		}
		fmt.Fprintln(Stderr, "gtr: watching for changes (Ctrl+C to stop)")
		paths, err := waitForChange(ctx, dirs, base)
		if err != nil {
			return nil // interrupted
		}
		fmt.Fprintf(Stderr, "\ngtr: changed %s\n", relPaths(pkgs[0].Dir, paths))
	}
}

// coverageResult is the coverage of one package directory.
type coverageResult struct {
	label   string
	percent float64
	min     *float64
	blocks  map[string]coverBlock
}

func runTests(ctx context.Context, pkgs []pkgDir, o testOptions) error {
	var failed []string
	var results []coverageResult
	tmp, err := os.MkdirTemp("", "gtr-test-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	raceOK, raceWhy := raceAvailable(pkgs[0].Dir)
	for i, p := range pkgs {
		cfg, err := loadTestConfig(p.M)
		if err != nil {
			return err
		}
		bc, err := loadBuildConfig(p.M)
		if err != nil {
			return err
		}
		args := []string{"test"}
		tags := append(append([]string(nil), bc.Tags...), o.tags...)
		if len(tags) > 0 {
			args = append(args, "-tags", strings.Join(uniq(tags), ","))
		}
		race := cfg.Race == nil || *cfg.Race
		explicit := o.race != nil
		if explicit {
			race = *o.race
		}
		if race && !raceOK {
			if explicit {
				return fmt.Errorf("--race: %s", raceWhy)
			}
			if i == 0 {
				fmt.Fprintf(Stderr, "gtr: testing without -race: %s\n", raceWhy)
			}
			race = false
		}
		if race {
			args = append(args, "-race")
		}
		if o.verbose {
			args = append(args, "-v")
		}
		if o.short {
			args = append(args, "-short")
		}
		if o.run != "" {
			args = append(args, "-run", o.run)
		}
		if o.count != "" {
			args = append(args, "-count", o.count)
		}
		timeout := cfg.Timeout
		if o.timeout != "" {
			timeout = o.timeout
		}
		if timeout != "" {
			args = append(args, "-timeout", timeout)
		}
		profile := filepath.Join(tmp, fmt.Sprintf("%d.out", i))
		if o.coverage {
			mode := "set"
			if race {
				mode = "atomic" // -race requires it
			}
			args = append(args, "-covermode="+mode, "-coverprofile="+profile)
		}
		args = append(args, o.goArgs...)
		args = append(args, "./...")
		if len(pkgs) > 1 {
			fmt.Fprintf(Stderr, "== %s\n", p.Label)
		}
		cmd := goCommand(ctx, p.Dir, nil, args...)
		cmd.Stdin = Stdin
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				return err
			}
			failed = append(failed, p.Label)
			continue
		}
		if o.coverage {
			blocks, err := readProfile(profile)
			if err != nil {
				return err
			}
			min := cfg.Coverage
			if o.min != nil {
				min = o.min
			}
			results = append(results, coverageResult{label: p.Label, percent: percent(blocks), min: min, blocks: blocks})
		}
	}
	var below []string
	if o.coverage && len(results) > 0 {
		all := map[string]coverBlock{}
		fmt.Fprintln(Stdout)
		for _, r := range results {
			for k, b := range r.blocks {
				all[k] = mergeBlock(all[k], b)
			}
			line := fmt.Sprintf("coverage %-24s %5.1f%%", r.label, r.percent)
			if r.min != nil {
				status := "ok"
				if r.percent+1e-9 < *r.min {
					status = "BELOW"
					below = append(below, fmt.Sprintf("%s %.1f%% < %.1f%%", r.label, r.percent, *r.min))
				}
				line += fmt.Sprintf("  (min %.1f%%, %s)", *r.min, status)
			}
			fmt.Fprintln(Stdout, line)
		}
		if len(results) > 1 {
			fmt.Fprintf(Stdout, "coverage %-24s %5.1f%%\n", "total", percent(all))
		}
		outDir := filepath.Join(pkgs[0].Dir, "build")
		if len(pkgs) > 1 {
			root, _, _ := install.FindRoot(pkgs[0].Dir)
			outDir = filepath.Join(root, "build")
		}
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return err
		}
		out := filepath.Join(outDir, "coverage.out")
		if err := writeProfile(out, all); err != nil {
			return err
		}
		if o.html {
			html := filepath.Join(outDir, "coverage.html")
			if err := goCommand(ctx, pkgs[0].Dir, nil, "tool", "cover", "-html="+out, "-o", html).Run(); err != nil {
				return fmt.Errorf("go tool cover: %w", err)
			}
			fmt.Fprintf(Stdout, "report: %s\n", html)
		}
	}
	switch {
	case len(failed) > 0 && len(pkgs) > 1:
		return &exitError{code: 1, msg: "tests failed in " + strings.Join(failed, ", ")}
	case len(failed) > 0:
		return &exitError{code: 1}
	case len(below) > 0:
		return &exitError{code: 1, msg: "coverage below the minimum (test.coverage in gtr.json): " + strings.Join(below, "; ")}
	}
	return nil
}

// exitError ends gtr with a code; msg (if any) is printed as the error.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

// ExitCode returns the exit code of an error from Test and friends: 0 if
// none, the code of an *exitError, else 1. silent reports that the error
// was already printed (a failing test).
func ExitCode(err error) (code int, silent bool) {
	var e *exitError
	if errors.As(err, &e) {
		return e.code, e.msg == ""
	}
	return 1, false
}

// raceAvailable reports whether go test -race can work: cgo enabled and a
// C compiler in PATH (Windows rarely has one).
func raceAvailable(dir string) (bool, string) {
	if v, err := goOutput(dir, "env", "CGO_ENABLED"); err != nil || v != "1" {
		return false, "the race detector needs cgo (CGO_ENABLED=1)"
	}
	cc, err := goOutput(dir, "env", "CC")
	if err != nil || cc == "" {
		cc = "gcc"
	}
	if _, err := exec.LookPath(strings.Fields(cc)[0]); err != nil {
		return false, fmt.Sprintf("the race detector needs a C compiler (%s not found)", strings.Fields(cc)[0])
	}
	return true, ""
}

func uniq(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// coverBlock is one line of a coverage profile.
type coverBlock struct {
	stmts int
	count int64
}

func mergeBlock(a, b coverBlock) coverBlock {
	if b.stmts > a.stmts {
		a.stmts = b.stmts
	}
	a.count += b.count
	return a
}

// readProfile reads a go test -coverprofile file: "mode: x" then
// "file:start,end stmts count" lines. A missing file (no Go packages) is
// empty.
func readProfile(path string) (map[string]coverBlock, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]coverBlock{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]coverBlock{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("%s: bad line %q", path, line)
		}
		stmts, err1 := strconv.Atoi(fields[1])
		count, err2 := strconv.ParseInt(fields[2], 10, 64)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("%s: bad line %q", path, line)
		}
		out[fields[0]] = mergeBlock(out[fields[0]], coverBlock{stmts, count})
	}
	return out, sc.Err()
}

func writeProfile(path string, blocks map[string]coverBlock) error {
	keys := make([]string, 0, len(blocks))
	for k := range blocks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("mode: count\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s %d %d\n", k, blocks[k].stmts, blocks[k].count)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// percent is the share of statements that ran, as go test reports it.
func percent(blocks map[string]coverBlock) float64 {
	var total, covered int
	for _, b := range blocks {
		total += b.stmts
		if b.count > 0 {
			covered += b.stmts
		}
	}
	if total == 0 {
		return 0
	}
	return 100 * float64(covered) / float64(total)
}
