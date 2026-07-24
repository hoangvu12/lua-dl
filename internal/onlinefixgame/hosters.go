package onlinefixgame

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hoangvu12/lua-dl/internal/onlinefix"
)

// Provider identifiers and priorities. Lower priority wins (tried first).
const (
	providerPixeldrain = "pixeldrain"
	providerFileditch  = "fileditch"
	providerGofile     = "gofile"

	// FileDitch: no per-IP daily cap or documented throttle → primary.
	priorityFileditch = 0
	// GoFile: also uncapped, covers FileDitch outages → secondary.
	priorityGofile = 10
	// Pixeldrain: throttles hard after ~6 GB/day/IP, but is the only provider
	// that supplies a sha256 for real integrity checks → last-resort fallback.
	priorityPixeldrain = 20

	gofileOrigin    = "https://gofile.io"
	gofileAPI       = "https://api.gofile.io"
	gofileLang      = "en-US"
	gofileSalt      = "9844d94d963d30"
	gofileUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"
)

// sizeUnknown marks a part/file whose size could not be resolved up front.
const sizeUnknown int64 = -1

type mirror struct {
	provider     string
	url          string
	priority     int
	referer      string
	gofileFolder string // GoFile root share code, needed to re-walk at download time
}

type resolvedFile struct {
	fileName string
	size     int64 // sizeUnknown if not resolved
	sha256   string
	mirror   mirror
}

// gamePart is one archive volume with every mirror that can serve it,
// ordered by priority.
type gamePart struct {
	fileName string
	size     int64
	sha256   string
	mirrors  []mirror
}

type hosterLink struct {
	DirectLink  string `json:"direct_link"`
	FileName    string `json:"file_name"`
	IsDangerous bool   `json:"is_dangerous"`
}

var hostersURLRE = regexp.MustCompile(`(?i)https://hosters\.online-fix\.me:2053/[^\s"'<>?#]+(?:%20[^\s"'<>?#]+)*`)

func findHostersURL(pageHTML string) string {
	return hostersURLRE.FindString(pageHTML)
}

var dataLinksRE = regexp.MustCompile(`data-links\s*=\s*'([^']+)'`)

// parseHosterLinks pulls every provider record out of the Hosters page's
// html-escaped `data-links='[...]'` attributes.
func parseHosterLinks(pageHTML string) ([]hosterLink, error) {
	var links []hosterLink
	for _, m := range dataLinksRE.FindAllStringSubmatch(pageHTML, -1) {
		raw := html.UnescapeString(m[1])
		var batch []hosterLink
		if err := json.Unmarshal([]byte(raw), &batch); err == nil {
			links = append(links, batch...)
		}
	}
	if len(links) == 0 {
		return nil, fmt.Errorf("Online-Fix Hosters did not return any provider links")
	}
	return links, nil
}

// resolveHosters turns the parsed provider records into merged, ordered parts.
// A GoFile guest token is created lazily and only if a GoFile link appears.
func resolveHosters(ctx context.Context, client *http.Client, links []hosterLink) ([]gamePart, error) {
	var resolved []resolvedFile
	var errs []string
	var gofileToken string

	for _, link := range links {
		if link.IsDangerous || isFixRepair(link.FileName) {
			continue
		}
		u, err := url.Parse(link.DirectLink)
		if err != nil || u.Scheme != "https" {
			continue
		}
		host := strings.ToLower(u.Hostname())
		switch host {
		case "pixeldrain.com", "www.pixeldrain.com":
			f, err := resolvePixeldrain(ctx, client, u, link.FileName)
			if err != nil {
				errs = append(errs, err.Error())
				continue
			}
			resolved = append(resolved, f)
		case "fileditchfiles.st", "fileditch.com", "www.fileditch.com":
			resolved = append(resolved, resolveFileditch(u, link.FileName))
		case "gofile.io", "www.gofile.io":
			if gofileToken == "" {
				token, err := createGofileGuestToken(ctx, client)
				if err != nil {
					errs = append(errs, "GoFile: "+err.Error())
					continue
				}
				gofileToken = token
			}
			files, err := resolveGofile(ctx, client, u, gofileToken)
			if err != nil {
				errs = append(errs, err.Error())
				continue
			}
			resolved = append(resolved, files...)
		}
	}

	parts := mergeFiles(resolved)
	if len(parts) == 0 {
		detail := "no supported safe providers were listed"
		if len(errs) > 0 {
			detail = strings.Join(errs, "; ")
		}
		return nil, fmt.Errorf("could not resolve this full game: %s", detail)
	}
	return parts, nil
}

