package commands

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gtr-manager/internal/manifest"
)

// buildConfig is the "build" block of gtr.json.
type buildConfig struct {
	Entry    string   `json:"entry"`    // main package, default "."
	Output   string   `json:"output"`   // binary name, default the package name
	OutDir   string   `json:"outDir"`   // default "build"
	Targets  []string `json:"targets"`  // GOOS/GOARCH for --all-targets
	Ldflags  string   `json:"ldflags"`  // {{version}} {{commit}} {{date}} {{name}}
	Tags     []string `json:"tags"`     // build tags (also used by gtr test)
	Cgo      bool     `json:"cgo"`      // CGO_ENABLED=1 (default 0)
	Trimpath *bool    `json:"trimpath"` // default true
	Files    []string `json:"files"`    // added to archives; default README*, LICENSE*
}

// DefaultTargets are built by --all-targets when build.targets is not set:
// the platforms gtr itself is released for.
var DefaultTargets = []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64", "windows/amd64", "windows/arm64"}

var targetRe = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9]+$`)

func loadBuildConfig(m *manifest.Manifest) (buildConfig, error) {
	var c buildConfig
	if raw := m.Raw("build"); raw != nil {
		if err := json.Unmarshal(raw, &c); err != nil {
			return c, fmt.Errorf("gtr.json: build: %w", err)
		}
	}
	for _, t := range c.Targets {
		if !targetRe.MatchString(t) {
			return c, fmt.Errorf("gtr.json: build.targets: %q is not GOOS/GOARCH (e.g. linux/amd64)", t)
		}
	}
	c.Targets = uniq(c.Targets)
	return c, nil
}

type buildOptions struct {
	targets   []string
	all       bool
	archive   bool
	version   string
	outDir    string
	recursive bool
	goArgs    []string
}

const buildUsage = "usage: gtr build [--target os/arch]... [--all-targets] [--archive] [--version V] [--out DIR] [-r] [-- go build flags]"

func parseBuildArgs(args []string) (buildOptions, error) {
	var o buildOptions
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, hasValue := strings.Cut(a, "=")
		needValue := func() error {
			if hasValue {
				return nil
			}
			if i+1 >= len(args) {
				return fmt.Errorf("%s needs a value; %s", name, buildUsage)
			}
			i++
			value = args[i]
			return nil
		}
		switch name {
		case "--":
			o.goArgs = append(o.goArgs, args[i+1:]...)
			i = len(args)
		case "--target", "-t":
			if err := needValue(); err != nil {
				return o, err
			}
			for _, t := range strings.Split(value, ",") {
				if !targetRe.MatchString(t) {
					return o, fmt.Errorf("--target %q: expected GOOS/GOARCH, e.g. linux/amd64", t)
				}
				o.targets = append(o.targets, t)
			}
		case "--all-targets", "--all":
			o.all = true
		case "--archive":
			o.archive = true
		case "--version":
			if err := needValue(); err != nil {
				return o, err
			}
			o.version = strings.TrimPrefix(value, "v")
			if !versionRe.MatchString(o.version) {
				return o, fmt.Errorf("--version %q is not a SemVer version", value)
			}
		case "--out", "-o":
			if err := needValue(); err != nil {
				return o, err
			}
			o.outDir = value
		case "-r", "--recursive":
			o.recursive = true
		default:
			return o, fmt.Errorf("unknown flag %s; %s", a, buildUsage)
		}
	}
	if o.all && len(o.targets) > 0 {
		return o, errors.New("--all-targets and --target exclude each other")
	}
	o.targets = uniq(o.targets)
	return o, nil
}

// Build compiles the package by the "build" block of gtr.json:
//
//	gtr build                          for this system
//	gtr build --target linux/arm64     for another (repeat, or a,b)
//	gtr build --all-targets --archive  every build.targets, archives and SHA256SUMS
//
// Binaries go to build/<version>/<os>-<arch>/<output>; archives
// (<output>_<version>_<os>_<arch>.tar.gz, .zip for Windows) and SHA256SUMS
// to build/<version>/, the layout gtr installs releases from (ADR-0008).
func Build(args []string) error {
	o, err := parseBuildArgs(args)
	if err != nil {
		return err
	}
	pkgs, err := packages(o.recursive)
	if err != nil {
		return err
	}
	if len(pkgs) > 1 {
		var withBuild []pkgDir
		for _, p := range pkgs {
			if p.M.Raw("build") != nil {
				withBuild = append(withBuild, p)
			}
		}
		if len(withBuild) == 0 {
			return errors.New(`no workspace member has a "build" block in its gtr.json; run gtr build in the package to build`)
		}
		pkgs = withBuild
	}
	for _, p := range pkgs {
		if len(pkgs) > 1 {
			fmt.Fprintf(Stderr, "== %s\n", p.Label)
		}
		if err := buildPackage(context.Background(), p, o); err != nil {
			return err
		}
	}
	return nil
}

// buildInfo fills the ldflags placeholders.
type buildInfo struct {
	name, version, commit string
	date                  time.Time
}

func newBuildInfo(p pkgDir, version string) buildInfo {
	b := buildInfo{name: p.M.Name, version: version, commit: "unknown", date: time.Now().UTC()}
	if b.version == "" {
		b.version = p.M.Version
	}
	if b.version == "" {
		b.version = "0.0.0"
	}
	if out, err := exec.Command("git", "-C", p.Dir, "rev-parse", "--short=12", "HEAD").Output(); err == nil {
		b.commit = strings.TrimSpace(string(out))
	}
	// SOURCE_DATE_EPOCH makes builds reproducible (reproducible-builds.org).
	if s := os.Getenv("SOURCE_DATE_EPOCH"); s != "" {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			b.date = time.Unix(n, 0).UTC()
		}
	}
	return b
}

var placeholderRe = regexp.MustCompile(`\{\{\s*([a-z]+)\s*\}\}`)

// expandLdflags replaces {{version}}, {{commit}}, {{date}} and {{name}}.
func expandLdflags(s string, b buildInfo) (string, error) {
	var bad []string
	out := placeholderRe.ReplaceAllStringFunc(s, func(m string) string {
		switch placeholderRe.FindStringSubmatch(m)[1] {
		case "version":
			return b.version
		case "commit":
			return b.commit
		case "date":
			return b.date.Format(time.RFC3339)
		case "name":
			return b.name
		}
		bad = append(bad, m)
		return m
	})
	if len(bad) > 0 {
		return "", fmt.Errorf("build.ldflags: unknown placeholder %s (known: {{version}}, {{commit}}, {{date}}, {{name}})", strings.Join(bad, ", "))
	}
	return out, nil
}

func buildPackage(ctx context.Context, p pkgDir, o buildOptions) error {
	c, err := loadBuildConfig(p.M)
	if err != nil {
		return err
	}
	info := newBuildInfo(p, o.version)
	ldflags, err := expandLdflags(c.Ldflags, info)
	if err != nil {
		return err
	}
	entry := c.Entry
	if entry == "" {
		entry = "."
	}
	if !strings.HasPrefix(entry, ".") {
		entry = "./" + entry // a directory, not an import path
	}
	output := c.Output
	if output == "" {
		output = p.M.Name
	}
	outDir := o.outDir
	if outDir == "" {
		outDir = c.OutDir
	}
	if outDir == "" {
		outDir = "build"
	}
	if !filepath.IsAbs(outDir) {
		outDir = filepath.Join(p.Dir, outDir)
	}
	targets := o.targets
	switch {
	case o.all && len(c.Targets) > 0:
		targets = c.Targets
	case o.all:
		targets = DefaultTargets
	case len(targets) == 0:
		host, err := goOutput(p.Dir, "env", "GOOS", "GOARCH")
		if err != nil {
			return err
		}
		targets = []string{strings.Join(strings.Fields(host), "/")}
	}
	versionDir := filepath.Join(outDir, info.version)
	type result struct {
		target, bin string
		out         bytes.Buffer
		err         error
	}
	results := make([]*result, len(targets))
	sem := make(chan struct{}, max(1, min(3, runtime.NumCPU())))
	var wg sync.WaitGroup
	for i, t := range targets {
		r := &result{target: t}
		results[i] = r
		goos, goarch, _ := strings.Cut(t, "/")
		bin := output
		if goos == "windows" {
			bin += ".exe"
		}
		r.bin = filepath.Join(versionDir, goos+"-"+goarch, bin)
		args := []string{"build", "-o", r.bin}
		if c.Trimpath == nil || *c.Trimpath {
			args = append(args, "-trimpath")
		}
		if len(c.Tags) > 0 {
			args = append(args, "-tags", strings.Join(c.Tags, ","))
		}
		if ldflags != "" {
			args = append(args, "-ldflags", ldflags)
		}
		args = append(args, o.goArgs...)
		args = append(args, entry)
		cgo := "0"
		if c.Cgo {
			cgo = "1"
		}
		cmd := goCommand(ctx, p.Dir, []string{"GOOS=" + goos, "GOARCH=" + goarch, "CGO_ENABLED=" + cgo}, args...)
		cmd.Stdout, cmd.Stderr = &r.out, &r.out
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			os.RemoveAll(filepath.Dir(r.bin))
			r.err = cmd.Run()
		}()
	}
	wg.Wait()
	var failed []string
	for _, r := range results {
		Stderr.Write(r.out.Bytes())
		if r.err != nil {
			failed = append(failed, r.target)
			continue
		}
		size := ""
		if st, err := os.Stat(r.bin); err == nil {
			size = fmt.Sprintf(" (%.1f MB)", float64(st.Size())/(1<<20))
		}
		fmt.Fprintf(Stdout, "built %-14s %s%s\n", r.target, relTo(p.Dir, r.bin), size)
	}
	if len(failed) > 0 {
		return fmt.Errorf("build failed for %s", strings.Join(failed, ", "))
	}
	if !o.archive {
		return nil
	}
	files, err := archiveFiles(p.Dir, c.Files)
	if err != nil {
		return err
	}
	var sums []string
	for _, r := range results {
		goos, goarch, _ := strings.Cut(r.target, "/")
		name := fmt.Sprintf("%s_%s_%s_%s", output, info.version, goos, goarch)
		if goos == "windows" {
			name += ".zip"
		} else {
			name += ".tar.gz"
		}
		path := filepath.Join(versionDir, name)
		if err := writeArchive(path, r.bin, files, info.date); err != nil {
			return err
		}
		sum, err := fileSum(path)
		if err != nil {
			return err
		}
		sums = append(sums, sum+"  "+name)
		fmt.Fprintf(Stdout, "archive %s\n", relTo(p.Dir, path))
	}
	sort.Slice(sums, func(i, j int) bool { return sums[i][66:] < sums[j][66:] })
	sumsPath := filepath.Join(versionDir, "SHA256SUMS")
	if err := os.WriteFile(sumsPath, []byte(strings.Join(sums, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(Stdout, "sums    %s\n", relTo(p.Dir, sumsPath))
	return nil
}

func relTo(base, p string) string {
	if r, err := filepath.Rel(base, p); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.ToSlash(r)
	}
	return p
}

// archiveFiles resolves build.files (globs relative to the package); by
// default README* and LICENSE* if present.
func archiveFiles(dir string, patterns []string) ([]string, error) {
	explicit := patterns != nil
	if !explicit {
		patterns = []string{"README*", "LICENSE*"}
	}
	var out []string
	for _, pat := range patterns {
		matches, err := filepath.Glob(filepath.Join(dir, filepath.FromSlash(pat)))
		if err != nil {
			return nil, fmt.Errorf("build.files: %q: %w", pat, err)
		}
		if explicit && len(matches) == 0 {
			return nil, fmt.Errorf("build.files: %q matches no file", pat)
		}
		for _, m := range matches {
			if st, err := os.Stat(m); err == nil && !st.IsDir() {
				out = append(out, m)
			}
		}
	}
	sort.Strings(out)
	return uniq(out), nil
}

// writeArchive packs the binary and files at the archive root (the layout
// gtr's installers expect), with fixed times so the same inputs give the
// same archive.
func writeArchive(path, bin string, files []string, mtime time.Time) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	all := append([]string{bin}, files...)
	if strings.HasSuffix(path, ".zip") {
		zw := zip.NewWriter(f)
		for _, p := range all {
			mode := os.FileMode(0o644)
			if p == bin {
				mode = 0o755
			}
			h := &zip.FileHeader{Name: filepath.Base(p), Method: zip.Deflate, Modified: mtime}
			h.SetMode(mode)
			w, err := zw.CreateHeader(h)
			if err != nil {
				f.Close()
				return err
			}
			if err := copyInto(w, p); err != nil {
				f.Close()
				return err
			}
		}
		err = zw.Close()
	} else {
		gz, _ := gzip.NewWriterLevel(f, gzip.BestCompression)
		gz.ModTime = mtime
		tw := tar.NewWriter(gz)
		for _, p := range all {
			st, err := os.Stat(p)
			if err != nil {
				f.Close()
				return err
			}
			mode := int64(0o644)
			if p == bin {
				mode = 0o755
			}
			hdr := &tar.Header{Name: filepath.Base(p), Mode: mode, Size: st.Size(), ModTime: mtime, Typeflag: tar.TypeReg, Format: tar.FormatPAX}
			if err := tw.WriteHeader(hdr); err != nil {
				f.Close()
				return err
			}
			if err := copyInto(tw, p); err != nil {
				f.Close()
				return err
			}
		}
		if err = tw.Close(); err == nil {
			err = gz.Close()
		}
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func copyInto(w io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
