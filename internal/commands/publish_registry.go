package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gtr-manager/internal/install"
	"gtr-manager/internal/registry"
)

// publishToRegistry packs the package and uploads it.
func publishToRegistry(p pkgDir, c *registry.Client, dryRun, skipTests, allowDirty bool, step func(string, ...any)) error {
	if c.Token == "" && !dryRun {
		return fmt.Errorf("not logged in to %s: run `gtr login --registry %s`", c.URL, c.URL)
	}
	repo, gitErr := findRepo(p.Dir)
	if gitErr == nil && !allowDirty {
		if d, err := repo.dirty(); err != nil {
			return err
		} else if len(d) > 0 {
			return fmt.Errorf("the repository has uncommitted changes (--allow-dirty to publish them anyway):\n  %s", strings.Join(firstN(d, 8), "\n  "))
		}
		step("the repository is clean")
	}
	if links, err := install.LoadLinks(filepath.Join(root(p.Dir), filepath.FromSlash(install.LinksFile))); err != nil {
		return err
	} else if len(links) > 0 {
		return errors.New("packages are linked to local copies (gtr link): the tests would not test what others install; run `gtr unlink` first")
	}
	if !dryRun {
		ctx, stop := signalContext()
		ix, err := c.Package(ctx, p.M.Name)
		stop()
		switch {
		case registry.IsNotFound(err):
		case err != nil:
			return err
		default:
			for _, v := range ix.Versions {
				if v.Version == p.M.Version {
					return fmt.Errorf("%s %s is published already in %s; bump it with `gtr version patch`", p.M.Name, p.M.Version, c.URL)
				}
			}
		}
		step("%s does not have %s %s yet", c.URL, p.M.Name, p.M.Version)
	}
	if skipTests {
		fmt.Fprintln(Stderr, "- tests skipped (--skip-tests)")
	} else if err := publishTests(p); err != nil {
		return err
	}

	files, err := packageFiles(p.Dir, gitErr == nil)
	if err != nil {
		return err
	}
	archive, err := registry.Pack(p.Dir, files)
	if err != nil {
		return err
	}
	if len(archive) > 50<<20 {
		return fmt.Errorf("the archive is %d MB, more than the registry takes (50 MB)", len(archive)>>20)
	}
	sum := sha256.Sum256(archive)
	step("archive: %d files, %s, sha256 %s", len(files), size(len(archive)), hex.EncodeToString(sum[:])[:12])
	if dryRun {
		for _, f := range files {
			fmt.Fprintln(Stdout, "  "+f)
		}
		fmt.Fprintf(Stdout, "dry run: would publish %s %s to %s\n", p.M.Name, p.M.Version, c.URL)
		return nil
	}
	ctx, stop := signalContext()
	defer stop()
	if _, err := c.Publish(ctx, p.M.Name, p.M.Version, archive); err != nil {
		return err
	}
	fmt.Fprintf(Stdout, "Published %s %s to %s\n", p.M.Name, p.M.Version, c.URL)
	return nil
}

func size(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// excluded are never part of a package archive: what gtr generates or
// installs, build output, secrets, CI settings and VCS data.
func excluded(rel string, dir bool) bool {
	base := path.Base(rel)
	if dir {
		switch base {
		case ".git", ".hg", ".svn", "gtr_modules", "node_modules", ".idea", ".vscode":
			return true
		}
		if rel == ".github" || rel == ".gitlab" {
			return true // CI settings are not part of the package
		}
		return rel == "build" || rel == "public/build" || rel == "storage"
	}
	switch base {
	case "go.mod", "go.sum", "go.work", "go.work.sum", "gtr.lock", ".gtr-links.json", "coverage.out", ".DS_Store":
		return true
	}
	return strings.HasPrefix(base, ".env") && base != ".env.example" || strings.HasSuffix(base, ".test") || strings.HasSuffix(base, ".exe")
}

// packageFiles lists the files to publish, sorted: those git does not
// ignore (in a repository), or all of the directory, without excluded ones.
func packageFiles(dir string, inGit bool) ([]string, error) {
	var files []string
	if inGit {
		out, err := git(dir, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", ".")
		if err != nil {
			return nil, err
		}
		for _, f := range strings.Split(out, "\x00") {
			if f == "" {
				continue
			}
			skip := false
			parts := strings.Split(f, "/")
			for i := range parts[:len(parts)-1] {
				if excluded(strings.Join(parts[:i+1], "/"), true) {
					skip = true
					break
				}
			}
			if !skip && !excluded(f, false) {
				if st, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(f))); err == nil && st.Mode().IsRegular() {
					files = append(files, f)
				}
			}
		}
	} else {
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(dir, p)
			rel = filepath.ToSlash(rel)
			if rel == "." {
				return nil
			}
			if d.IsDir() {
				if excluded(rel, true) {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type().IsRegular() && !excluded(rel, false) {
				files = append(files, rel)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	hasManifest := false
	for _, f := range files {
		hasManifest = hasManifest || f == "gtr.json"
	}
	if !hasManifest {
		return nil, errors.New("gtr.json is not among the files to publish (is it ignored by git?)")
	}
	return files, nil
}
