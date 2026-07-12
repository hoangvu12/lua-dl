// Package onlinefix scrapes online-fix.me for community multiplayer fixes
// and applies them over a freshly-downloaded Steam depot.
//
// Data path (plain HTTP, baked subscriber session cookie):
//
//  1. /index.php?do=search&story=<name>           → game page URLs
//  2. /games/<genre>/<id>-<slug>.html             → uploads folder slug
//  3. uploads.online-fix.me:2053/uploads/<slug>/  → seed online_fix_auth cookie
//  4. .../Fix Repair/                           → list the .rar(s)
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
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/term"
	"golang.org/x/text/encoding/charmap"

	"github.com/hoangvu12/lua-dl/internal/picker"
	"github.com/hoangvu12/lua-dl/internal/ui"
)

const (
	loginUsername = "luadl"
	loginPassword = "NwbVPVj6pQmY4Z9D"
	userAgent     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36"

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
//
// Returns true if the fix was successfully applied (Goldberg is already
// included in online-fix patches, so the caller should skip a standalone
// Goldberg install when this returns true).
func Offer(ctx context.Context, gameName, gameDir string) (bool, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, nil
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return false, nil
	}
	client := &http.Client{Jar: jar}
	if err := login(ctx, client); err != nil {
		ui.Phase("Online-Fix")
		ui.LastStep(fmt.Sprintf("login failed: %v", err))
		return false, nil
	}

	results, err := search(ctx, client, gameName)
	if err != nil {
		ui.Phase("Online-Fix")
		ui.LastStep(fmt.Sprintf("search failed: %v", err))
		return false, nil
	}
	if len(results) == 0 {
		return false, nil
	}

	ui.Phase("Online-Fix · multiplayer fix available")
	preview, unambiguous := unambiguousMatch(gameName, results)
	var prompt string
	if unambiguous {
		prompt = fmt.Sprintf("  Install fix for %q? [Y/n]: ", preview.Title)
	} else {
		prompt = fmt.Sprintf("  Found %d candidates for %q. Pick one? [Y/n]: ", len(results), gameName)
	}
	if !askYesNo(prompt) {
		ui.Note("skipped")
		return false, nil
	}

	var chosen result
	if unambiguous {
		chosen = preview
	} else {
		c, ok := pick(gameName, results)
		if !ok {
			ui.Note("cancelled")
			return false, nil
		}
		chosen = c
	}
	if err := apply(ctx, client, chosen.PageURL, gameDir); err != nil {
		ui.LastStep(fmt.Sprintf("failed: %v", err))
		return false, nil
	}
	return true, nil
}

// unambiguousMatch returns the single result we're confident we'd install
// without prompting the user to pick — either because there's only one hit
// or because one hit's normalized title matches the requested game exactly.
// Callers use the returned title in the install confirmation so the user
// sees what they're about to apply.
func unambiguousMatch(gameName string, results []result) (result, bool) {
	if len(results) == 1 {
		return results[0], true
	}
	if exact, ok := exactMatch(gameName, results); ok {
		return exact, true
	}
	return result{}, false
}

func pick(gameName string, results []result) (result, bool) {
	items := make([]picker.Item, len(results))
	for i, r := range results {
		items[i] = picker.Item{Label: r.Title, Selected: i == 0}
	}
	picked, err := picker.RunSingle(fmt.Sprintf("\r\nMultiple fixes matched %q. Pick one:", gameName), items)
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

func exactMatch(gameName string, results []result) (result, bool) {
	want := normalizeTitle(gameName)
	for _, r := range results {
		if normalizeTitle(r.Title) == want {
			return r, true
		}
	}
	return result{}, false
}

func normalizeTitle(title string) string {
	title = strings.ToLower(strings.TrimSpace(title))
	for _, suffix := range []string{" по сети", " po seti"} {
		title = strings.TrimSpace(strings.TrimSuffix(title, suffix))
	}
	return title
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

func login(ctx context.Context, client *http.Client) error {
	token, err := authToken(ctx, client)
	if err != nil {
		return err
	}
	form := url.Values{
		"login_name":     {loginUsername},
		"login_password": {loginPassword},
		"login":          {"submit"},
		token.name:       {token.value},
	}
	req, err := http.NewRequestWithContext(ctx, "POST", siteURL+"/", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	setBrowserHeaders(req, siteURL+"/")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("POST %s/: HTTP %d", siteURL, res.StatusCode)
	}
	if !hasCookie(client, "dle_user_id") || !hasCookie(client, "dle_password") {
		return fmt.Errorf("credentials were not accepted")
	}
	return nil
}

func hasCookie(client *http.Client, name string) bool {
	if client.Jar == nil {
		return false
	}
	u, err := url.Parse(siteURL + "/")
	if err != nil {
		return false
	}
	for _, c := range client.Jar.Cookies(u) {
		if c.Name == name && c.Value != "" {
			return true
		}
	}
	return false
}

type loginToken struct {
	name  string
	value string
}

func authToken(ctx context.Context, client *http.Client) (loginToken, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", siteURL+"/engine/ajax/authtoken.php", nil)
	if err != nil {
		return loginToken{}, err
	}
	setBrowserHeaders(req, siteURL+"/")
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	res, err := client.Do(req)
	if err != nil {
		return loginToken{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return loginToken{}, fmt.Errorf("GET auth token: HTTP %d", res.StatusCode)
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return loginToken{}, err
	}
	body := strings.TrimSpace(string(raw))
	name, value := parseAuthToken(body)
	if name == "" || value == "" {
		return loginToken{}, fmt.Errorf("auth token response did not contain a login token")
	}
	return loginToken{name: name, value: value}, nil
}

func parseAuthToken(body string) (string, string) {
	var payload struct {
		Field string `json:"field"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err == nil && payload.Field != "" && payload.Value != "" {
		return payload.Field, html.UnescapeString(payload.Value)
	}
	if m := tokenJSONRE.FindStringSubmatch(body); m != nil {
		return m[1], unescapeTokenValue(m[2])
	}
	name := tokenNameRE.FindString(body)
	value := ""
	if m := tokenValueRE.FindStringSubmatch(body); m != nil {
		for _, group := range m[1:] {
			if group != "" {
				value = group
				break
			}
		}
	}
	return name, html.UnescapeString(value)
}

func unescapeTokenValue(value string) string {
	unquoted, err := strconv.Unquote(`"` + strings.ReplaceAll(value, `"`, `\"`) + `"`)
	if err != nil {
		unquoted = value
	}
	return html.UnescapeString(unquoted)
}

var (
	tokenJSONRE  = regexp.MustCompile(`"(token_[0-9a-f]+)"\s*:\s*"([^"]+)"`)
	tokenNameRE  = regexp.MustCompile(`token_[0-9a-f]+`)
	tokenValueRE = regexp.MustCompile(`(?i)"(?:value|token)"\s*:\s*"([^"]+)"|value=['"]([^'"]+)`)
)

func setBrowserHeaders(req *http.Request, referer string) {
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
}

// get forwards the UA and a Referer (uploads.online-fix.me rejects requests
// without one; the initial search is the only call that can pass an empty
// referer). Authentication cookies are carried by the client's cookie jar.
func get(ctx context.Context, client *http.Client, url, referer string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	setBrowserHeaders(req, referer)
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
		if strings.Contains(pageURL, "uploads.online-fix.me") && (res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden) {
			return "", fmt.Errorf("online-fix upload is unavailable or closed for this game (HTTP %d)", res.StatusCode)
		}
		return "", fmt.Errorf("GET %s: HTTP %d", pageURL, res.StatusCode)
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
