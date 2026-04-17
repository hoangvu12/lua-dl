//go:build windows

package goldberg

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"unicode/utf16"
)

// isDefenderError returns true when Windows Defender quarantined a file
// (ERROR_VIRUS_INFECTED = 0xE1 = 225).
//
// Two checks: syscall.Errno unwrapping (works for direct os.* calls) plus
// a string fallback for libs like bodgit/sevenzip that don't propagate %w.
func isDefenderError(err error) bool {
	if err == nil {
		return false
	}
	var errno syscall.Errno
	if errors.As(err, &errno) && errno == 225 {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "virus") || strings.Contains(msg, "unwanted software")
}

// addDefenderExclusion pops a UAC dialog asking for admin permission, then
// adds path to Windows Defender's exclusion list. Blocks until the user
// accepts or denies.
func addDefenderExclusion(path string) error {
	safe := strings.ReplaceAll(path, "'", "''")
	script := fmt.Sprintf("Add-MpPreference -ExclusionPath '%s'", safe)

	// PowerShell -EncodedCommand expects UTF-16LE base64 — avoids all escaping.
	runes := utf16.Encode([]rune(script))
	buf := make([]byte, len(runes)*2)
	for i, r := range runes {
		buf[i*2] = byte(r)
		buf[i*2+1] = byte(r >> 8)
	}
	encoded := base64.StdEncoding.EncodeToString(buf)

	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command",
		fmt.Sprintf(
			`Start-Process powershell.exe -Verb RunAs -Wait -ArgumentList "-NoProfile -EncodedCommand %s"`,
			encoded,
		),
	)
	return cmd.Run()
}
