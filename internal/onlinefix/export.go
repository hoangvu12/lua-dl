package onlinefix

import (
	"context"
	"net/http"
	"net/http/cookiejar"
)

// This file exposes a thin, stable surface over the Fix-Repair flow's
// internals so the full-game downloader (internal/onlinefixgame) can reuse the
// authenticated online-fix.me session, the windows-1251 page fetcher, and the
// RAR extractor without duplicating the login protocol. Every function here
// delegates to the existing unexported implementation — no behavior changes.

// Shared constants for the full-game downloader.
const (
	SiteURL     = siteURL
	UserAgent   = userAgent
	RarPassword = rarPassword
)

// NewSession builds a cookie-jar-backed HTTP client and logs into
// online-fix.me with the shared subscriber account. The returned client's jar
// carries the session cookies needed to read authenticated pages (the article
// and the Hosters listing).
func NewSession(ctx context.Context) (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Jar: jar}
	if err := login(ctx, client); err != nil {
		return nil, err
	}
	return client, nil
}

// FetchHTML GETs a page (carrying the session jar and a Referer) and returns
// its body decoded from windows-1251, the site's encoding.
func FetchHTML(ctx context.Context, client *http.Client, url, referer string) (string, error) {
	return fetchString(ctx, client, url, referer)
}

// Get issues a GET with the shared browser headers and a Referer, carrying the
// session jar. Used for authenticated non-HTML fetches.
func Get(ctx context.Context, client *http.Client, url, referer string) (*http.Response, error) {
	return get(ctx, client, url, referer)
}

// SetBrowserHeaders applies the shared User-Agent / Referer headers to req.
func SetBrowserHeaders(req *http.Request, referer string) { setBrowserHeaders(req, referer) }

// ExtractOver extracts a (possibly multi-volume) RAR at rarPath over destDir
// using the shared "online-fix.me" password. Returns the number of files
// written. rardecode auto-follows sibling volumes, so pass the primary volume.
func ExtractOver(rarPath, destDir string) (int, error) { return extractOver(rarPath, destDir) }

// PrimaryRAR returns the entry-point volume (.part1./.part01.) for rardecode,
// or the first name for single-volume archives.
func PrimaryRAR(names []string) string { return primaryRAR(names) }
