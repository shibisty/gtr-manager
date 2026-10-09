package gomodules

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"
)

// ModFile is the part of a go.mod that version selection needs.
type ModFile struct {
	Module  string
	Go      string            // the go line as written ("1.21", "1.22.0"), "" if absent
	Retract []Interval        // retracted versions (only meaningful in the latest go.mod)
	Require map[string]string // path → version
}

// ParseModFile reads module, go and require directives.
func ParseModFile(data []byte) ModFile {
	mf := ModFile{Require: map[string]string{}}
	block := ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if block != "" {
			if f[0] == ")" {
				block = ""
				continue
			}
			if block == "require" && len(f) >= 2 {
				mf.Require[unquote(f[0])] = unquote(f[1])
			}
			if block == "retract" {
				mf.Retract = append(mf.Retract, parseInterval(f))
			}
			continue
		}
		switch {
		case len(f) == 2 && f[1] == "(":
			block = f[0]
		case f[0] == "module" && len(f) >= 2:
			mf.Module = unquote(f[1])
		case f[0] == "go" && len(f) >= 2:
			mf.Go = f[1]
		case f[0] == "retract" && len(f) >= 2:
			mf.Retract = append(mf.Retract, parseInterval(f[1:]))
		case f[0] == "require" && len(f) >= 3:
			mf.Require[unquote(f[1])] = unquote(f[2])
		}
	}
	return mf
}

// Interval is a retracted version or [Low, High] range.
type Interval struct{ Low, High string }

func parseInterval(f []string) Interval {
	joined := strings.Join(f, "")
	if strings.HasPrefix(joined, "[") {
		lo, hi, _ := strings.Cut(strings.Trim(joined, "[]"), ",")
		return Interval{strings.TrimSpace(lo), strings.TrimSpace(hi)}
	}
	return Interval{joined, joined}
}

// Retracted reports whether v is retracted by this go.mod.
func (m ModFile) Retracted(v string) bool {
	for _, iv := range m.Retract {
		if Compare(iv.Low, v) <= 0 && Compare(v, iv.High) <= 0 {
			return true
		}
	}
	return false
}

func unquote(s string) string {
	if u, err := strconv.Unquote(s); err == nil {
		return u
	}
	return s
}

// Pruned reports whether the module uses graph pruning (go 1.17 or later):
// its go.mod already lists everything its packages need, so the go command
// does not look at its dependencies' requirements.
func (m ModFile) Pruned() bool {
	parts := strings.SplitN(m.Go, ".", 3)
	if len(parts) < 2 {
		return false
	}
	maj, _ := strconv.Atoi(parts[0])
	min, _ := strconv.Atoi(strings.TrimRightFunc(parts[1], func(r rune) bool { return r < '0' || r > '9' }))
	return maj > 1 || maj == 1 && min >= 17
}
