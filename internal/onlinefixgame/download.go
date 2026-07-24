package onlinefixgame

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hoangvu12/lua-dl/internal/ui"
)

// downloadParts fetches every volume sequentially into destDir. Each part tries
// its mirrors in priority order, falling through to the next on failure. A
// GoFile guest token is created lazily and shared across parts.
func downloadParts(ctx context.Context, client *http.Client, parts []gamePart, destDir string) error {
	var gofileToken string
	for i := range parts {
		p := parts[i]
		finalPath := filepath.Join(destDir, p.fileName)

		// A final file only ever exists after a verified download, so its
		// presence means this part is already complete.
		if fileExists(finalPath) && verifyPartFile(finalPath, p) == nil {
			ui.Step(fmt.Sprintf("%s · already downloaded", p.fileName))
			continue
		}

		var errs []string
		done := false
		for mi, m := range p.mirrors {
			// Without a hash we cannot trust bytes from a different mirror, so
			// drop any partial before switching sources.
			if mi > 0 && p.sha256 == "" {
				_ = os.Remove(finalPath + ".part")
			}
			if m.provider == providerGofile && gofileToken == "" {
				token, err := createGofileGuestToken(ctx, client)
				if err != nil {
					errs = append(errs, "gofile: "+err.Error())
					continue
				}
				gofileToken = token
			}
			if err := downloadPart(ctx, client, p, m, gofileToken, destDir); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", m.provider, err))
				continue
			}
			done = true
			break
		}
		if !done {
			return fmt.Errorf("could not download %s: %s", p.fileName, strings.Join(errs, "; "))
		}
	}
	return nil
}

func downloadPart(ctx context.Context, client *http.Client, p gamePart, m mirror, gofileToken, destDir string) error {
	finalPath := filepath.Join(destDir, p.fileName)
	partialPath := finalPath + ".part"

	var existing int64
	if fi, err := os.Stat(partialPath); err == nil {
		existing = fi.Size()
	}

	downloadURL, err := resolveDownloadURL(ctx, client, m, gofileToken)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		return err
	}
	if err := configureDownloadRequest(req, m, gofileToken); err != nil {
		return err
	}
	if existing > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existing))
	}

	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	switch res.StatusCode {
	case http.StatusRequestedRangeNotSatisfiable:
		// The whole file is likely already on disk; finalize if it checks out.
		if p.size != sizeUnknown && existing == p.size {
			if err := verifyPartFile(partialPath, p); err != nil {
				_ = os.Remove(partialPath)
				return err
			}
			return finalize(partialPath, finalPath)
		}
		return fmt.Errorf("server rejected the resume range")
	case http.StatusOK:
		existing = 0 // full body, not a range: restart from scratch
	case http.StatusPartialContent:
		// resuming — good
	default:
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}

	if ct := strings.ToLower(strings.TrimSpace(res.Header.Get("Content-Type"))); strings.HasPrefix(ct, "text/html") {
		return fmt.Errorf("server returned a web page, not the file")
	}

	total := p.size
	if total == sizeUnknown {
		if cl := res.Header.Get("Content-Length"); cl != "" {
			if n, err := strconv.ParseInt(cl, 10, 64); err == nil {
				if res.StatusCode == http.StatusPartialContent {
					total = existing + n
				} else {
					total = n
				}
			}
		}
	}

	flag := os.O_CREATE | os.O_WRONLY
	if existing > 0 && res.StatusCode == http.StatusPartialContent {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
		existing = 0
	}
	f, err := os.OpenFile(partialPath, flag, 0o644)
	if err != nil {
		return err
	}

	var barTotal uint64
	if total != sizeUnknown {
		barTotal = uint64(total)
	}
	pr := ui.NewProgressReader(res.Body, barTotal, "downloading "+p.fileName)
	_, copyErr := io.Copy(f, pr)
	pr.Done()
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}

	if err := verifyPartFile(partialPath, p); err != nil {
		_ = os.Remove(partialPath)
		return err
	}
	return finalize(partialPath, finalPath)
}

func finalize(partialPath, finalPath string) error {
	if fileExists(finalPath) {
		if err := os.Remove(finalPath); err != nil {
			return fmt.Errorf("replace completed file: %w", err)
		}
	}
	if err := os.Rename(partialPath, finalPath); err != nil {
		return fmt.Errorf("finalize download: %w", err)
	}
	return nil
}

// verifyPartFile checks a downloaded file against the known size and sha256.
// When neither is known it can only confirm the file exists.
func verifyPartFile(path string, p gamePart) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if p.size != sizeUnknown && fi.Size() != p.size {
		return fmt.Errorf("download ended at %d of %d bytes", fi.Size(), p.size)
	}
	if p.sha256 != "" {
		sum, err := sha256File(path)
		if err != nil {
			return err
		}
		if !strings.EqualFold(sum, p.sha256) {
			return fmt.Errorf("sha256 mismatch")
		}
	}
	return nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
