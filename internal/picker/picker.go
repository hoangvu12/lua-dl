// Package picker is a tiny raw-mode terminal multi-select.
//
// No heavy TUI framework — just x/term for raw mode plus ANSI escapes to
// redraw in place. Arrow keys / j,k to move, space to toggle, a/n for
// all/none, enter to confirm, q or ctrl-c to cancel. Locked items (e.g. the
// base depot) can't be toggled off.
package picker

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

type Item struct {
	Label    string // primary line text
	Hint     string // greyed trailing hint, e.g. "1.2 GB"
	Tag      string // bracketed tag before label, e.g. "base", "DLC"
	Selected bool
	Locked   bool // cannot be toggled off (still drawn checked)
}

var ErrCancelled = errors.New("picker: cancelled")

// Run draws the picker on stderr and blocks until the user confirms or
// cancels. Returns the updated items slice (Selected fields updated).
// If stderr is not a terminal it returns an error — the caller should have
// already guarded with term.IsTerminal.
func Run(title string, items []Item) ([]Item, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, errors.New("picker: stdin is not a terminal")
	}
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, fmt.Errorf("picker: make raw: %w", err)
	}
	defer term.Restore(fd, oldState)

	// Hide cursor for the duration; restore on exit.
	fmt.Fprint(os.Stderr, "\x1b[?25l")
	defer fmt.Fprint(os.Stderr, "\x1b[?25h")

	cursor := 0
	for i, it := range items {
		if !it.Locked && it.Selected {
			cursor = i
			break
		}
	}

	firstDraw := true
	lastLines := 0
	buf := make([]byte, 8)
	for {
		out := render(title, items, cursor)
		// Visual rows == number of "\n" in the output (render ends without
		// a trailing newline, so final line doesn't count against the move).
		lines := strings.Count(out, "\n") + 1
		if !firstDraw && lastLines > 1 {
			// Move cursor back to the top-left of the previous frame, then
			// clear from there to end of screen. Much simpler than per-line
			// clearing and it gets the row math right on the first try.
			fmt.Fprintf(os.Stderr, "\r\x1b[%dA\x1b[J", lastLines-1)
		} else if !firstDraw {
			fmt.Fprint(os.Stderr, "\r\x1b[J")
		}
		fmt.Fprint(os.Stderr, out)
		firstDraw = false
		lastLines = lines

		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			return nil, ErrCancelled
		}
		key := buf[:n]
		// Escape sequences for arrow keys: ESC [ A/B/C/D
		if n >= 3 && key[0] == 0x1b && key[1] == '[' {
			switch key[2] {
			case 'A': // up
				cursor = moveCursor(items, cursor, -1)
				continue
			case 'B': // down
				cursor = moveCursor(items, cursor, +1)
				continue
			}
		}
		switch key[0] {
		case 'k':
			cursor = moveCursor(items, cursor, -1)
		case 'j':
			cursor = moveCursor(items, cursor, +1)
		case ' ':
			if !items[cursor].Locked {
				items[cursor].Selected = !items[cursor].Selected
			}
		case 'a':
			for i := range items {
				items[i].Selected = true
			}
		case 'n':
			for i := range items {
				if !items[i].Locked {
					items[i].Selected = false
				}
			}
		case '\r', '\n':
			// Clear the picker frame so subsequent output starts clean.
			clearFrame(lastLines)
			return items, nil
		case 'q', 0x03 /* ctrl-c */, 0x1b /* bare ESC */ :
			clearFrame(lastLines)
			return nil, ErrCancelled
		}
	}
}

func moveCursor(items []Item, cur, delta int) int {
	cur += delta
	if cur < 0 {
		cur = 0
	}
	if cur >= len(items) {
		cur = len(items) - 1
	}
	return cur
}

func clearFrame(lines int) {
	if lines <= 0 {
		return
	}
	if lines > 1 {
		fmt.Fprintf(os.Stderr, "\r\x1b[%dA\x1b[J", lines-1)
	} else {
		fmt.Fprint(os.Stderr, "\r\x1b[J")
	}
}

func render(title string, items []Item, cursor int) string {
	var b strings.Builder
	b.WriteString(title)
	b.WriteString("\r\n")
	b.WriteString("  \x1b[2m↑/↓ move · space toggle · a all · n none · enter confirm · q cancel\x1b[0m\r\n")
	b.WriteString("\r\n")

	var selCount int
	for _, it := range items {
		if it.Selected {
			selCount++
		}
	}

	for i, it := range items {
		mark := " "
		if i == cursor {
			mark = "\x1b[36m>\x1b[0m"
		}
		box := "[ ]"
		if it.Selected {
			box = "[\x1b[32mx\x1b[0m]"
		}
		tag := ""
		if it.Tag != "" {
			tag = fmt.Sprintf("\x1b[33m%s\x1b[0m ", it.Tag)
		}
		hint := ""
		if it.Hint != "" {
			hint = fmt.Sprintf("  \x1b[2m%s\x1b[0m", it.Hint)
		}
		lock := ""
		if it.Locked {
			lock = " \x1b[2m(required)\x1b[0m"
		}
		fmt.Fprintf(&b, " %s %s %s%s%s%s\r\n", mark, box, tag, it.Label, hint, lock)
	}
	fmt.Fprintf(&b, "\r\n  \x1b[2m%d of %d selected\x1b[0m", selCount, len(items))
	return b.String()
}
