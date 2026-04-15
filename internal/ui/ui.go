// Package ui has small output helpers for lua-dl's phase-style CLI.
//
// The visual model is a tree of phases:
//
//	▸ Phase title — summary
//	  ├─ first step                  0.3s
//	  ├─ second step    bar          4.2s
//	  └─ last step                   1.1s
//
//	✓ Done in 5.6s · /path/to/output
//
// Helpers write to stderr (same as the rest of lua-dl's user-facing output)
// so stdout stays clean for piping. ANSI escapes are emitted unconditionally
// — modern Windows terminals (cmd.exe since 1607, Windows Terminal, PowerShell)
// all parse VT100. Output is rendered fine without colors when ANSI is off.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/term"

	"github.com/hoangvu12/lua-dl/internal/verbose"
)

// Phase prints the heading of a new section.
func Phase(title string) {
	fmt.Fprintf(os.Stderr, "\n\x1b[1m▸ %s\x1b[0m\n", title)
}

// Step prints a non-final step under the current phase.
func Step(line string)     { fmt.Fprintf(os.Stderr, "  ├─ %s\n", line) }

// LastStep prints the final step under the current phase.
func LastStep(line string) { fmt.Fprintf(os.Stderr, "  └─ %s\n", line) }

// Done prints the final summary line for a successful run.
func Done(message string) {
	fmt.Fprintf(os.Stderr, "\n\x1b[32m✓\x1b[0m %s\n", message)
}

// Note prints a dim hint line, used for soft warnings like
// "no online-fix available — skipping".
func Note(message string) {
	fmt.Fprintf(os.Stderr, "  \x1b[2m%s\x1b[0m\n", message)
}

// Plural picks the right English noun form. Use for "1 file" / "3 files".
func Plural(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

// FormatBytes is the Steam-store style "1.2 GB" formatter (SI, 1000-base).
func FormatBytes(b uint64) string {
	if b == 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	n := float64(b)
	i := 0
	for n >= 1000 && i < len(units)-1 {
		n /= 1000
		i++
	}
	digits := 2
	switch {
	case n >= 100:
		digits = 0
	case n >= 10:
		digits = 1
	}
	return fmt.Sprintf("%.*f %s", digits, n, units[i])
}

// FormatDuration prints a compact duration: "0.4s", "5.8s", "1m12s".
func FormatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	m := int(d / time.Minute)
	s := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%dm%02ds", m, s)
}

// ProgressReader wraps an io.Reader and draws an inline bar via verbose.StatusLine.
// `total` is the expected size in bytes; `label` is rendered before the bar
// (e.g. "Downloading fix.rar"). Call Done() (or just let it go out of scope —
// the caller is expected to call Done so the bar is finalized with a newline).
type ProgressReader struct {
	r        io.Reader
	total    uint64
	read     atomic.Uint64
	label    string
	start    time.Time
	lastDraw time.Time
}

func NewProgressReader(r io.Reader, total uint64, label string) *ProgressReader {
	return &ProgressReader{r: r, total: total, label: label, start: time.Now()}
}

func (p *ProgressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.read.Add(uint64(n))
	}
	now := time.Now()
	if now.Sub(p.lastDraw) >= 150*time.Millisecond {
		p.lastDraw = now
		p.draw(false)
	}
	return n, err
}

// Done draws a final 100% line and terminates it with a newline.
func (p *ProgressReader) Done() {
	p.draw(true)
	if !verbose.Enabled() && term.IsTerminal(int(os.Stderr.Fd())) {
		fmt.Fprintln(os.Stderr)
	}
}

func (p *ProgressReader) draw(final bool) {
	read := p.read.Load()
	pct := 0.0
	if p.total > 0 {
		pct = float64(read) / float64(p.total) * 100
		if pct > 100 {
			pct = 100
		}
	}
	if final {
		pct = 100
	}
	const barW = 20
	filled := int(pct / 100 * barW)
	if filled > barW {
		filled = barW
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barW-filled)

	elapsed := time.Since(p.start).Seconds()
	mbps := 0.0
	if elapsed > 0 {
		mbps = float64(read) / elapsed / 1e6
	}
	line := fmt.Sprintf("  └─ %s  %s  %s/%s  %.1f MB/s",
		p.label, bar, FormatBytes(read), FormatBytes(p.total), mbps)
	verbose.StatusLine(line)
}
