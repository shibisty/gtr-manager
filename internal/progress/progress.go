// Package progress shows what a long command is doing: a status line at the
// bottom of a terminal (the phase, a bar or a counter, the current item),
// with log lines printed above it. Without a terminal (CI, pipes, MCP) only
// the log lines and one line per phase are written.
package progress

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Reporter is what an operation tells about itself. A nil *Bar is a valid
// Reporter that does nothing.
type Reporter interface {
	// Phase starts a stage: "Resolving", "Downloading"; total is the
	// number of steps, 0 when unknown (a counter instead of a bar).
	Phase(name string, total int)
	// Start shows the item being worked on.
	Start(item string)
	// Done counts a finished step.
	Done(item string)
}

// Bar is a Reporter and an io.Writer for log lines.
type Bar struct {
	w     io.Writer
	tty   bool
	width int

	mu       sync.Mutex
	phase    string
	total    int
	done     int
	item     string
	shown    int // length of the status line on screen
	frame    int
	lastDraw time.Time
	stop     chan struct{}
	started  time.Time
}

// New returns a Bar writing to w; tty selects the live status line.
func New(w io.Writer, tty bool) *Bar {
	return &Bar{w: w, tty: tty, width: 78, started: time.Now()}
}

// ForStderr returns a Bar on os.Stderr that redraws only in an interactive
// terminal: not in CI, not under MCP (GTR_MCP), not with TERM=dumb or
// GTR_PROGRESS=0.
func ForStderr() *Bar {
	return New(os.Stderr, Interactive(os.Stderr))
}

// Interactive reports whether f is a terminal a status line can be drawn on.
func Interactive(f *os.File) bool {
	switch {
	case os.Getenv("GTR_PROGRESS") == "0", os.Getenv("GTR_MCP") != "", os.Getenv("CI") != "",
		os.Getenv("TERM") == "dumb":
		return false
	case os.Getenv("GTR_PROGRESS") == "1":
		return true
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// Phase implements Reporter.
func (b *Bar) Phase(name string, total int) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.phase, b.total, b.done, b.item = name, total, 0, ""
	if !b.tty {
		if total > 0 {
			fmt.Fprintf(b.w, "%s (%d)...\n", name, total)
		} else {
			fmt.Fprintf(b.w, "%s...\n", name)
		}
		return
	}
	if b.stop == nil {
		b.stop = make(chan struct{})
		go b.spin(b.stop)
	}
	b.draw(true)
}

// Start implements Reporter.
func (b *Bar) Start(item string) {
	if b == nil || !b.tty {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.item = item
	b.draw(false)
}

// Done implements Reporter.
func (b *Bar) Done(item string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.done++
	if item != "" {
		b.item = item
	}
	if b.tty {
		b.draw(false)
	}
}

// Write prints log lines above the status line.
func (b *Bar) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.tty || b.phase == "" {
		return b.w.Write(p)
	}
	b.clear()
	n, err := b.w.Write(p)
	b.draw(true)
	return n, err
}

// Stop removes the status line; log lines keep going straight through.
func (b *Bar) Stop() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stop != nil {
		close(b.stop)
		b.stop = nil
	}
	if b.tty {
		b.clear()
	}
	b.phase = ""
}

func (b *Bar) spin(stop chan struct{}) {
	t := time.NewTicker(120 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			b.mu.Lock()
			if b.phase != "" {
				b.frame++
				b.draw(true)
			}
			b.mu.Unlock()
		}
	}
}

func (b *Bar) clear() {
	if b.shown > 0 {
		fmt.Fprintf(b.w, "\r%s\r", strings.Repeat(" ", b.shown))
		b.shown = 0
	}
}

// draw redraws the status line; without force, at most every 50 ms.
func (b *Bar) draw(force bool) {
	if b.phase == "" || !force && time.Since(b.lastDraw) < 50*time.Millisecond {
		return
	}
	b.lastDraw = time.Now()
	line := b.line()
	n := utf8.RuneCountInString(line)
	pad := ""
	if b.shown > n {
		pad = strings.Repeat(" ", b.shown-n) + strings.Repeat("\b", b.shown-n)
	}
	fmt.Fprintf(b.w, "\r%s%s", line, pad)
	b.shown = n
}

// line renders "Downloading [#######.......] 12/30  orm@0.2.1" or
// "Resolving | 14  passport/oauth2", cut to the width.
func (b *Bar) line() string {
	var s string
	if b.total > 0 {
		const cells = 24
		filled := min(cells, b.done*cells/b.total)
		s = fmt.Sprintf("%-12s [%s%s] %d/%d", b.phase, strings.Repeat("#", filled), strings.Repeat(".", cells-filled), b.done, b.total)
	} else {
		s = fmt.Sprintf("%-12s %c", b.phase, `|/-\`[b.frame%4])
		if b.done > 0 {
			s += fmt.Sprintf(" %d", b.done)
		}
	}
	if b.item != "" {
		s += "  " + b.item
	}
	if r := []rune(s); len(r) > b.width {
		s = string(r[:b.width-1]) + "…"
	}
	return s
}
