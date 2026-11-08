package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DotenvFiles are read from each directory, later ones overriding earlier
// ones: .env is usually committed, .env.local holds local secrets.
var DotenvFiles = []string{".env", ".env.local"}

// loadDotenv reads the .env files of dirs (later directories override
// earlier ones) and returns KEY=VALUE pairs in the order they were defined.
func loadDotenv(dirs ...string) ([]string, error) {
	vars := map[string]string{}
	var order []string
	// ${VAR} sees what the script will see: the environment wins over .env
	// files (see withDefaults).
	lookup := func(k string) (string, bool) {
		if v, ok := os.LookupEnv(k); ok {
			return v, true
		}
		v, ok := vars[k]
		return v, ok
	}
	for _, dir := range dirs {
		for _, name := range DotenvFiles {
			path := filepath.Join(dir, name)
			data, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			pairs, err := parseDotenv(string(data), lookup)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			for _, kv := range pairs {
				if _, seen := vars[kv[0]]; !seen {
					order = append(order, kv[0])
				}
				vars[kv[0]] = kv[1]
			}
		}
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+vars[k])
	}
	return out, nil
}

// parseDotenv parses KEY=VALUE lines:
//
//	# comment
//	export KEY=value       ("export " is ignored)
//	KEY=value # comment    (unquoted: trimmed, # after a space starts a comment)
//	KEY="a\nb ${OTHER}"    (double quotes: \n \t \" \\ escapes, ${VAR} and $VAR expanded)
//	KEY='literal $X'       (single quotes: taken as is)
//
// Values may span lines inside quotes. ${VAR} is resolved by lookup (the
// environment and earlier files), then by variables defined earlier in this
// file; unknown ones expand to "".
func parseDotenv(data string, lookup func(string) (string, bool)) ([][2]string, error) {
	var out [][2]string
	defined := map[string]string{}
	get := func(k string) string {
		if v, ok := lookup(k); ok {
			return v
		}
		return defined[k]
	}
	data = strings.ReplaceAll(strings.TrimPrefix(data, "\ufeff"), "\r\n", "\n")
	line := 1
	for len(data) > 0 {
		var raw string
		raw, data, _ = strings.Cut(data, "\n")
		start := line
		line++
		text := strings.TrimSpace(raw)
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		text = strings.TrimSpace(strings.TrimPrefix(text, "export "))
		key, value, ok := strings.Cut(text, "=")
		key = strings.TrimSpace(key)
		if !ok || !validEnvKey(key) {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE", start)
		}
		value = strings.TrimLeft(value, " \t")
		switch {
		case strings.HasPrefix(value, `"`) || strings.HasPrefix(value, "'"):
			q := value[0]
			body := value[1:]
			// The closing quote may be on a later line.
			for {
				if end := closingQuote(body, q); end >= 0 {
					rest := strings.TrimSpace(body[end+1:])
					if rest != "" && !strings.HasPrefix(rest, "#") {
						return nil, fmt.Errorf("line %d: unexpected text after the closing quote", start)
					}
					body = body[:end]
					break
				}
				if data == "" {
					return nil, fmt.Errorf("line %d: unterminated quote", start)
				}
				var next string
				next, data, _ = strings.Cut(data, "\n")
				line++
				body += "\n" + next
			}
			if q == '"' {
				body = expand(unescape(body), get)
			}
			value = body
		default:
			if i := strings.Index(value, " #"); i >= 0 {
				value = value[:i]
			}
			if i := strings.Index(value, "\t#"); i >= 0 {
				value = value[:i]
			}
			value = expand(strings.TrimSpace(value), get)
		}
		defined[key] = value
		out = append(out, [2]string{key, value})
	}
	return out, nil
}

func validEnvKey(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && (r >= '0' && r <= '9' || r == '.')) {
			return false
		}
	}
	return true
}

// closingQuote finds the unescaped closing quote q in s (backslash escapes
// count only inside double quotes).
func closingQuote(s string, q byte) int {
	for i := 0; i < len(s); i++ {
		if q == '"' && s[i] == '\\' {
			i++
			continue
		}
		if s[i] == q {
			return i
		}
	}
	return -1
}

func unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '$':
			b.WriteString("\x00$") // a literal $, protected from expansion
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// expand replaces ${VAR} and $VAR.
func expand(s string, get func(string) string) string {
	out := os.Expand(strings.ReplaceAll(s, "\x00$", "\x00"), get)
	return strings.ReplaceAll(out, "\x00", "$")
}

// withDefaults adds the KEY=VALUE pairs of extra that env does not set.
func withDefaults(env, extra []string) []string {
	if len(extra) == 0 {
		return env
	}
	norm := func(k string) string {
		if runtime.GOOS == "windows" {
			return strings.ToUpper(k)
		}
		return k
	}
	have := map[string]bool{}
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		have[norm(k)] = true
	}
	out := append([]string(nil), env...)
	for _, kv := range extra {
		k, _, _ := strings.Cut(kv, "=")
		if !have[norm(k)] {
			out = append(out, kv)
		}
	}
	return out
}
