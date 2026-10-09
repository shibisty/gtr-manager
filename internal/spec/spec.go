// Package spec parses dependency entries of gtr.json (ADR-0003):
//
//	"orm":       "github:shibisty/orm.go#^0.1"           GitHub, versions from tags
//	"orm-mysql": "github:shibisty/orm.go/drivers/mysql#^0.1"  monorepo subdirectory
//	"passport":  "file:../passport.go"                  local directory, not copied
//	"faker":     "^0.1"                                 the gtr registry (stage 6)
//	"github.com/go-sql-driver/mysql": "^1.10"           an external Go module
//
// The key is always the import path.
package spec

import (
	"fmt"
	"regexp"
	"strings"

	"gtr-manager/internal/semver"
)

// Kind is where a dependency comes from.
type Kind string

const (
	GitHub    Kind = "github"
	File      Kind = "file"
	Registry  Kind = "registry"  // gtr registry, stage 6
	GoModule  Kind = "go"        // proxy.golang.org, step 3.2
	Workspace Kind = "workspace" // a workspace member (ADR-0009)
)

// Spec is a parsed dependency value.
type Spec struct {
	Kind   Kind
	Repo   string // GitHub "owner/repo"
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
	case strings.HasPrefix(value, "gitlab:"), strings.HasPrefix(value, "git+"):
		return s, fmt.Errorf("%s: %s sources are not supported yet; use github: or file:", key, strings.SplitN(value, ":", 2)[0])
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

// String formats a spec back into a gtr.json value.
func (s Spec) String() string {
	switch s.Kind {
	case File:
		return "file:" + s.Path
	case Workspace:
		if s.Raw != "" {
			return s.Raw
		}
		if s.Range == "" {
			return "workspace:*"
		}
		return "workspace:" + s.Range
	case GitHub:
		v := "github:" + s.Repo
		if s.Subdir != "" {
			v += "/" + s.Subdir
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
	return s.Kind == o.Kind && s.Repo == o.Repo && s.Subdir == o.Subdir && s.Path == o.Path
}
