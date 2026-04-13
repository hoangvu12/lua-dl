package sanitize

import (
	"regexp"
	"strings"
)

var (
	forbidden = regexp.MustCompile(`[<>:"|?*\\/\x00-\x1f]`)
	spaces    = regexp.MustCompile(`\s+`)
	trailing  = regexp.MustCompile(`[. ]+$`)
	reserved  = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[1-9]|lpt[1-9])(\..*)?$`)
)

// FolderName sanitizes a string for use as a Windows folder name.
// Windows forbids: < > : " | ? * \ / and control chars (0x00-0x1f).
// Also silently strips trailing dots and spaces, and reserves names like
// CON, PRN, AUX, NUL, COM1-9, LPT1-9.
func FolderName(name string) string {
	s := forbidden.ReplaceAllString(name, "-")
	s = spaces.ReplaceAllString(s, " ")
	s = trailing.ReplaceAllString(s, "")
	s = strings.TrimSpace(s)
	if s == "" {
		s = "game"
	}
	if reserved.MatchString(s) {
		s = "_" + s
	}
	if len(s) > 120 {
		s = strings.TrimRight(s[:120], " ")
	}
	return s
}