// --- Pixeldrain -------------------------------------------------------------

type pixeldrainInfo struct {
	Success     bool   `json:"success"`
	Name        string `json:"name"`
	Size        uint64 `json:"size"`
	HashSHA256  string `json:"hash_sha256"`
	CanDownload bool   `json:"can_download"`
}

func resolvePixeldrain(ctx context.Context, client *http.Client, u *url.URL, fallbackName string) (resolvedFile, error) {
	id := pixeldrainID(u)
	if id == "" {
		return resolvedFile{}, fmt.Errorf("Pixeldrain returned an invalid share URL")
	}
	infoURL := "https://pixeldrain.com/api/file/" + id + "/info"
	req, _ := http.NewRequestWithContext(ctx, "GET", infoURL, nil)
	req.Header.Set("User-Agent", onlinefix.UserAgent)
	res, err := client.Do(req)
	if err != nil {
		return resolvedFile{}, fmt.Errorf("Pixeldrain metadata: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return resolvedFile{}, fmt.Errorf("Pixeldrain metadata: HTTP %d", res.StatusCode)
	}
	var info pixeldrainInfo
	if err := json.NewDecoder(res.Body).Decode(&info); err != nil {
		return resolvedFile{}, fmt.Errorf("Pixeldrain metadata: %w", err)
	}
	if !info.Success || !info.CanDownload {
		return resolvedFile{}, fmt.Errorf("Pixeldrain reports that this file is unavailable")
	}
	name := fallbackName
	if name == "" {
		name = info.Name
	}
	sha := ""
	if len(info.HashSHA256) == 64 && isHex(info.HashSHA256) {
		sha = info.HashSHA256
	}
	return resolvedFile{
		fileName: safeFileName(name),
		size:     int64(info.Size),
		sha256:   sha,
		mirror: mirror{
			provider: providerPixeldrain,
			url:      "https://pixeldrain.com/api/file/" + id,
			priority: priorityPixeldrain,
			referer:  "https://pixeldrain.com/",
		},
	}, nil
}

func pixeldrainID(u *url.URL) string {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 2 && parts[0] == "u" && isAlnum(parts[1]) {
		return parts[1]
	}
	return ""
}

// --- FileDitch --------------------------------------------------------------

func resolveFileditch(u *url.URL, fileName string) resolvedFile {
	return resolvedFile{
		fileName: safeFileName(fileName),
		size:     sizeUnknown,
		mirror: mirror{
			provider: providerFileditch,
			url:      u.String(),
			priority: priorityFileditch,
		},
	}
}

// --- GoFile -----------------------------------------------------------------

func createGofileGuestToken(ctx context.Context, client *http.Client) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, "POST", gofileAPI+"/accounts", nil)
	req.Header.Set("User-Agent", gofileUserAgent)
	req.Header.Set("Origin", gofileOrigin)
	req.Header.Set("Referer", gofileOrigin+"/")
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("create GoFile guest account: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("create GoFile guest account: HTTP %d", res.StatusCode)
	}
	var payload struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("read GoFile guest account: %w", err)
	}
	if len(payload.Data.Token) != 32 {
		return "", fmt.Errorf("GoFile did not issue a guest token")
	}
	return payload.Data.Token, nil
}

type gofileChild struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Link string `json:"link"`
	Size int64  `json:"size"`
	ID   string `json:"id"`
	Code string `json:"code"`
}

func resolveGofile(ctx context.Context, client *http.Client, u *url.URL, token string) ([]resolvedFile, error) {
	code := gofileFolderCode(u)
	if code == "" {
		return nil, fmt.Errorf("GoFile returned an invalid folder URL")
	}
	var files []resolvedFile
	if err := collectGofile(ctx, client, code, code, token, 0, &files); err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("GoFile folder did not contain downloadable files")
	}
	return files, nil
}

func gofileFolderCode(u *url.URL) string {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 2 && parts[0] == "d" && isAlnum(parts[1]) {
		return parts[1]
	}
	return ""
}

