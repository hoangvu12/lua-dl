package onlinefix

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nwaples/rardecode/v2"
)

// apply runs the download+extract pipeline for a picked game: extract its
// upload-folder slug, warm the cookie jar on the folder listing, list the
// Fix Repair subdirectory, download each .rar in it, and extract them over
// gameDir using the rar-v5 password "online-fix.me". The Fix Repair archives
// use an in-place layout so a simple extract-over-root is all we need.
func apply(ctx context.Context, client *http.Client, pageURL, gameDir string) error {
	slug, err := extractUploadSlug(ctx, client, pageURL)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[online-fix] upload folder: %s\n", slug)

	// The uploads origin sets an `online_fix_auth` cookie on the first
	// contact with the game folder listing; we need that cookie on the
	// follow-up Fix Repair listing and file GETs, so warm the jar here.
	folderURL := uploadsBase + "/uploads/" + slug + "/"
	if _, err := fetchString(ctx, client, folderURL, pageURL); err != nil {
		return err
	}

	fixListURL := folderURL + "Fix%20Repair/"
	rars, err := listRARs(ctx, client, fixListURL, pageURL)
	if err != nil {
		return err
	}
	if len(rars) == 0 {
		return errors.New("Fix Repair folder is empty for this game")
	}

	tmpRoot, err := os.MkdirTemp("", "lua-dl-fix-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpRoot)

	for _, name := range rars {
		fmt.Fprintf(os.Stderr, "[online-fix] downloading %s\n", name)
		rarURL := fixListURL + strings.ReplaceAll(name, " ", "%20")
		if err := downloadRAR(ctx, client, rarURL, pageURL, filepath.Join(tmpRoot, name)); err != nil {
			return fmt.Errorf("download %s: %w", name, err)
		}
	}

	primary := filepath.Join(tmpRoot, primaryRAR(rars))
	fmt.Fprintf(os.Stderr, "[online-fix] extracting %s\n", filepath.Base(primary))
	n, err := extractOver(primary, gameDir)
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	fmt.Fprintf(os.Stderr, "[online-fix] applied %d file(s) to %s\n", n, gameDir)
	return nil
}

var uploadSlugRE = regexp.MustCompile(`uploads\.online-fix\.me:2053/uploads/([^/"]+)/`)

func extractUploadSlug(ctx context.Context, client *http.Client, pageURL string) (string, error) {
	html, err := fetchString(ctx, client, pageURL, siteURL+"/")
	if err != nil {
		return "", err
	}
	m := uploadSlugRE.FindStringSubmatch(html)
	if m == nil {
		return "", errors.New("no uploads.online-fix.me link on game page")
	}
	return m[1], nil
}

// rarHrefRE picks .rar entries out of nginx autoindex HTML. Each file shows
// up as `<a href="name">name</a>  date  size` on its own line.
var rarHrefRE = regexp.MustCompile(`<a href="([^"]+\.rar)">`)

func listRARs(ctx context.Context, client *http.Client, listURL, referer string) ([]string, error) {
	body, err := fetchString(ctx, client, listURL, referer)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range rarHrefRE.FindAllStringSubmatch(body, -1) {
		out = append(out, strings.ReplaceAll(m[1], "%20", " "))
	}
	return out, nil
}

func downloadRAR(ctx context.Context, client *http.Client, url, referer, dst string) error {
	res, err := get(ctx, client, url, referer)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("GET %s: HTTP %d", url, res.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, res.Body)
	return err
}

// primaryRAR picks which .rar in the download dir is the entry point for
// rardecode — for single-volume archives there's only one; for multi-volume
// the first is `.part1.rar` or `.part01.rar` and rardecode auto-follows the
// siblings once it's given the first.
func primaryRAR(names []string) string {
	for _, n := range names {
		ln := strings.ToLower(n)
		if strings.Contains(ln, ".part1.") || strings.Contains(ln, ".part01.") {
			return n
		}
	}
	return names[0]
}

func extractOver(rarPath, gameDir string) (int, error) {
	r, err := rardecode.OpenReader(rarPath, rardecode.Password(rarPassword))
	if err != nil {
		return 0, err
	}
	defer r.Close()
	var n int
	for {
		hdr, err := r.Next()
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		dst := filepath.Join(gameDir, filepath.FromSlash(hdr.Name))
		if hdr.IsDir {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return n, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return n, err
		}
		f, err := os.Create(dst)
		if err != nil {
			return n, err
		}
		if _, err := io.Copy(f, r); err != nil {
			f.Close()
			return n, err
		}
		if err := f.Close(); err != nil {
			return n, err
		}
		n++
	}
}
