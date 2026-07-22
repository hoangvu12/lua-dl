package cdn

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hoangvu12/lua-dl/internal/verbose"
)

// manifestMagic is the first 4 bytes of a decompressed Steam depot manifest
// (PROTOBUF_PAYLOAD_MAGIC). We validate it so a CDN error page that happens to
// unzip doesn't sail through into the parser.
const manifestMagic uint32 = 0x71f617d0

// FetchManifest pulls the raw (decompressed, still filename-encrypted) depot
// manifest for (depotID, manifestID) directly from Steam's CDN, using a
// manifest request code the caller obtained from an upstream API.
//
// This is the OpenSteamTool-style path: instead of relying on a third party
// having archived the .manifest binary, we let Steam's own CDN serve it. The
// returned bytes carry manifestMagic and are ready for
// steamcdn.NewDepotManifest(buf, depotKey).
//
// Retries across the server pool with exponential backoff, mirroring the
// chunk-download retry policy.
func (d *Downloader) FetchManifest(ctx context.Context, appID, depotID uint32, manifestID, requestCode uint64) ([]byte, error) {
	var lastErr error
	backoff := 500 * time.Millisecond
	for attempt := 1; attempt <= maxChunkAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		srv := d.pool.Pick()
		host := srv.GetHost()

		// Steam CDN manifest path: /depot/{id}/manifest/{gid}/5/{code}. The
		// trailing 5 is the manifest version format. The request code is a
		// path segment; the CDN auth token (optional for manifests) is a query
		// string, matching the chunk URL layout.
		url := fmt.Sprintf("https://%s/depot/%d/manifest/%d/5/%d", host, depotID, manifestID, requestCode)
		if token, err := d.getAuthToken(appID, depotID, host); err == nil && token != "" {
			url += "?" + token
		}

		raw, err := d.fetchManifestOnce(ctx, url)
		if err != nil {
			lastErr = err
			verbose.Vlog("[manifest] gid %d attempt %d/%d via %s: %v",
				manifestID, attempt, maxChunkAttempts, host, err)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
			continue
		}
		return raw, nil
	}
	return nil, fmt.Errorf("manifest %d failed after %d attempts: %w",
		manifestID, maxChunkAttempts, lastErr)
}

func (d *Downloader) fetchManifestOnce(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	res, err := d.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	// Steam serves manifests as a PK zip wrapping a single entry whose bytes
	// are the decompressed manifest. Reuse the depot chunk zip decoder.
	raw, err := decompressPKZip(body)
	if err != nil {
		return nil, fmt.Errorf("unzip manifest: %w", err)
	}
	if len(raw) < 4 || binary.LittleEndian.Uint32(raw[:4]) != manifestMagic {
		return nil, fmt.Errorf("bad manifest magic (got %d bytes)", len(raw))
	}
	return raw, nil
}
