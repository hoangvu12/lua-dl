// Package manifestcode fetches a Steam "manifest request code" for a given
// manifest GID from a public upstream API.
//
// Background: Steam gates manifest downloads behind a per-(depot,manifest)
// request code obtained via CM's GetManifestRequestCode, which AccessDenies
// anonymous accounts on paid apps. OpenSteamTool sidesteps this by fetching
// the code from a third-party API that runs a real logged-in account, then
// letting Steam's CDN serve the manifest directly. We do the same: with the
// code in hand, the manifest comes fresh from Steam's own CDN — no dependency
// on someone having archived the .manifest binary in a (DMCA-prone) GitHub
// mirror.
//
// Providers are tried in order; the first to return a usable uint64 wins.
package manifestcode

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hoangvu12/lua-dl/internal/verbose"
)

// perProviderTimeout bounds each upstream call. Codes are tiny responses, so
// a slow provider is a dead provider — move on quickly.
const perProviderTimeout = 10 * time.Second

type provider struct {
	name  string
	urlF  string // Printf template taking the gid as a single %d
	parse func([]byte) uint64
}

// providers ordered by observed reliability (checked 2026-07-22):
//   - wudrm returns a bare decimal uint64 and is the most consistently up.
//   - steam.run returns JSON {"content":"..."}.
//   - opensteamtool is the OST default but sits behind Cloudflare, so it's
//     last — it often serves a JS challenge to non-browser clients.
var providers = []provider{
	{"wudrm", "http://gmrc.wudrm.com/manifest/%d", parsePlain},
	{"steamrun", "https://manifest.steam.run/api/manifest/%d", parseJSON},
	{"opensteamtool", "https://manifest.opensteamtool.com/%d", parsePlain},
}

var jsonContentRe = regexp.MustCompile(`"content"\s*:\s*"(\d+)"`)

// Fetch returns a manifest request code for gid, plus the provider name that
// served it. Tries each provider until one yields a non-zero uint64.
func Fetch(ctx context.Context, gid uint64) (code uint64, source string, err error) {
	var errs []string
	for _, p := range providers {
		c, e := fetchFrom(ctx, p, gid)
		if e == nil && c != 0 {
			verbose.Vlog("[mcode] ✓ %s → %d (gid %d)", p.name, c, gid)
			return c, p.name, nil
		}
		if e == nil {
			e = fmt.Errorf("empty/unparseable body")
		}
		verbose.Vlog("[mcode] %s failed for gid %d: %v", p.name, gid, e)
		errs = append(errs, p.name+": "+e.Error())
	}
	return 0, "", fmt.Errorf("no provider served a code for gid %d:\n    - %s",
		gid, strings.Join(errs, "\n    - "))
}

func fetchFrom(ctx context.Context, p provider, gid uint64) (uint64, error) {
	cctx, cancel := context.WithTimeout(ctx, perProviderTimeout)
	defer cancel()

	url := fmt.Sprintf(p.urlF, gid)
	req, err := http.NewRequestWithContext(cctx, "GET", url, nil)
	if err != nil {
		return 0, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return 0, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	// Cap the body — a legit response is <64 bytes; anything huge is a
	// Cloudflare/error page.
	body, err := io.ReadAll(io.LimitReader(res.Body, 64*1024))
	if err != nil {
		return 0, err
	}
	return p.parse(body), nil
}

// parsePlain accepts a body that is exactly a decimal uint64 (optional
// surrounding whitespace).
func parsePlain(body []byte) uint64 {
	s := strings.TrimSpace(string(body))
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// parseJSON pulls the digit string out of {"content":"12345..."}.
func parseJSON(body []byte) uint64 {
	m := jsonContentRe.FindSubmatch(body)
	if m == nil {
		return 0
	}
	n, err := strconv.ParseUint(string(m[1]), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
