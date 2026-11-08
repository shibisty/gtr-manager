package source

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gtr-manager/internal/archive"
	"gtr-manager/internal/semver"
)

// TagRef is a git tag and the commit it points at.
type TagRef struct {
	Name   string
	Commit string
}

// tagCandidates turns tags into versions of the package name (in subdir for
// a monorepo), highest first; tags of other packages and non-SemVer tags
// are skipped.
func tagCandidates(tags []TagRef, name, subdir string) []Candidate {
	seen := map[string]bool{}
	var out []Candidate
	for _, t := range tags {
		v := TagVersion(t.Name, name, subdir)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, Candidate{Version: v, Tag: t.Name, Commit: t.Commit})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return semver.Compare(semver.MustParse(out[i].Version), semver.MustParse(out[j].Version)) > 0
	})
	return out
}

// placeArchive extracts a repository zip (one top directory, as GitHub and
// GitLab produce, or the files directly) inside work and moves the package
// directory (subdir, or the whole tree) to dst.
func placeArchive(zipPath, work, subdir, dst, label, version string) error {
	tree := filepath.Join(work, "tree")
	if err := archive.Extract(zipPath, tree); err != nil {
		return err
	}
	entries, err := os.ReadDir(tree)
	if err != nil {
		return err
	}
	root := tree
	if len(entries) == 1 && entries[0].IsDir() {
		root = filepath.Join(tree, entries[0].Name()) // owner-repo-<sha>/
	}
	if subdir != "" {
		root = filepath.Join(root, filepath.FromSlash(subdir))
		if st, err := os.Stat(root); err != nil || !st.IsDir() {
			return fmt.Errorf("%s at %s has no directory %s", label, version, subdir)
		}
	}
	return os.Rename(root, dst)
}
