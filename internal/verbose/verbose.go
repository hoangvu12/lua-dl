package verbose

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

var enabled bool

func Set(v bool) { enabled = v }

func Enabled() bool { return enabled }

// Vlog writes to stderr only when verbose mode is on.
func Vlog(format string, args ...any) {
	if enabled {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	}
}

// Errf writes to stderr unconditionally (used for user-facing progress).
func Errf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// StatusLine prints a progress line. In verbose mode it's a normal append.
// On a TTY in non-verbose mode it rewrites the current line in place.
// Off-TTY it appends with newline.
func StatusLine(msg string) {
	if enabled {
		fmt.Fprintln(os.Stderr, msg)
		return
	}
	if term.IsTerminal(int(os.Stderr.Fd())) {
		fmt.Fprintf(os.Stderr, "\r\x1b[2K%s", msg)
	} else {
		fmt.Fprintln(os.Stderr, msg)
	}
}

// StatusDone finalizes an in-place status line with a newline.
func StatusDone() {
	if !enabled && term.IsTerminal(int(os.Stderr.Fd())) {
		fmt.Fprintln(os.Stderr)
	}
}
