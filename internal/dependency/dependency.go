// Package dependency parses a package spec given on the command line:
// "github:owner/repo", "github:owner/repo@1.2.0", "name@^1".
package dependency

import (
	"fmt"
	"regexp"
	"strings"
)

// Dependency is a parsed spec.
type Dependency struct {
	Type       string // "github", "gitlab", "bitbucket", "git", "gtr"
	Repository string // "owner/repo" for hosting services, URL for git, name for gtr
	Version    string // "*" by default
}

var (
	hostedRe  = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9._-]{1,100}$`)
	gtrNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(?:/[A-Za-z0-9][A-Za-z0-9._-]*)*$`)
	versionRe = regexp.MustCompile(`^[0-9A-Za-z.^~*+<>=_ |-]+$`)
)

// Parse parses a spec and validates names: they become paths on disk, so
// "..", absolute paths and the like are rejected.
func Parse(s string) (*Dependency, error) {
	s = strings.TrimSpace(s)
	version := "*"
	// An "@" in a URL like https://user@host/... is not treated as a version.
	if i := strings.LastIndex(s, "@"); i > 0 && !strings.Contains(s[i:], "/") {
		version = strings.TrimSpace(s[i+1:])
		s = strings.TrimSpace(s[:i])
		if version == "" {
			return nil, fmt.Errorf("empty version after @")
		}
	}
	if s == "" {
		return nil, fmt.Errorf("package name is required")
	}
	if !versionRe.MatchString(version) {
		return nil, fmt.Errorf("invalid version %q", version)
	}
	dep := &Dependency{Type: "gtr", Repository: s, Version: version}
	for _, host := range []string{"github", "gitlab", "bitbucket"} {
		if rest, ok := strings.CutPrefix(s, host+":"); ok {
			dep.Type, dep.Repository = host, rest
			if !hostedRe.MatchString(rest) || strings.HasSuffix(rest, "/.") || strings.HasSuffix(rest, "/..") {
				return nil, fmt.Errorf("invalid %s repository %q: expected owner/repo", host, rest)
			}
			return dep, nil
		}
	}
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		dep.Type = "git"
		return dep, nil
	}
	if !gtrNameRe.MatchString(s) || strings.Contains(s, "..") {
		return nil, fmt.Errorf("invalid package name %q", s)
	}
	return dep, nil
}

// Key returns the key in the gtr.json dependencies: "github:owner/repo" for
// hosting services, URL for git, name for gtr packages.
func (d *Dependency) Key() string {
	switch d.Type {
	case "github", "gitlab", "bitbucket":
		return d.Type + ":" + d.Repository
	default:
		return d.Repository
	}
}

// String returns the spec in a form suitable for the command line.
func (d *Dependency) String() string {
	if d.Version == "" || d.Version == "*" {
		return d.Key()
	}
	return d.Key() + "@" + d.Version
}
