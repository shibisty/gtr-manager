// Package gomodules installs external Go modules (ADR-0003, rule 3): versions
// and files come from a GOPROXY-compatible server, hashes are checked against
// the checksum database (sum.golang.org), and versions are selected with
// minimal version selection like the go command does.
package gomodules

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"gtr-manager/internal/semver"
)

// Escape encodes a module path or version for proxy URLs and file names:
// upper-case letters become "!" + lower case ("github.com/BurntSushi/toml" →
// "github.com/!burnt!sushi/toml").
func Escape(s string) (string, error) {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '!' || r >= unicode.MaxASCII:
			return "", fmt.Errorf("invalid character %q in %q", r, s)
		case 'A' <= r && r <= 'Z':
			b.WriteByte('!')
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune(r)
		}
	}
	return b.String(), nil
}

var (
	majorSuffixRe = regexp.MustCompile(`/v([2-9]|[1-9][0-9]+)$`)
	gopkgInRe     = regexp.MustCompile(`^gopkg\.in/.*\.v([0-9]+)(-unstable)?$`)
	versionRe     = regexp.MustCompile(`^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?(\+incompatible)?$`)
)

// PathMajor returns the major version a module path requires: 2 for
// ".../v2" and "gopkg.in/x.v2", 0 if the path has no suffix (v0 or v1).
func PathMajor(modPath string) int {
	if m := majorSuffixRe.FindStringSubmatch(modPath); m != nil {
		var n int
		fmt.Sscan(m[1], &n)
		return n
	}
	if m := gopkgInRe.FindStringSubmatch(modPath); m != nil {
		var n int
		fmt.Sscan(m[1], &n)
		return n
	}
	return 0
}

// ValidVersion reports whether v is a canonical module version for modPath.
func ValidVersion(modPath, v string) bool {
	m := versionRe.FindStringSubmatch(v)
	if m == nil {
		return false
	}
	var major int
	fmt.Sscan(m[1], &major)
	pm := PathMajor(modPath)
	switch {
	case strings.HasPrefix(modPath, "gopkg.in/"):
		return major == pm || (pm == 0 && major <= 1)
	case pm >= 2:
		return major == pm && m[5] == ""
	case m[5] != "": // +incompatible: v2+ without a go.mod, on a path without suffix
		return major >= 2
	default:
		return major <= 1
	}
}

// Parse returns the semver value of a module version (v and +incompatible
// stripped) for comparisons and range matching.
func Parse(v string) (semver.Version, error) {
	return semver.Parse(strings.TrimSuffix(strings.TrimPrefix(v, "v"), "+incompatible"))
}

// Compare compares two module versions by strict SemVer 2.0 precedence, as
// the go command does: numeric pre-release identifiers numerically,
// alphanumeric ones lexically ("rc10" < "rc9"), build metadata ignored.
func Compare(a, b string) int {
	va, ea := Parse(a)
	vb, eb := Parse(b)
	switch {
	case ea != nil && eb != nil:
		return strings.Compare(a, b)
	case ea != nil:
		return -1
	case eb != nil:
		return 1
	}
	for _, d := range [3]int{va.Major - vb.Major, va.Minor - vb.Minor, va.Patch - vb.Patch} {
		if d != 0 {
			if d < 0 {
				return -1
			}
			return 1
		}
	}
	return comparePre(va.Pre, vb.Pre)
}

func comparePre(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if pa[i] == pb[i] {
			continue
		}
		na, ea := strconv.Atoi(pa[i])
		nb, eb := strconv.Atoi(pb[i])
		switch {
		case ea == nil && eb == nil:
			if na < nb {
				return -1
			}
			return 1
		case ea == nil: // numeric identifiers sort first
			return -1
		case eb == nil:
			return 1
		default:
			return strings.Compare(pa[i], pb[i])
		}
	}
	switch {
	case len(pa) < len(pb):
		return -1
	case len(pa) > len(pb):
		return 1
	}
	return 0
}

var pathElemRe = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

// CheckPath validates a module path the way the go command does in
// essence: slash-separated non-empty elements of letters, digits and
// ".-_~", no "." or ".." elements, no leading dot, and a first element
// that is a lower-case domain-like name with a dot.
func CheckPath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return fmt.Errorf("invalid module path %q", p)
	}
	elems := strings.Split(p, "/")
	for i, el := range elems {
		if !pathElemRe.MatchString(el) || strings.HasPrefix(el, ".") || strings.HasSuffix(el, ".") || el == "~" {
			return fmt.Errorf("invalid module path %q: bad element %q", p, el)
		}
		if i == 0 && (!strings.Contains(el, ".") || strings.ToLower(el) != el || strings.ContainsAny(el, "_~")) {
			return fmt.Errorf("invalid module path %q: the first element must be a domain name", p)
		}
	}
	return nil
}

// IsPseudo reports a pseudo-version (v0.0.0-20200823014737-9f7001d12a5f).
func IsPseudo(v string) bool {
	return regexp.MustCompile(`-(\d+\.)?\d{14}-[0-9a-f]{12}(\+incompatible)?$`).MatchString(v)
}

// Floor returns the lowest version a range admits for modPath, used in the
// go.mod gtr writes for packages ("^1.10" → v1.10.0; "*" → v0.0.0, or
// v2.0.0 for ".../v2").
func Floor(modPath, rng string) string {
	low := "0.0.0"
	if m := regexp.MustCompile(`\d+(\.\d+){0,2}`).FindString(rng); m != "" {
		if v, err := semver.Parse(m); err == nil {
			low = v.String()
		}
	}
	v := "v" + low
	if pm := PathMajor(modPath); pm >= 2 && !strings.HasPrefix(low, fmt.Sprint(pm)+".") {
		v = fmt.Sprintf("v%d.0.0", pm)
	}
	return v
}

// DirName is the directory name of a module version in the store and in
// gtr_modules/.go: the escaped path plus "@" and the version.
func DirName(modPath, version string) (string, error) {
	if err := CheckPath(modPath); err != nil {
		return "", err
	}
	if !ValidVersion(modPath, version) {
		return "", fmt.Errorf("invalid version %q for %s", version, modPath)
	}
	ep, err := Escape(modPath)
	if err != nil {
		return "", err
	}
	ev, err := Escape(version)
	if err != nil {
		return "", err
	}
	for _, el := range strings.Split(ep, "/") {
		if el == "" || el == "." || el == ".." {
			return "", fmt.Errorf("invalid module path %q", modPath)
		}
	}
	return path.Join(ep) + "@" + ev, nil
}
