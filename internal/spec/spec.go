// Package spec parses dependency entries of gtr.json (ADR-0003):
//
//	"orm":       "github:shibisty/orm.go#^0.1"           GitHub, versions from tags
//	"orm-mysql": "github:shibisty/orm.go/drivers/mysql#^0.1"  monorepo subdirectory
//	"passport":  "file:../passport.go"                  local directory, not copied
//	"faker":     "^0.1"                                 the gtr registry
//	"github.com/go-sql-driver/mysql": "^1.10"           an external Go module
//	"preact":    "npm:^10.19"                           an npm package for the frontend (node_modules)
//
// The key is always the import path.
package spec

import (
	"fmt"
	"regexp"
	"strings"

	"gtr-manager/internal/npm"
	"gtr-manager/internal/semver"
)

// Kind is where a dependency comes from.
type Kind string

const (
	GitHub    Kind = "github"
	GitLab    Kind = "gitlab" // GitLab REST API (gitlab.com or GTR_GITLAB_URL)
	Git       Kind = "git"    // any git server through the git command
	File      Kind = "file"
	Registry  Kind = "registry"  // the gtr registry (GTR_REGISTRY)
	GoModule  Kind = "go"        // proxy.golang.org, step 3.2
	Workspace Kind = "workspace" // a workspace member (ADR-0009)
	NPM       Kind = "npm"       // the npm registry: node_modules, not go.mod
)

// Spec is a parsed dependency value.
type Spec struct {
	Kind   Kind
	Repo   string // GitHub "owner/repo"; GitLab project path "group/sub/project"
	URL    string // git: the repository URL without "git+" ("https://host/x.git")
	Subdir string // monorepo subdirectory, slash-separated, "" for the root
	Path   string // file: path as written
	Range  string // version range ("*" if none)
	Raw    string
}

var (
	nameRe     = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
	goModuleRe = regexp.MustCompile(`^[a-z0-9-]+(\.[a-z0-9-]+)+(/[A-Za-z0-9._~-]+)*$`)
	repoRe     = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9._-]{1,100}$`)
	subdirRe   = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
	gitlabRe   = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_.-]*(/[A-Za-z0-9_.][A-Za-z0-9_.-]*)+$`)
)

// reserved names (ADR-0002, item 5); the standard library is checked
// separately against `go list std`.
var reserved = map[string]bool{
	"std": true, "cmd": true, "all": true, "main": true, "tool": true, "work": true,
	"internal": true, "vendor": true, "test": true, "gtr": true, "go": true, "self": true,
}

// IsGoModule reports whether an import path is an external Go module (a dot
// in the first element).
func IsGoModule(key string) bool {
	first, _, _ := strings.Cut(key, "/")
	return strings.Contains(first, ".")
}

// ValidateKey checks a dependency key (ADR-0002, ADR-0003).
func ValidateKey(key string) error {
	if IsGoModule(key) {
		if !goModuleRe.MatchString(key) {
			return fmt.Errorf("invalid Go module path %q", key)
		}
		return nil
	}
	return ValidateName(key)
}

// ValidateName checks a gtr package name.
func ValidateName(name string) error {
	if len(name) < 2 || len(name) > 64 || !nameRe.MatchString(name) {
		return fmt.Errorf("invalid package name %q: lowercase letters, digits and single hyphens, 2–64 characters, starting with a letter", name)
	}
	if reserved[name] {
		return fmt.Errorf("package name %q is reserved", name)
	}
	return nil
}

// ValidateDep checks the key of a dependency with its value: npm packages
// ("npm:...") have npm names ("@preact/signals"), the others are import
// paths.
func ValidateDep(key, value string) error {
	if strings.HasPrefix(strings.TrimSpace(value), "npm:") {
		return npm.ValidName(key)
	}
	return ValidateKey(key)
}

// IsLegacyKey reports a key in the pre-ADR-0003 format ("github:owner/repo").
func IsLegacyKey(key string) bool {
	return strings.HasPrefix(key, "github:") || strings.HasPrefix(key, "gitlab:") || strings.HasPrefix(key, "bitbucket:")
}

