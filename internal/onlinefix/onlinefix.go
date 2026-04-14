// Package onlinefix scrapes online-fix.me for community multiplayer fixes
// and applies them over a freshly-downloaded Steam depot.
//
// Data path (plain HTTP, baked subscriber session cookie):
//
//  1. /index.php?do=search&story=<name>           → game page URLs
//  2. /games/<genre>/<id>-<slug>.html             → uploads folder slug
//  3. uploads.online-fix.me:2053/uploads/<slug>/  → seed online_fix_auth cookie
//  4.   .../Fix Repair/                           → list the .rar(s)
//  5. GET each .rar, extract, copy into gameDir
//
// Every request to uploads.online-fix.me needs a Referer pointing at the
// game page — the origin enforces it via nginx rule. Files outside
// `Fix Repair/` (i.e. the full-game repack) return 403 even with the right
// cookies, which is exactly what we want: we only ever pull the ~10 MB patch.
package onlinefix

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strings"

	"golang.org/x/term"
	"golang.org/x/text/encoding/charmap"

	"github.com/hoangvu12/lua-dl/internal/picker"
)

// Baked subscriber session. Rotate both consts when errors start flashing
// "session cookies may have expired".
const (
	sessionCookies = "SITE_TOTAL_ID=6855e127e2eb867851ee3c2b764f0137; dle_user_id=4600624; dle_password=0465b7c359b0776c82bf6b9a3e02387a; PHPSESSID=psjpb0qj2tfc3kjsj64hdb1n62; cf_clearance=hryVqoCYBF29gbQ6an6Wu6_9dgS3f3Y1vx.E3aucDBE-1776138584-1.2.1.1-4.TwpNjkPm.1v1ye06wAit3vEQquBZ7Yde5lxh6uJ2uKogaJlQxsYyZSdAy1bKfXskm5gjK70i2o0LSpC.ilmaWYT7te4j41IJ4.k_qSoznTyPo9jmqUcQsCFZL8MwohytihnZqeTa5nJ56pWM6G3V8ZpLPyVwu5G_AusASDKXEJdKK3mtQHEJW6SWshuRzjzbK16fFPOP932vdWylM7Osto3GHVGSyNYsBA2WJinR1vnCW4U4nKunI65CKk.JjZmsElDxKTiH.DnK9VnqfqSiF3CE7zUD3Dc9oXjfaXF0LjFV9mrRzamI9NowPV9URMSTSW51MHkf08PV4PPVSwDw"
	userAgent      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36"

	siteURL     = "https://online-fix.me"
	uploadsBase = "https://uploads.online-fix.me:2053"
	rarPassword = "online-fix.me"
)

type result struct {
	Title   string
	PageURL string
}

// Offer runs the post-download flow: search online-fix.me for the game,
// prompt Y/n on any hit, let the user disambiguate when there's more than
// one, then download+extract+apply. Every error is soft — we only print
// them, never bubble them — because a broken fix lookup must not taint the
// main depot download.
func Offer(ctx context.Context, gameName, gameDir string) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil
	}
	client := &http.Client{Jar: jar}

	results, err := search(ctx, client, gameName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n[online-fix] search failed: %v\n", err)
		return nil
	}
	if len(results) == 0 {
		return nil
	}

	fmt.Fprintf(os.Stderr, "\n[online-fix] multiplayer fix available for this game.\n")
	if !askYesNo("Install Online-Fix now? [Y/n]: ") {
		return nil
	}

	chosen, ok := pick(gameName, results)
	if !ok {
		return nil
	}
	if err := apply(ctx, client, chosen.PageURL, gameDir); err != nil {
		fmt.Fprintf(os.Stderr, "\n[online-fix] failed: %v\n", err)
	}
	return nil
}

func pick(gameName string, results []result) (result, bool) {
	if len(results) == 1 {
		return results[0], true
	}
	items := make([]picker.Item, len(results))
	for i, r := range results {
		items[i] = picker.Item{Label: r.Title, Selected: i == 0}
	}
	picked, err := picker.Run(fmt.Sprintf("\r\nMultiple fixes matched %q. Pick one:", gameName), items)
	if err != nil {
		return result{}, false
	}
	for i, it := range picked {
		if it.Selected {
			return results[i], true
		}
	}
	return result{}, false
}

func askYesNo(prompt string) bool {
	fmt.Fprint(os.Stderr, prompt)
	s := bufio.NewScanner(os.Stdin)
	if !s.Scan() {
		return false
	}
	ans := strings.ToLower(strings.TrimSpace(s.Text()))
	return ans == "" || ans == "y" || ans == "yes"
}

// get forwards the baked cookies+UA and a Referer (uploads.online-fix.me
// rejects requests without one; the initial search is the only call that
// can pass an empty referer).
func get(ctx context.Context, client *http.Client, url, referer string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Cookie", sessionCookies)
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	return client.Do(req)
}

// fetchString GETs a url and returns its body decoded from windows-1251
// (the site's encoding). Used for HTML pages only — binary downloads use
// get+io.Copy directly.
func fetchString(ctx context.Context, client *http.Client, pageURL, referer string) (string, error) {
	res, err := get(ctx, client, pageURL, referer)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("GET %s: HTTP %d (session cookies may have expired)", pageURL, res.StatusCode)
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	return decodeRussian(raw), nil
}

func decodeRussian(b []byte) string {
	out, err := io.ReadAll(charmap.Windows1251.NewDecoder().Reader(bytes.NewReader(b)))
	if err != nil {
		return string(b)
	}
	return string(out)
}

// searchLinkRE matches search-result cards via their `<h2 class="title">`
// heading wrapped in a link anchor. The `big-link` overlay anchor is empty,
// and other card links (image thumbnail, comments) point at the same URL
// but don't carry the title; using the h2 wrapper gives us URL + title in
// one shot and rejects the site's live-chat widget (which also contains
// game-page hrefs but never wraps them in h2.title).
var searchLinkRE = regexp.MustCompile(
	`(?s)<a[^>]+href="(https?://online-fix\.me/games/[^"]+?\.html)"[^>]*>\s*<h2[^>]*class="title"[^>]*>\s*([^<]+?)\s*</h2>`,
)

func search(ctx context.Context, client *http.Client, name string) ([]result, error) {
	u := siteURL + "/index.php?do=search&subaction=search&story=" + url.QueryEscape(name)
	html, err := fetchString(ctx, client, u, siteURL+"/")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []result
	for _, m := range searchLinkRE.FindAllStringSubmatch(html, -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, result{Title: strings.TrimSpace(m[2]), PageURL: m[1]})
		if len(out) >= 10 {
			break
		}
	}
	return out, nil
}
