package commands

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gtr-manager/internal/install"
	"gtr-manager/internal/manifest"
	"gtr-manager/internal/semver"
)

// git runs git in dir and returns its trimmed output.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return "", fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(exit.Stderr)))
		}
		if errors.Is(err, exec.ErrNotFound) {
			return "", errors.New("git is not installed (gtr version and gtr publish tag and push with git)")
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}

// gitRepo is where a package lives in git.
type gitRepo struct {
	top    string // repository root
	subdir string // package directory relative to it, slash-separated; "" at the root
}

func findRepo(dir string) (gitRepo, error) {
	top, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return gitRepo{}, fmt.Errorf("%s is not in a git repository: %w", dir, err)
	}
	r := gitRepo{top: top}
	// Compare resolved paths: git prints the real path (macOS /private/var).
	realDir, err1 := filepath.EvalSymlinks(dir)
	realTop, err2 := filepath.EvalSymlinks(filepath.FromSlash(top))
	if err1 != nil || err2 != nil {
		realDir, realTop = dir, filepath.FromSlash(top)
	}
	rel, err := filepath.Rel(realTop, realDir)
	if err != nil {
		return r, err
	}
	if rel != "." {
		r.subdir = filepath.ToSlash(rel)
	}
	return r, nil
}

// tagName is the git tag of a version: v1.2.3 for a package at the root of
// its repository, <name>@1.2.3 for one in a subdirectory (a monorepo), as
// the github:, gitlab: and git+ sources read tags (source.TagVersion).
func (r gitRepo) tagName(name, version string) string {
	if r.subdir == "" {
		return "v" + version
	}
	return name + "@" + version
}

// dirty lists uncommitted changes in the repository (untracked files too).
func (r gitRepo) dirty() ([]string, error) {
	out, err := git(r.top, "status", "--porcelain")
	if err != nil || out == "" {
		return nil, err
	}
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l) // " M a.go": git's output is trimmed as a whole
	}
	return lines, nil
}

// bump computes the next version: patch, minor, major, pre<kind>,
// prerelease, or an explicit version.
func bump(current, how, preid string) (string, error) {
	if v := strings.TrimPrefix(how, "v"); versionRe.MatchString(v) {
		return v, nil
	}
	cur, err := semver.Parse(current)
	if err != nil || !versionRe.MatchString(current) {
		return "", fmt.Errorf("gtr.json: version %q is not a SemVer version", current)
	}
	next := semver.Version{Major: cur.Major, Minor: cur.Minor, Patch: cur.Patch}
	if preid == "" && how == "prerelease" && cur.Pre != "" {
		// keep the current id: beta.2 → beta.3
		preid = cur.Pre
		if m := lastNumRe.FindStringSubmatch(cur.Pre); m != nil {
			preid = strings.TrimSuffix(m[1], ".")
		}
	}
	if preid == "" {
		preid = "rc"
	}
	switch how {
	case "patch":
		if cur.Pre == "" { // 1.2.3-rc.1 → 1.2.3: the release of the pre-release
			next.Patch++
		}
	case "minor":
		if cur.Pre == "" || cur.Patch != 0 {
			next.Minor, next.Patch = next.Minor+1, 0
		}
	case "major":
		if cur.Pre == "" || cur.Minor != 0 || cur.Patch != 0 {
			next.Major, next.Minor, next.Patch = next.Major+1, 0, 0
		}
	case "prepatch":
		next.Patch++
		next.Pre = preid + ".0"
	case "preminor":
		next.Minor, next.Patch = next.Minor+1, 0
		next.Pre = preid + ".0"
	case "premajor":
		next.Major, next.Minor, next.Patch = next.Major+1, 0, 0
		next.Pre = preid + ".0"
	case "prerelease":
		if cur.Pre == "" {
			next.Patch++
			next.Pre = preid + ".0"
			break
		}
		next.Pre = incPre(cur.Pre, preid)
	default:
		return "", fmt.Errorf("unknown bump %q: patch, minor, major, prepatch, preminor, premajor, prerelease or a version such as 1.4.0", how)
	}
	return next.String(), nil
}

var lastNumRe = regexp.MustCompile(`^(.*\.)(\d+)$`)

