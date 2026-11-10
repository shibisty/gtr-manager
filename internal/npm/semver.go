package npm

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is an npm (SemVer 2.0) version: build metadata is kept but
// ignored in comparisons.
type Version struct {
	Major, Minor, Patch int
	Pre                 []string
	Build               string
	raw                 string
}

func (v Version) String() string { return v.raw }

// ParseVersion parses "1.2.3", "v1.2.3", "1.2.3-rc.1+build.5".
func ParseVersion(s string) (Version, error) {
	raw := strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(raw, "="), "v")
	v := Version{raw: s}
	if i := strings.IndexByte(s, '+'); i >= 0 {
		v.Build, s = s[i+1:], s[:i]
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		v.Pre = strings.Split(s[i+1:], ".")
		s = s[:i]
		for _, p := range v.Pre {
			if p == "" {
				return v, fmt.Errorf("invalid version %q", raw)
			}
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("invalid version %q", raw)
	}
	nums := [3]int{}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" || len(p) > 1 && p[0] == '0' {
			return v, fmt.Errorf("invalid version %q", raw)
		}
		nums[i] = n
	}
	v.Major, v.Minor, v.Patch = nums[0], nums[1], nums[2]
	return v, nil
}

// Compare orders versions by SemVer precedence.
func Compare(a, b Version) int {
	for _, d := range [3]int{a.Major - b.Major, a.Minor - b.Minor, a.Patch - b.Patch} {
		if d != 0 {
			if d < 0 {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a.Pre) == 0 && len(b.Pre) == 0:
		return 0
	case len(a.Pre) == 0:
		return 1
	case len(b.Pre) == 0:
		return -1
	}
	for i := 0; i < len(a.Pre) && i < len(b.Pre); i++ {
		x, y := a.Pre[i], b.Pre[i]
		xn, xerr := strconv.Atoi(x)
		yn, yerr := strconv.Atoi(y)
		switch {
		case xerr == nil && yerr == nil:
			if xn != yn {
				if xn < yn {
					return -1
				}
				return 1
			}
		case xerr == nil:
			return -1 // numeric identifiers sort before alphanumeric ones
		case yerr == nil:
			return 1
		case x != y:
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a.Pre) < len(b.Pre):
		return -1
	case len(a.Pre) > len(b.Pre):
		return 1
	}
	return 0
}

// comparator is one bound: op is "<", "<=", ">", ">=" or "=".
type comparator struct {
	op string
	v  Version
}

func (c comparator) match(v Version) bool {
	d := Compare(v, c.v)
	switch c.op {
	case "<":
		return d < 0
	case "<=":
		return d <= 0
	case ">":
		return d > 0
	case ">=":
		return d >= 0
	}
	return d == 0
}

// Range is an npm version range: sets of comparators joined by "||".
type Range struct {
	raw  string
	sets [][]comparator
}

func (r Range) String() string { return r.raw }

// ParseRange parses an npm range: "^1.2.3", "~1.2", "1.x", ">=1 <2",
// "1.2.3 - 2.0", "*", "", and alternatives joined by "||".
func ParseRange(s string) (Range, error) {
	r := Range{raw: strings.TrimSpace(s)}
	for _, alt := range strings.Split(r.raw, "||") {
		set, err := parseSet(strings.TrimSpace(alt))
		if err != nil {
			return r, fmt.Errorf("invalid range %q: %w", s, err)
		}
		r.sets = append(r.sets, set)
	}
	return r, nil
}

// partial is a version with missing or wildcard parts ("1", "1.2", "1.x").
type partial struct {
	nums [3]int
	n    int // how many parts are given (0–3)
	pre  []string
}

func parsePartial(s string) (partial, error) {
	var p partial
	s = strings.TrimPrefix(strings.TrimPrefix(s, "="), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		p.pre = strings.Split(s[i+1:], ".")
		s = s[:i]
	}
	if s == "" || s == "*" || s == "x" || s == "X" {
		return p, nil
	}
	for i, part := range strings.Split(s, ".") {
		if i > 2 {
			return p, fmt.Errorf("%q has too many parts", s)
		}
		if part == "x" || part == "X" || part == "*" {
			break
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return p, fmt.Errorf("%q is not a version", s)
		}
		p.nums[i] = n
		p.n = i + 1
	}
	return p, nil
}

func (p partial) version() Version {
	v := Version{Major: p.nums[0], Minor: p.nums[1], Patch: p.nums[2]}
	if p.n == 3 {
		v.Pre = p.pre
	}
	return v
}

// next is the smallest version above every version p stands for
// (1.2 → 1.3.0-0).
func (p partial) next() Version {
	switch p.n {
	case 1:
		return Version{Major: p.nums[0] + 1, Pre: []string{"0"}}
	case 2:
		return Version{Major: p.nums[0], Minor: p.nums[1] + 1, Pre: []string{"0"}}
	}
	return Version{Major: p.nums[0], Minor: p.nums[1], Patch: p.nums[2] + 1, Pre: []string{"0"}}
}

func parseSet(s string) ([]comparator, error) {
	if s == "" || s == "*" || s == "x" || s == "latest" {
		return nil, nil // any version
	}
	// Hyphen range: "1.2.3 - 2.3".
	if lo, hi, ok := strings.Cut(s, " - "); ok {
		a, err := parsePartial(strings.TrimSpace(lo))
		if err != nil {
			return nil, err
		}
		b, err := parsePartial(strings.TrimSpace(hi))
		if err != nil {
			return nil, err
		}
		set := []comparator{{">=", a.version()}}
		switch {
		case b.n == 0:
		case b.n < 3:
			set = append(set, comparator{"<", b.next()})
		default:
			set = append(set, comparator{"<=", b.version()})
		}
		return set, nil
	}
	// Operators may be separated from versions by spaces (">= 1.2").
	fields := strings.Fields(s)
	var toks []string
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if strings.Trim(f, "<>=~^") == "" && i+1 < len(fields) {
			f += fields[i+1]
			i++
		}
		toks = append(toks, f)
	}
	var set []comparator
	for _, t := range toks {
		cs, err := parseComparator(t)
		if err != nil {
			return nil, err
		}
		set = append(set, cs...)
	}
	return set, nil
}

