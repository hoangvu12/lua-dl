// Package resolver fetches a lua script or a .manifest binary for a given
// (appId, depotId, manifestId) via ryuu.lol first, then racing a list of
// GitHub ManifestAutoUpdate-style mirrors.
//
// This bypasses Steam's GetManifestRequestCode gate (which rejects anonymous
// accounts for paid apps). We already know the correct manifestId from live
// PICS — we just need someone else's already-fetched copy of the binary.
package resolver

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/hoangvu12/lua-dl/internal/ryuu"
	"github.com/hoangvu12/lua-dl/internal/verbose"
)

// Mirrors ordered by freshness (most-recent pushed_at first, checked 2026-04-13).
// SPIN0ZAi/SB_manifest_DB is a fork of the DMCA'd SteamAutoCracks/ManifestHub.
var Mirrors = []string{
	"SPIN0ZAi/SB_manifest_DB",
	"tymolu233/ManifestAutoUpdate-fix",
	"BlankTMing/ManifestAutoUpdate",
	"Auiowu/ManifestAutoUpdate",
	"pjy612/SteamManifestCache",
}

const manifestMagic uint32 = 0x71f617d0

var addAppidRe = regexp.MustCompile(`(?i)addappid\s*\(`)

type ResolvedLua struct {
	Source string
	Text   string
}

type ResolvedManifest struct {
	Buffer []byte
	Source string
}

// ResolveLua returns a lua script for the app, trying ryuu first.
func ResolveLua(ctx context.Context, appID uint32) (*ResolvedLua, error) {
	verbose.Errf("[resolver] trying ryuu.lol for %d.lua", appID)
	if text, err := ryuu.FetchLua(ctx, appID); err == nil {
		verbose.Errf("[resolver] ✓ ryuu.lol/resellerlua (%d bytes lua)", len(text))
		return &ResolvedLua{Source: "ryuu.lol/resellerlua", Text: text}, nil
	} else {
		verbose.Errf("[resolver] ryuu.lol failed (%v), racing %d GH mirrors", err, len(Mirrors))
	}

	type result struct {
		repo string
		text string
	}
	res, err := raceMirrors(ctx, func(ctx context.Context, repo string) (any, error) {
		url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%d/%d.lua", repo, appID, appID)
		body, err := httpGet(ctx, url)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", repo, err)
		}
		text := string(body)
		if !addAppidRe.MatchString(text) {
			return nil, fmt.Errorf("%s: not a lua script", repo)
		}
		return result{repo: repo, text: text}, nil
	})
	if err != nil {
		return nil, fmt.Errorf("all sources failed to serve %d.lua: %w", appID, err)
	}
	r := res.(result)
	verbose.Errf("[resolver] ✓ %s (%d bytes lua)", r.repo, len(r.text))
	return &ResolvedLua{Source: r.repo, Text: r.text}, nil
}

// ResolveManifest returns the raw .manifest binary for (depotId, manifestId),
// trying ryuu's bundle first then racing the GH mirrors.
func ResolveManifest(ctx context.Context, appID, depotID uint32, manifestID uint64) (*ResolvedManifest, error) {
	filename := fmt.Sprintf("%d_%d.manifest", depotID, manifestID)

	if bundle, err := ryuu.FetchBundle(ctx, appID); err == nil {
		if data, ok := bundle.Files[filename]; ok {
			if err := validateManifest("ryuu.lol/secure_download", data); err != nil {
				return nil, err
			}
			verbose.Errf("[resolver] ✓ ryuu.lol/secure_download %s (%d bytes)", filename, len(data))
			return &ResolvedManifest{Buffer: data, Source: "ryuu.lol/secure_download"}, nil
		}
		verbose.Errf("[resolver] ryuu bundle missing %s, falling back to GH mirrors", filename)
	} else {
		verbose.Errf("[resolver] ryuu.lol failed (%v), racing %d GH mirrors", err, len(Mirrors))
	}

	verbose.Errf("[resolver] racing %d mirrors for %d/%s", len(Mirrors), appID, filename)
	type result struct {
		repo string
		buf  []byte
	}
	res, err := raceMirrors(ctx, func(ctx context.Context, repo string) (any, error) {
		url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%d/%s", repo, appID, filename)
		body, err := httpGet(ctx, url)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", repo, err)
		}
		if err := validateManifest(repo, body); err != nil {
			return nil, err
		}
		return result{repo: repo, buf: body}, nil
	})
	if err != nil {
		return nil, fmt.Errorf("all sources failed for %s: %w", filename, err)
	}
	r := res.(result)
	verbose.Errf("[resolver] ✓ %s (%d bytes)", r.repo, len(r.buf))
	return &ResolvedManifest{Buffer: r.buf, Source: r.repo}, nil
}

func validateManifest(name string, buf []byte) error {
	if len(buf) < 4 {
		return fmt.Errorf("%s: too small (%d bytes)", name, len(buf))
	}
	m := binary.LittleEndian.Uint32(buf[:4])
	if m != manifestMagic {
		return fmt.Errorf("%s: bad magic 0x%x", name, m)
	}
	return nil
}

// raceMirrors runs fn(repo) for each mirror concurrently. The first success
// wins; once a winner exists, the context for the rest is cancelled. If all
// fail, returns a joined error.
func raceMirrors(ctx context.Context, fn func(context.Context, string) (any, error)) (any, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type outcome struct {
		val any
		err error
	}
	ch := make(chan outcome, len(Mirrors))
	var wg sync.WaitGroup
	for _, repo := range Mirrors {
		wg.Add(1)
		go func(repo string) {
			defer wg.Done()
			v, err := fn(ctx, repo)
			ch <- outcome{val: v, err: err}
		}(repo)
	}
	go func() { wg.Wait(); close(ch) }()

	var errs []string
	for o := range ch {
		if o.err == nil {
			return o.val, nil
		}
		errs = append(errs, o.err.Error())
	}
	return nil, errors.New("\n  - " + strings.Join(errs, "\n  - "))
}

func httpGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return io.ReadAll(res.Body)
}