// incPre bumps a pre-release: rc.1 → rc.2, rc → rc.1; another preid
// starts over (beta.3 with --preid rc → rc.0).
func incPre(pre, preid string) string {
	if pre == preid {
		return preid + ".1"
	}
	if m := lastNumRe.FindStringSubmatch(pre); m != nil && m[1] == preid+"." {
		n, _ := strconv.Atoi(m[2])
		return m[1] + strconv.Itoa(n+1)
	}
	return preid + ".0"
}

var unreleasedRe = regexp.MustCompile(`(?mi)^##[ \t]+\[?unreleased\]?[ \t]*\r?$`)

// releaseChangelog turns the "## Unreleased" heading of CHANGELOG.md into
// "## <version> — <date>" under a new empty "## Unreleased". It reports
// whether the file changed.
func releaseChangelog(dir, version string, date time.Time) (bool, error) {
	path := filepath.Join(dir, "CHANGELOG.md")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	loc := unreleasedRe.FindIndex(data)
	if loc == nil {
		return false, nil
	}
	nl := "\n"
	if data[loc[1]-1] == '\r' {
		nl = "\r\n" // keep the file's line endings (core.autocrlf on Windows)
	}
	heading := fmt.Sprintf("## Unreleased%s%s## %s — %s", nl, nl, version, date.Format("2006-01-02"))
	if nl == "\r\n" {
		heading += "\r"
	}
	out := append(append(append([]byte(nil), data[:loc[0]]...), heading...), data[loc[1]:]...)
	return true, manifest.WriteFileAtomic(path, out)
}

// isExplicit reports whether how is a version, not patch, minor, ….
func isExplicit(how string) bool {
	_, err := semver.Parse(strings.TrimPrefix(how, "v"))
	return err == nil
}