// Parse parses the value of a dependency with the given key.
func Parse(key, value string) (Spec, error) {
	value = strings.TrimSpace(value)
	s := Spec{Raw: value, Range: "*"}
	switch {
	case value == "":
		return s, fmt.Errorf("%s: empty dependency value", key)
	case strings.HasPrefix(value, "workspace:"):
		// workspace:* / workspace:^ / workspace:~ (any version of the member)
		// or workspace:<range> (ADR-0009).
		s.Kind = Workspace
		rng := strings.TrimSpace(strings.TrimPrefix(value, "workspace:"))
		if rng != "" && rng != "^" && rng != "~" {
			s.Range = rng
		}
		if _, err := semver.ParseRange(s.Range); err != nil {
			return s, fmt.Errorf("%s: invalid version range in %q", key, value)
		}
		return s, nil
	case strings.HasPrefix(value, "npm:"):
		s.Kind = NPM
		if rng := strings.TrimSpace(strings.TrimPrefix(value, "npm:")); rng != "" {
			s.Range = rng
		}
		if _, err := npm.ParseRange(s.Range); err != nil {
			return s, fmt.Errorf("%s: invalid npm version range in %q", key, value)
		}
		return s, nil
	case strings.HasPrefix(value, "file:"):
		s.Kind, s.Path = File, strings.TrimPrefix(value, "file:")
		if s.Path == "" {
			return s, fmt.Errorf("%s: file: needs a path", key)
		}
		return s, nil
	case strings.HasPrefix(value, "github:"):
		s.Kind = GitHub
		loc, rng, hasRange := strings.Cut(strings.TrimPrefix(value, "github:"), "#")
		if hasRange {
			s.Range = strings.TrimSpace(rng)
		}
		parts := strings.SplitN(loc, "/", 3)
		if len(parts) < 2 || !repoRe.MatchString(parts[0]+"/"+parts[1]) {
			return s, fmt.Errorf("%s: invalid GitHub source %q, expected github:owner/repo[/subdir][#range]", key, value)
		}
		s.Repo = parts[0] + "/" + parts[1]
		if len(parts) == 3 {
			s.Subdir = strings.Trim(parts[2], "/")
			if !subdirRe.MatchString(s.Subdir) || strings.Contains("/"+s.Subdir+"/", "/../") || strings.Contains("/"+s.Subdir+"/", "/./") {
				return s, fmt.Errorf("%s: invalid subdirectory %q", key, parts[2])
			}
		}
	case strings.HasPrefix(value, "gitlab:"):
		// gitlab:group[/subgroup...]/project[//subdir][#range]: a GitLab
		// project path may have any depth, so a subdirectory follows "//".
		s.Kind = GitLab
		loc, rng, hasRange := strings.Cut(strings.TrimPrefix(value, "gitlab:"), "#")
		if hasRange {
			s.Range = strings.TrimSpace(rng)
		}
		project, sub, _ := strings.Cut(loc, "//")
		if !gitlabRe.MatchString(project) || strings.Contains("/"+project+"/", "/../") || strings.Contains("/"+project+"/", "/./") {
			return s, fmt.Errorf("%s: invalid GitLab source %q, expected gitlab:group/project[//subdir][#range]", key, value)
		}
		s.Repo = project
		if err := s.setSubdir(key, sub); err != nil {
			return s, err
		}
	case strings.HasPrefix(value, "git+"):
		// git+https://host/path/repo.git[//subdir][#range], also git+ssh://
		// and git+file:// (a local repository).
		s.Kind = Git
		loc, rng, hasRange := strings.Cut(strings.TrimPrefix(value, "git+"), "#")
		if hasRange {
			s.Range = strings.TrimSpace(rng)
		}
		scheme, rest, ok := strings.Cut(loc, "://")
		switch {
		case !ok || rest == "":
			return s, fmt.Errorf("%s: invalid git source %q, expected git+https://host/repo.git[//subdir][#range]", key, value)
		case scheme == "http":
			return s, fmt.Errorf("%s: %q: plain http is not allowed for git sources; use git+https://", key, value)
		case scheme != "https" && scheme != "ssh" && scheme != "file":
			return s, fmt.Errorf("%s: %q: unsupported scheme %q (git+https, git+ssh or git+file)", key, value, scheme)
		}
		repoPath, sub, _ := strings.Cut(rest, "//")
		if scheme == "file" && strings.HasPrefix(rest, "/") {
			// file:///abs/path: the path starts after the third slash
			repoPath, sub, _ = strings.Cut(rest[1:], "//")
			repoPath = "/" + repoPath
		}
		if strings.ContainsAny(repoPath, " \t\n") || repoPath == "" || repoPath == "/" {
			return s, fmt.Errorf("%s: invalid git source %q", key, value)
		}
		s.URL = scheme + "://" + strings.TrimRight(repoPath, "/")
		if err := s.setSubdir(key, sub); err != nil {
			return s, err
		}
	default:
		s.Range = value
		s.Kind = Registry
		if IsGoModule(key) {
			s.Kind = GoModule
		}
	}
	if _, err := semver.ParseRange(s.Range); err != nil {
		return s, fmt.Errorf("%s: invalid version range %q", key, s.Range)
	}
	return s, nil
}

func (s *Spec) setSubdir(key, sub string) error {
	sub = strings.Trim(sub, "/")
	if sub == "" {
		return nil
	}
	if !subdirRe.MatchString(sub) || strings.Contains("/"+sub+"/", "/../") || strings.Contains("/"+sub+"/", "/./") {
		return fmt.Errorf("%s: invalid subdirectory %q", key, sub)
	}
	s.Subdir = sub
	return nil
}

// IsGit reports whether the source is a git repository (versions are tags).
func (s Spec) IsGit() bool { return s.Kind == GitHub || s.Kind == GitLab || s.Kind == Git }

// String formats a spec back into a gtr.json value.
func (s Spec) String() string {
	switch s.Kind {
	case File:
		return "file:" + s.Path
	case NPM:
		if s.Range == "*" {
			return "npm:*"
		}
		return "npm:" + s.Range
	case Workspace:
		if s.Raw != "" {
			return s.Raw
		}
		if s.Range == "" {
			return "workspace:*"
		}
		return "workspace:" + s.Range
	case GitHub, GitLab, Git:
		var v string
		switch s.Kind {
		case GitHub:
			v = "github:" + s.Repo
			if s.Subdir != "" {
				v += "/" + s.Subdir
			}
		case GitLab:
			v = "gitlab:" + s.Repo
			if s.Subdir != "" {
				v += "//" + s.Subdir
			}
		default:
			v = "git+" + s.URL
			if s.Subdir != "" {
				v += "//" + s.Subdir
			}
		}
		if s.Range != "" && s.Range != "*" {
			v += "#" + s.Range
		}
		return v
	default:
		return s.Range
	}
}

// SameSource reports whether two specs point at the same place, ignoring
// the version range.
func (s Spec) SameSource(o Spec) bool {
	return s.Kind == o.Kind && s.Repo == o.Repo && s.URL == o.URL && s.Subdir == o.Subdir && s.Path == o.Path
}