func parseComparator(t string) ([]comparator, error) {
	op := ""
	for _, o := range []string{">=", "<=", ">", "<", "=", "^", "~>", "~"} {
		if strings.HasPrefix(t, o) {
			op, t = o, t[len(o):]
			break
		}
	}
	p, err := parsePartial(t)
	if err != nil {
		return nil, err
	}
	v := p.version()
	switch op {
	case "^":
		if p.n == 0 {
			return nil, nil
		}
		var hi Version
		switch {
		case p.nums[0] > 0 || p.n == 1:
			hi = Version{Major: p.nums[0] + 1, Pre: []string{"0"}}
		case p.nums[1] > 0 || p.n == 2:
			hi = Version{Minor: p.nums[1] + 1, Pre: []string{"0"}}
		default:
			hi = Version{Patch: p.nums[2] + 1, Pre: []string{"0"}}
		}
		return []comparator{{">=", v}, {"<", hi}}, nil
	case "~", "~>":
		if p.n == 0 {
			return nil, nil
		}
		hi := Version{Major: p.nums[0] + 1, Pre: []string{"0"}}
		if p.n >= 2 {
			hi = Version{Major: p.nums[0], Minor: p.nums[1] + 1, Pre: []string{"0"}}
		}
		return []comparator{{">=", v}, {"<", hi}}, nil
	case "", "=":
		if p.n == 0 {
			return nil, nil
		}
		if p.n < 3 { // "1.2" is 1.2.x
			return []comparator{{">=", v}, {"<", p.next()}}, nil
		}
		return []comparator{{"=", v}}, nil
	case ">":
		if p.n < 3 && p.n > 0 { // >1.2 is >=1.3.0
			return []comparator{{">=", p.next()}}, nil
		}
		return []comparator{{">", v}}, nil
	case "<=":
		if p.n < 3 && p.n > 0 { // <=1.2 is <1.3.0-0
			return []comparator{{"<", p.next()}}, nil
		}
		return []comparator{{"<=", v}}, nil
	case "<":
		if p.n < 3 {
			v.Pre = []string{"0"}
		}
		return []comparator{{"<", v}}, nil
	}
	return []comparator{{op, v}}, nil // ">="
}

// Match reports whether v is in the range. A pre-release matches only a
// set with a comparator of the same major.minor.patch that has a
// pre-release itself (npm's rule: ^1.2.3-beta.1 allows 1.2.3-beta.2, not
// 1.2.4-beta.1).
func (r Range) Match(v Version) bool {
	for _, set := range r.sets {
		ok := true
		for _, c := range set {
			if !c.match(v) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if len(v.Pre) == 0 {
			return true
		}
		for _, c := range set {
			if len(c.v.Pre) > 0 && c.v.Pre[0] != "0" && c.v.Major == v.Major && c.v.Minor == v.Minor && c.v.Patch == v.Patch {
				return true
			}
		}
	}
	return false
}
