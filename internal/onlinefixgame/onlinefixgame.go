// Package onlinefixgame downloads a full cracked game from online-fix.me's
// authenticated Hosters page and extracts it in place.
//
// Data path (reuses the online-fix.me session from internal/onlinefix):
//
//  1. <articleId|url>                         → article page HTML
//  2. hosters.online-fix.me:2053/...          → Hosters page (needs session)
//  3. data-links='[...]' provider records     → FileDitch / GoFile / Pixeldrain
//  4. resolve + download each .rar volume     → resumable, mirror fallback
//  5. extract the primary volume over destDir → rardecode auto-follows parts
//
// Unlike the Fix Repair flow (internal/onlinefix.Offer), this pulls the entire
// repack from third-party hosters rather than the ~10 MB patch, and runs
// non-interactively so it can be driven from a generated .bat.
package onlinefixgame

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hoangvu12/lua-dl/internal/defender"
	"github.com/hoangvu12/lua-dl/internal/onlinefix"
	"github.com/hoangvu12/lua-dl/internal/ui"
)

// Download resolves the online-fix article referenced by ref (an article id, a
// full online-fix.me URL, or a site-relative path), downloads the full game,
// and extracts it into outDir. When outDir is empty it defaults to a folder
// named after the game's title under the current working directory.
func Download(ctx context.Context, ref, outDir string) error {
	ui.Phase("Online-Fix · full game")

	client, err := onlinefix.NewSession(ctx)
	if err != nil {
		return fmt.Errorf("online-fix login: %w", err)
	}

	pageURL := articleURL(ref)
	ui.Step("resolving article")
	article, err := onlinefix.FetchHTML(ctx, client, pageURL, onlinefix.SiteURL+"/")
	if err != nil {
		return err
	}

	hostersURL := findHostersURL(article)
	if hostersURL == "" {
		return fmt.Errorf("this article does not expose an Online-Fix Hosters page")
	}

	ui.Step("reading hosters page")
	hostersHTML, err := onlinefix.FetchHTML(ctx, client, hostersURL, pageURL)
	if err != nil {
		return err
	}
	links, err := parseHosterLinks(hostersHTML)
	if err != nil {
		return err
	}

	ui.Step("resolving download links")
	parts, err := resolveHosters(ctx, client, links)
	if err != nil {
		return err
	}

	if strings.TrimSpace(outDir) == "" {
		outDir = defaultOutDir(article)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	absOut, err := filepath.Abs(outDir)
	if err != nil {
		absOut = outDir
	}

	ui.Step(fmt.Sprintf("%d %s · %s → %s",
		len(parts), ui.Plural(len(parts), "part", "parts"), totalSizeLabel(parts), absOut))

	// The archives and the game's own files carry the online-fix loader DLLs,
	// which Windows Defender routinely quarantines. Exclude the target folder
	// up front (best effort — a missing exclusion only matters if files vanish).
	ui.Step("adding Defender exclusion · click Yes in the popup")
	if err := defender.AddExclusion(absOut); err != nil {
		ui.Note("could not add a Defender exclusion automatically — add this folder manually if files disappear:\n     " + absOut)
	}

	if err := downloadParts(ctx, client, parts, absOut); err != nil {
		return err
	}

	names := partNames(parts)
	primary := filepath.Join(absOut, onlinefix.PrimaryRAR(names))
	ui.Step("extracting " + filepath.Base(primary))
	n, err := onlinefix.ExtractOver(primary, absOut)
	if err != nil {
		if defender.IsBlockedError(err) {
			if exclErr := defender.AddExclusion(absOut); exclErr == nil {
				n, err = onlinefix.ExtractOver(primary, absOut)
			}
		}
		if err != nil {
			return fmt.Errorf("extract: %w", err)
		}
	}

	// Remove the downloaded archive volumes now that the game is extracted.
	for _, name := range names {
		_ = os.Remove(filepath.Join(absOut, name))
	}

	ui.Done(fmt.Sprintf("installed %d %s · %s", n, ui.Plural(n, "file", "files"), absOut))
	return nil
}

func partNames(parts []gamePart) []string {
	names := make([]string, len(parts))
	for i, p := range parts {
		names[i] = p.fileName
	}
	return names
}

func totalSizeLabel(parts []gamePart) string {
	var sum uint64
	unknown := false
	for _, p := range parts {
		if p.size == sizeUnknown {
			unknown = true
			continue
		}
		sum += uint64(p.size)
	}
	if sum == 0 && unknown {
		return "size unknown"
	}
	label := ui.FormatBytes(sum)
	if unknown {
		label = "≥ " + label
	}
	return label
}

// articleURL turns a bare article id, a full URL, or a site-relative path into
// an online-fix.me URL. A newsid link redirects to the canonical article page.
func articleURL(ref string) string {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	if isDigitsStr(ref) {
		return onlinefix.SiteURL + "/index.php?newsid=" + ref
	}
	return onlinefix.SiteURL + "/" + strings.TrimPrefix(ref, "/")
}

var (
	h1TitleRE     = regexp.MustCompile(`(?is)<h1[^>]*>(.*?)</h1>`)
	titleTagRE    = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	htmlTagRE     = regexp.MustCompile(`(?s)<[^>]+>`)
	titleSuffixes = []string{" по сети", " po seti"}
)

// defaultOutDir derives a folder name from the article's title. Falls back to a
// generic name when no title can be scraped.
func defaultOutDir(article string) string {
	title := scrapeTitle(article)
	if title == "" {
		return "online-fix-game"
	}
	return safeFileName(title)
}

func scrapeTitle(article string) string {
	raw := ""
	if m := h1TitleRE.FindStringSubmatch(article); m != nil {
		raw = m[1]
	} else if m := titleTagRE.FindStringSubmatch(article); m != nil {
		raw = m[1]
	}
	raw = htmlTagRE.ReplaceAllString(raw, "")
	raw = strings.TrimSpace(raw)
	// Trim site suffixes appended to <title> like "Game » online-fix.me".
	for _, sep := range []string{" » ", " | ", " — "} {
		if i := strings.Index(raw, sep); i > 0 {
			raw = raw[:i]
		}
	}
	lower := strings.ToLower(raw)
	for _, suffix := range titleSuffixes {
		if strings.HasSuffix(lower, suffix) {
			raw = strings.TrimSpace(raw[:len(raw)-len(suffix)])
			break
		}
	}
	return strings.TrimSpace(raw)
}

func isDigitsStr(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