// Version prints or changes the package version:
//
//	gtr version              the package and gtr-manager versions
//	gtr version patch        1.2.3 → 1.2.4 (also minor, major, a version)
//	gtr version 0.1.0        the current version, when it has no tag yet:
//	                         the first release of a package
//	gtr version prerelease   1.2.4 → 1.2.5-rc.0 → 1.2.5-rc.1 (--preid beta)
//
// A change writes gtr.json, turns "## Unreleased" in CHANGELOG.md into the
// version, commits both and tags the commit: v1.2.4, or orm-mysql@1.2.4 for
// a package in a subdirectory of its repository. --no-git only writes the
// files; --no-tag commits without tagging. The repository must be clean.
func Version(args []string, toolVersion string) error {
	var how, preid, message string
	noGit, noTag, allowDirty := false, false, false
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, hasValue := strings.Cut(a, "=")
		switch {
		case name == "--preid" || name == "-m" || name == "--message":
			if !hasValue {
				if i+1 >= len(args) {
					return fmt.Errorf("%s needs a value", name)
				}
				i++
				value = args[i]
			}
			if name == "--preid" {
				preid = value
			} else {
				message = value
			}
		case a == "--no-git":
			noGit = true
		case a == "--no-tag":
			noTag = true
		case a == "--allow-dirty":
			allowDirty = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %s; usage: gtr version [patch|minor|major|prerelease|<version>] [--preid rc] [--no-git] [--no-tag] [-m message]", a)
		case how == "":
			how = a
		default:
			return fmt.Errorf("unexpected argument %q", a)
		}
	}
	pkgs, err := packages(false)
	if how == "" {
		if err == nil && len(pkgs) == 1 && pkgs[0].M.Name != "" {
			fmt.Fprintf(Stdout, "%s %s\n", pkgs[0].M.Name, orDash(pkgs[0].M.Version))
		}
		fmt.Fprintf(Stdout, "gtr-manager %s\n", toolVersion)
		return nil
	}
	if err != nil {
		return err
	}
	if len(pkgs) != 1 {
		return errors.New("gtr version changes one package: run it in the workspace member to release")
	}
	p := pkgs[0]
	if p.M.Name == "" {
		return errors.New("gtr.json has no name")
	}
	next, err := bump(p.M.Version, how, preid)
	if err != nil {
		return err
	}
	// first: the explicit current version, never tagged yet (the first
	// release of a package that started at 0.1.0): tag it as is.
	first := false
	if p.M.Version != "" {
		if cur, err := semver.Parse(p.M.Version); err == nil {
			switch c := semver.Compare(semver.MustParse(next), cur); {
			case c == 0 && isExplicit(how) && !noGit && !noTag:
				first = true
			case c <= 0:
				return fmt.Errorf("version %s is not higher than the current %s", next, p.M.Version)
			}
		}
	}
	var repo gitRepo
	if !noGit {
		if repo, err = findRepo(p.Dir); err != nil {
			return fmt.Errorf("%w (or use --no-git)", err)
		}
		if !allowDirty {
			if d, err := repo.dirty(); err != nil {
				return err
			} else if len(d) > 0 {
				return fmt.Errorf("the repository has uncommitted changes; commit or stash them first (or --allow-dirty):\n  %s", strings.Join(firstN(d, 8), "\n  "))
			}
		}
		if !noTag {
			tag := repo.tagName(p.M.Name, next)
			if _, err := git(repo.top, "rev-parse", "-q", "--verify", "refs/tags/"+tag); err == nil {
				if first {
					return fmt.Errorf("version %s is already tagged (%s); bump it with `gtr version patch`", next, tag)
				}
				return fmt.Errorf("tag %s already exists", tag)
			}
		}
	}
	p.M.Version = next
	if err := manifest.Save(filepath.Join(p.Dir, manifest.FileName), p.M); err != nil {
		return err
	}
	changelog, err := releaseChangelog(p.Dir, next, time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintf(Stdout, "%s %s\n", p.M.Name, next)
	warnDependents(p)
	if noGit {
		return nil
	}
	files := []string{manifest.FileName}
	if changelog {
		files = append(files, "CHANGELOG.md")
	}
	tag := repo.tagName(p.M.Name, next)
	if message == "" {
		message = tag
	}
	if _, err := git(p.Dir, append([]string{"add", "--"}, files...)...); err != nil {
		return err
	}
	// A first release may have nothing to commit (gtr.json unchanged, no
	// "## Unreleased" in CHANGELOG.md): then only the tag is made.
	if _, err := git(p.Dir, append([]string{"diff", "--cached", "--quiet", "--"}, files...)...); err != nil || !first {
		if _, err := git(p.Dir, append([]string{"commit", "-q", "-m", message, "--"}, files...)...); err != nil {
			return err
		}
	}
	if noTag {
		fmt.Fprintf(Stdout, "committed %q\n", message)
		return nil
	}
	if _, err := git(p.Dir, "tag", "-a", tag, "-m", tag); err != nil {
		return err
	}
	fmt.Fprintf(Stdout, "committed and tagged %s; publish with `gtr publish`\n", tag)
	return nil
}

// warnDependents tells which workspace members require the package with a
// range the new version no longer matches.
func warnDependents(p pkgDir) {
	root, member, err := install.FindRoot(p.Dir)
	if err != nil || member == "" {
		return
	}
	rm, err := manifest.Load(filepath.Join(root, manifest.FileName))
	if err != nil {
		return
	}
	members, err := install.Members(root, rm, nil)
	if err != nil {
		return
	}
	v := semver.MustParse(p.M.Version)
	for _, mem := range memberOrder(members) {
		for _, deps := range []map[string]string{mem.Manifest.Dependencies, mem.Manifest.PeerDependencies, mem.Manifest.DevDependencies} {
			req, ok := deps[p.M.Name]
			if !ok {
				continue
			}
			_, rng, _ := strings.Cut(req, "#")
			if rng == "" && !strings.Contains(req, ":") {
				rng = req
			}
			if strings.HasPrefix(req, "workspace:") {
				rng = strings.TrimPrefix(req, "workspace:")
			}
			if r, err := semver.ParseRange(rng); err == nil && rng != "" && !r.Match(v) {
				fmt.Fprintf(Stderr, "warning: %s requires %s %q, which %s does not match; update it in %s/gtr.json\n",
					mem.Manifest.Name, p.M.Name, req, p.M.Version, mem.Dir)
			}
		}
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func firstN(list []string, n int) []string {
	if len(list) <= n {
		return list
	}
	return append(list[:n:n], fmt.Sprintf("… and %d more", len(list)-n))
}