func collectGofile(ctx context.Context, client *http.Client, contentID, root, token string, depth int, out *[]resolvedFile) error {
	if depth > 8 {
		return fmt.Errorf("GoFile folder nesting is too deep")
	}
	children, err := fetchGofileContents(ctx, client, contentID, token)
	if err != nil {
		return err
	}
	for _, child := range children {
		switch child.Type {
		case "file":
			if child.Name == "" || child.Link == "" {
				continue
			}
			parsed, err := url.Parse(child.Link)
			if err != nil || parsed.Scheme != "https" {
				continue
			}
			host := strings.ToLower(parsed.Hostname())
			if host != "gofile.io" && !strings.HasSuffix(host, ".gofile.io") {
				continue
			}
			size := sizeUnknown
			if child.Size > 0 {
				size = child.Size
			}
			*out = append(*out, resolvedFile{
				fileName: safeFileName(child.Name),
				size:     size,
				mirror: mirror{
					provider:     providerGofile,
					url:          parsed.String(),
					priority:     priorityGofile,
					referer:      gofileOrigin + "/",
					gofileFolder: root,
				},
			})
		case "folder":
			id := child.ID
			if id == "" {
				id = child.Code
			}
			if id != "" {
				if err := collectGofile(ctx, client, id, root, token, depth+1, out); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func fetchGofileContents(ctx context.Context, client *http.Client, contentID, token string) (map[string]gofileChild, error) {
	websiteToken := gofileWebsiteToken(token)
	u := fmt.Sprintf("%s/contents/%s?contentFilter=&page=1&pageSize=1000&sortField=name&sortDirection=1", gofileAPI, contentID)
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("User-Agent", gofileUserAgent)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Website-Token", websiteToken)
	req.Header.Set("X-BL", gofileLang)
	req.Header.Set("Origin", gofileOrigin)
	req.Header.Set("Referer", gofileOrigin+"/")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GoFile listing: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("GoFile listing: HTTP %d", res.StatusCode)
	}
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Children map[string]gofileChild `json:"children"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("GoFile listing: %w", err)
	}
	if payload.Status != "ok" {
		status := payload.Status
		if status == "" {
			status = "an unknown error"
		}
		return nil, fmt.Errorf("GoFile listing returned %s", status)
	}
	return payload.Data.Children, nil
}

// gofileWebsiteToken mirrors GoFile's in-page token derivation. The salt
// rotates whenever GoFile updates its web client — update gofileSalt then.
func gofileWebsiteToken(token string) string {
	now := time.Now().Unix() / 14400
	payload := fmt.Sprintf("%s::%s::%s::%d::%s", gofileUserAgent, gofileLang, token, now, gofileSalt)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// --- merge / ordering -------------------------------------------------------

func mergeFiles(files []resolvedFile) []gamePart {
	byName := map[string]*gamePart{}
	var order []string
	for _, f := range files {
		key := strings.ToLower(f.fileName)
		p, ok := byName[key]
		if !ok {
			p = &gamePart{fileName: f.fileName, size: f.size, sha256: f.sha256}
			byName[key] = p
			order = append(order, key)
		}
		// Skip mirrors that contradict an already-known size or hash.
		if p.size != sizeUnknown && f.size != sizeUnknown && p.size != f.size {
			continue
		}
		if p.sha256 != "" && f.sha256 != "" && p.sha256 != f.sha256 {
			continue
		}
		if p.size == sizeUnknown {
			p.size = f.size
		}
		if p.sha256 == "" {
			p.sha256 = f.sha256
		}
		if !hasProvider(p.mirrors, f.mirror.provider) {
			p.mirrors = append(p.mirrors, f.mirror)
			sort.SliceStable(p.mirrors, func(i, j int) bool {
				return p.mirrors[i].priority < p.mirrors[j].priority
			})
		}
	}
	parts := make([]gamePart, 0, len(order))
	for _, key := range order {
		parts = append(parts, *byName[key])
	}
	sort.SliceStable(parts, func(i, j int) bool {
		return comparePartNames(parts[i].fileName, parts[j].fileName) < 0
	})
	return parts
}

func hasProvider(mirrors []mirror, provider string) bool {
	for _, m := range mirrors {
		if m.provider == provider {
			return true
		}
	}
	return false
}

// comparePartNames orders volumes by their multipart index (so part2 < part10)
// when both share a prefix, else lexicographically.
func comparePartNames(a, b string) int {
	al, bl := strings.ToLower(a), strings.ToLower(b)
	ap, an, aok := multipartNumber(al)
	bp, bn, bok := multipartNumber(bl)
	if aok && bok && ap == bp {
		switch {
		case an < bn:
			return -1
		case an > bn:
			return 1
		default:
			return 0
		}
	}
	return strings.Compare(al, bl)
}

func multipartNumber(value string) (prefix string, number int, ok bool) {
	i := strings.Index(value, ".part")
	if i < 0 {
		return "", 0, false
	}
	rest := value[i+len(".part"):]
	digits := ""
	for _, r := range rest {
		if r < '0' || r > '9' {
			break
		}
		digits += string(r)
	}
	if digits == "" {
		return "", 0, false
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		return "", 0, false
	}
	return value[:i], n, true
}

func isFixRepair(fileName string) bool {
	var b strings.Builder
	for _, r := range strings.ToLower(fileName) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return strings.Contains(b.String(), "fixrepair")
}

func isMultipart(fileName string) bool {
	lower := strings.ToLower(fileName)
	return strings.Contains(lower, ".part") && strings.HasSuffix(lower, ".rar")
}

// --- download-time URL resolution ------------------------------------------

// resolveDownloadURL turns a stored mirror URL into the URL to actually GET.
// FileDitch and GoFile need per-attempt work: FileDitch gates files behind a
// JS proof-of-work whose unlocked link is a short-lived signed CDN URL; GoFile
// only serves a file to a token that has walked its folder tree.
func resolveDownloadURL(ctx context.Context, client *http.Client, m mirror, gofileToken string) (string, error) {
	switch m.provider {
	case providerFileditch:
		return resolveFileditchURL(ctx, client, m.url)
	case providerGofile:
		if gofileToken == "" {
			return "", fmt.Errorf("GoFile guest token is unavailable")
		}
		return resolveGofileURL(ctx, client, m, gofileToken)
	default:
		return m.url, nil
	}
}

func resolveGofileURL(ctx context.Context, client *http.Client, m mirror, token string) (string, error) {
	if m.gofileFolder == "" {
		return "", fmt.Errorf("GoFile mirror is missing its folder code")
	}
	targetID := gofileFileID(m.url)
	if targetID == "" {
		return "", fmt.Errorf("GoFile mirror URL is not a recognized file link")
	}
	targetName := gofileFileName(m.url)
	var files []resolvedFile
	if err := collectGofile(ctx, client, m.gofileFolder, m.gofileFolder, token, 0, &files); err != nil {
		return "", err
	}
	for _, f := range files {
		if gofileFileID(f.mirror.url) == targetID {
			return f.mirror.url, nil
		}
	}
	if targetName != "" {
		for _, f := range files {
			if f.fileName == targetName {
				return f.mirror.url, nil
			}
		}
	}
	return "", fmt.Errorf("GoFile folder no longer lists this file")
}

// gofileFileID pulls the stable id out of a GoFile link:
// https://file-XX.gofile.io/download/web/<fileId>/<name>
func gofileFileID(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "download" && parts[1] == "web" && parts[2] != "" {
		return parts[2]
	}
	return ""
}

func gofileFileName(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	last := parts[len(parts)-1]
	if last == "" {
		return ""
	}
	if dec, err := url.PathUnescape(last); err == nil {
		last = dec
	}
	return safeFileName(last)
}

var (
	fileditchArrayRE = regexp.MustCompile(`(?s)var\s+u\s*=\s*\[(.*?)\]\s*\.join\(`)
	fileditchSegRE   = regexp.MustCompile(`"([^"]*)"`)
)

func resolveFileditchURL(ctx context.Context, client *http.Client, pageURL string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", pageURL, nil)
	req.Header.Set("User-Agent", onlinefix.UserAgent)
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("FileDitch page: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("FileDitch page: HTTP %d", res.StatusCode)
	}
	ct := strings.TrimSpace(res.Header.Get("Content-Type"))
	if !strings.HasPrefix(ct, "text/html") {
		// FileDitch served the file directly (no gate); use the URL as-is.
		return pageURL, nil
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return "", fmt.Errorf("read FileDitch page: %w", err)
	}
	page := string(raw)

	landing := page
	if challenge := extractHiddenValue(page, "pow_challenge"); challenge != "" {
		diffStr := extractHiddenValue(page, "pow_diff")
		diff, err := strconv.Atoi(diffStr)
		if err != nil {
			return "", fmt.Errorf("FileDitch challenge missing difficulty")
		}
		ts := extractHiddenValue(page, "pow_ts")
		sig := extractHiddenValue(page, "pow_sig")
		origRef := extractHiddenValue(page, "orig_ref")
		nonce, err := solveFileditchPoW(ctx, challenge, diff)
		if err != nil {
			return "", err
		}
		form := url.Values{
			"orig_ref":      {origRef},
			"pow_challenge": {challenge},
			"pow_ts":        {ts},
			"pow_diff":      {strconv.Itoa(diff)},
			"pow_sig":       {sig},
			"pow_nonce":     {nonce},
		}
		vreq, _ := http.NewRequestWithContext(ctx, "POST", pageURL, strings.NewReader(form.Encode()))
		vreq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		vreq.Header.Set("User-Agent", onlinefix.UserAgent)
		vres, err := client.Do(vreq)
		if err != nil {
			return "", fmt.Errorf("FileDitch verify: %w", err)
		}
		defer vres.Body.Close()
		if vres.StatusCode != 200 {
			return "", fmt.Errorf("FileDitch verify: HTTP %d", vres.StatusCode)
		}
		lb, err := io.ReadAll(vres.Body)
		if err != nil {
			return "", fmt.Errorf("read FileDitch landing: %w", err)
		}
		landing = string(lb)
	}

	link := extractFileditchLink(landing)
	if link == "" {
		return "", fmt.Errorf("FileDitch did not expose a download link after verification")
	}
	return link, nil
}

func extractHiddenValue(pageHTML, name string) string {
	re, err := regexp.Compile(`name="` + regexp.QuoteMeta(name) + `"[^>]*?value="([^"]*)"`)
	if err != nil {
		return ""
	}
	m := re.FindStringSubmatch(pageHTML)
	if m == nil {
		return ""
	}
	return m[1]
}

// solveFileditchPoW brute-forces a nonce whose SHA-256 over "{challenge}:{nonce}"
// begins with diff zero bits.
func solveFileditchPoW(ctx context.Context, challenge string, diff int) (string, error) {
	const maxIterations = 50_000_000
	full := diff / 8
	remainder := diff % 8
	var mask byte
	if remainder > 0 {
		mask = 0xFF << (8 - remainder)
	}
	prefix := challenge + ":"
	for nonce := 0; nonce < maxIterations; nonce++ {
		if nonce%1_000_000 == 0 && ctx.Err() != nil {
			return "", ctx.Err()
		}
		sum := sha256.Sum256([]byte(prefix + strconv.Itoa(nonce)))
		ok := true
		for i := 0; i < full; i++ {
			if sum[i] != 0 {
				ok = false
				break
			}
		}
		if ok && (remainder == 0 || sum[full]&mask == 0) {
			return strconv.Itoa(nonce), nil
		}
	}
	return "", fmt.Errorf("FileDitch proof-of-work unsolved after %d attempts", maxIterations)
}

// extractFileditchLink reassembles the signed CDN link FileDitch injects as a
// split, escaped JS array: var u = ["https:\/\/1", ".frea", …].join("").
func extractFileditchLink(pageHTML string) string {
	m := fileditchArrayRE.FindStringSubmatch(pageHTML)
	if m == nil {
		return ""
	}
	var b strings.Builder
	for _, seg := range fileditchSegRE.FindAllStringSubmatch(m[1], -1) {
		b.WriteString(seg[1])
	}
	link := strings.ReplaceAll(b.String(), `\/`, "/")
	if strings.HasPrefix(link, "https://") || strings.HasPrefix(link, "http://") {
		return link
	}
	return ""
}

// configureDownloadRequest applies provider-specific headers to a file GET.
func configureDownloadRequest(req *http.Request, m mirror, gofileToken string) error {
	if m.referer != "" {
		req.Header.Set("Referer", m.referer)
	}
	switch m.provider {
	case providerGofile:
		if gofileToken == "" {
			return fmt.Errorf("GoFile guest token is unavailable")
		}
		req.Header.Set("User-Agent", gofileUserAgent)
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		req.Header.Set("Origin", gofileOrigin)
		req.Header.Set("Cookie", "accountToken="+gofileToken)
	default:
		req.Header.Set("User-Agent", onlinefix.UserAgent)
	}
	return nil
}

// --- small helpers ----------------------------------------------------------

func safeFileName(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch r {
		case '<', '>', ':', '"', '/', '\\', '|', '?', '*':
			b.WriteRune('_')
		default:
			if r < 0x20 {
				b.WriteRune('_')
			} else {
				b.WriteRune(r)
			}
		}
	}
	cleaned := strings.TrimRight(strings.TrimSpace(b.String()), ". ")
	if cleaned == "" {
		return "download.bin"
	}
	r := []rune(cleaned)
	if len(r) > 180 {
		r = r[:180]
	}
	return string(r)
}

func isAlnum(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

func isHex(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}
