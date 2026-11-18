package progress

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// screen replays \r and \b the way a terminal does, line by line.
func screen(out string) []string {
	var lines []string
	cur := []rune{}
	pos := 0
	for _, r := range out {
		switch r {
		case '\n':
			lines = append(lines, strings.TrimRight(string(cur), " "))
			cur, pos = []rune{}, 0
		case '\r':
			pos = 0
		case '\b':
			if pos > 0 {
				pos--
			}
		default:
			if pos < len(cur) {
				cur[pos] = r
			} else {
				cur = append(cur, r)
			}
			pos++
		}
	}
	return append(lines, strings.TrimRight(string(cur), " "))
}

func TestTerminal(t *testing.T) {
	var buf bytes.Buffer
	b := New(&buf, true)
	b.Phase("Resolving", 0)
	b.Start("passport")
	b.Done("passport")
	b.Phase("Installing", 4)
	b.Start("orm")
	b.Done("orm@0.2.1")
	b.Done("@passport/oauth2@0.2.0")
	fmt.Fprintf(b, "Downloading @passport/oauth2@0.2.0\n")
	s := screen(buf.String())
	if len(s) != 2 || s[0] != "Downloading @passport/oauth2@0.2.0" ||
		s[1] != "Installing   [############............] 2/4  @passport/oauth2@0.2.0" {
		t.Fatalf("%q", s)
	}
	b.Stop()
	if s := screen(buf.String()); s[len(s)-1] != "" {
		t.Fatalf("the status line is left: %q", s)
	}
	fmt.Fprintf(b, "4 packages installed\n") // after Stop: straight through
	if s := screen(buf.String()); s[len(s)-2] != "4 packages installed" {
		t.Fatalf("%q", s)
	}
}

func TestLongItemIsCut(t *testing.T) {
	var buf bytes.Buffer
	b := New(&buf, true)
	b.Phase("Go modules", 1)
	b.Start(strings.Repeat("x", 200))
	b.Stop()
	for _, l := range strings.Split(strings.ReplaceAll(buf.String(), "\r", "\n"), "\n") {
		if n := len([]rune(strings.TrimRight(l, " \b"))); n > 78 {
			t.Fatalf("%d runes: %q", n, l)
		}
	}
}

func TestPlain(t *testing.T) {
	var buf bytes.Buffer
	b := New(&buf, false)
	b.Phase("Resolving", 0)
	b.Start("x")
	b.Done("x")
	b.Phase("Installing", 3)
	fmt.Fprintf(b, "Downloading orm@0.2.1\n")
	b.Stop()
	if got := buf.String(); got != "Resolving...\nInstalling (3)...\nDownloading orm@0.2.1\n" {
		t.Fatalf("%q", got)
	}
}

func TestNil(t *testing.T) {
	var b *Bar
	b.Phase("x", 1)
	b.Start("y")
	b.Done("y")
	b.Stop()
}
