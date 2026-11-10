package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"

	"gtr-manager/internal/install"
	"gtr-manager/internal/spec"
)

// Publish releases the current version, to the gtr registry or as a git tag:
//
//	gtr publish                to the registry when logged in (gtr login), else as a git tag
//	gtr publish --registry URL to that registry
//	gtr publish --git          checks, then git push --atomic origin <branch> <tag>
//	gtr publish --dry-run      only the checks (and the files of the archive)
//	gtr publish --skip-tests   without running the tests
//	gtr publish --remote up    another remote (git)
//
// To the registry, the package's files (those git does not ignore, without
// generated go.mod, gtr_modules, build output) go up as a .tar.gz; a
// published version is never replaced. As a tag, the tag `gtr version` made
// is pushed, and github:, gitlab: and git+ sources install from it.
//
// Checks: gtr.json has a name and a SemVer version and is not private;
// dependencies have no file: or workspace: sources (nobody else can
// install them); the repository is clean; the version tag exists and points
// at HEAD; the tests pass (with the coverage threshold of gtr.json); the
// remote does not have the tag yet.
func Publish(args []string) error {
	regURL, args, err := parseRegistryFlag(args)
	if err != nil {
		return err
	}
	dryRun, skipTests, remote, useGit, allowDirty := false, false, "origin", false, false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--git":
			useGit = true
		case a == "--allow-dirty":
			allowDirty = true
		case a == "--dry-run" || a == "-n":
			dryRun = true
		case a == "--skip-tests":
			skipTests = true
		case a == "--remote" && i+1 < len(args):
			i++
			remote = args[i]
		case strings.HasPrefix(a, "--remote="):
			remote = strings.TrimPrefix(a, "--remote=")
		default:
			return fmt.Errorf("unknown argument %q; usage: gtr publish [--registry <url> | --git] [--dry-run] [--skip-tests] [--remote origin]", a)
		}
	}
	pkgs, err := packages(false)
	if err != nil {
		return err
	}
	if len(pkgs) != 1 {
		return errors.New("gtr publish releases one package: run it in the workspace member to publish")
	}
	p := pkgs[0]
	step := func(format string, a ...any) { fmt.Fprintf(Stderr, "✓ "+format+"\n", a...) }

	if err := spec.ValidateName(p.M.Name); err != nil {
		return fmt.Errorf("gtr.json: %w", err)
	}
	if !versionRe.MatchString(p.M.Version) {
		return fmt.Errorf("gtr.json: version %q is not a SemVer version such as 1.2.0; set one with `gtr version 0.1.0`", p.M.Version)
	}
	if string(p.M.Raw("private")) == "true" {
		return errors.New(`gtr.json has "private": true; remove it to publish`)
	}
	if bad := localDeps(p); len(bad) > 0 {
		return fmt.Errorf("these dependencies are only reachable here, so nobody could install the package:\n  %s\nuse a published source such as \"github:owner/repo/subdir#^0.1\"", strings.Join(bad, "\n  "))
	}
	step("gtr.json: %s %s", p.M.Name, p.M.Version)

	if !useGit {
		c := clientFor(regURL)
		if regURL != "" || c.Token != "" {
			return publishToRegistry(p, c, dryRun, skipTests, allowDirty, step)
		}
	}

	repo, err := findRepo(p.Dir)
	if err != nil {
		return err
	}
	if d, err := repo.dirty(); err != nil {
		return err
	} else if len(d) > 0 {
		return fmt.Errorf("the repository has uncommitted changes:\n  %s", strings.Join(firstN(d, 8), "\n  "))
	}
	step("the repository is clean")

	tag := repo.tagName(p.M.Name, p.M.Version)
	tagged, err := git(repo.top, "rev-list", "-n", "1", "refs/tags/"+tag)
	if err != nil {
		return fmt.Errorf("there is no tag %s for version %s; make it with `gtr version patch` (or minor, major)", tag, p.M.Version)
	}
	head, err := git(repo.top, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if tagged != head {
		return fmt.Errorf("tag %s is on %.12s, not on HEAD (%.12s); release the current commit with `gtr version patch`", tag, tagged, head)
	}
	step("tag %s is on HEAD", tag)

	if out, err := git(repo.top, "ls-remote", "--tags", remote, "refs/tags/"+tag); err != nil {
		return err
	} else if out != "" {
		return fmt.Errorf("%s already has tag %s: version %s is published; bump it with `gtr version patch`", remote, tag, p.M.Version)
	}
	step("%s does not have %s yet", remote, tag)

	if links, err := install.LoadLinks(filepath.Join(root(p.Dir), filepath.FromSlash(install.LinksFile))); err != nil {
		return err
	} else if len(links) > 0 {
		return errors.New("packages are linked to local copies (gtr link): the tests would not test what others install; run `gtr unlink` first")
	}
	if skipTests {
		fmt.Fprintln(Stderr, "- tests skipped (--skip-tests)")
	} else if err := publishTests(p); err != nil {
		return err
	}

	push := []string{"push", "--atomic", remote}
	if branch, err := git(repo.top, "symbolic-ref", "-q", "--short", "HEAD"); err == nil && branch != "" {
		// A first push also sets the upstream, so a plain `git push` works afterwards.
		if _, err := git(repo.top, "rev-parse", "-q", "--verify", branch+"@{upstream}"); err != nil {
			push = append(push[:2:2], append([]string{"--set-upstream"}, push[2:]...)...)
		}
		push = append(push, branch)
	}
	push = append(push, "refs/tags/"+tag)
	if dryRun {
		fmt.Fprintf(Stdout, "dry run: would run git %s\n", strings.Join(push, " "))
		return nil
	}
	if _, err := git(repo.top, push...); err != nil {
		return err
	}
	fmt.Fprintf(Stdout, "Published %s %s (tag %s on %s)\n", p.M.Name, p.M.Version, tag, remote)
	return nil
}

// publishTests runs the package's tests (with its coverage threshold).
func publishTests(p pkgDir) error {
	cfg, err := loadTestConfig(p.M)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err = runTests(ctx, []pkgDir{p}, testOptions{coverage: cfg.Coverage != nil})
	stop()
	if err != nil {
		if code, silent := ExitCode(err); silent {
			return &exitError{code: code, msg: "the tests failed; not publishing"}
		}
		return err
	}
	fmt.Fprintln(Stderr, "✓ tests pass")
	return nil
}

// localDeps lists dependencies other machines cannot install: file: and
// workspace: sources. devDependencies are only installed in the package's
// own checkout, so they may stay local.
func localDeps(p pkgDir) []string {
	var bad []string
	for field, deps := range map[string]map[string]string{"dependencies": p.M.Dependencies, "peerDependencies": p.M.PeerDependencies} {
		for name, v := range deps {
			if strings.HasPrefix(v, "file:") || strings.HasPrefix(v, "workspace:") {
				bad = append(bad, fmt.Sprintf("%s.%s: %q", field, name, v))
			}
		}
	}
	sort.Strings(bad)
	return bad
}

// root is the workspace root of dir, or dir.
func root(dir string) string {
	if r, _, err := install.FindRoot(dir); err == nil {
		return r
	}
	return dir
}
