// Package cdn orchestrates per-file parallel chunk downloads on top of
// envelop's steamcdn.Client. Responsibilities:
//
//   - Round-robin a filtered CDN server pool
//   - Cache per-(app,depot,host) auth tokens
//   - Retry each chunk 4× with exponential backoff (matches the TS version)
//   - Post-decompress SHA1 verify against the manifest chunk id (stronger
//     than envelop's built-in adler32-only check)
//   - Resume via StateCache + atomic .partial rename
//   - Intra-file chunk parallelism plus a global chunk semaphore
package cdn

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Lucino772/envelop/pkg/steam/steamcdn"
	"github.com/Lucino772/envelop/pkg/steam/steamlang"
	"golang.org/x/sync/errgroup"

	"github.com/hoangvu12/lua-dl/internal/state"
	"github.com/hoangvu12/lua-dl/internal/steam"
	"github.com/hoangvu12/lua-dl/internal/verbose"
)

const (
	// Shared chunk semaphore across all files in a depot. Matches the TS
	// CONCURRENCY=24 setting that saturates a ~100 Mbps link to Steam SG.
	maxParallelChunks = 24
	// Upper bound on retries per chunk before giving up.
	maxChunkAttempts = 4
)

// DepotRequest is everything needed to download one depot's files.
type DepotRequest struct {
	AppID      uint32
	DepotID    uint32
	ManifestID uint64
	DepotKey   []byte
	Manifest   *steamcdn.DepotManifest
	OutputDir  string // files land directly inside this dir
}

// Downloader is stateful: it holds the CDN server pool, auth token cache,
// and a reference to the Steam client (for refreshing tokens).
type Downloader struct {
	steam *steam.Client
	pool  *ServerPool
	state *state.Cache
	httpc *http.Client

	tokenMu sync.Mutex
	tokens  map[tokenKey]string
}

type tokenKey struct {
	appID, depotID uint32
	host           string
}

// NewDownloader fetches + filters the CDN server list and returns a ready
// Downloader. Call once per CLI invocation.
func NewDownloader(client *steam.Client, cache *state.Cache) (*Downloader, error) {
	servers, err := client.GetServersForSteamPipe(0)
	if err != nil {
		return nil, fmt.Errorf("cdn: GetServersForSteamPipe: %w", err)
	}
	pool, err := NewServerPool(servers)
	if err != nil {
		return nil, err
	}
	verbose.Errf("[cdn] %d servers available", pool.Size())
	// Tuned transport: HTTP/1.1 keep-alive with a generous per-host pool so
	// we don't open-and-close a socket per chunk. TCP_NODELAY is on by
	// default in Go's net package.
	transport := &http.Transport{
		MaxIdleConns:        64,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     90 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &Downloader{
		steam:  client,
		pool:   pool,
		state:  cache,
		httpc:  &http.Client{Transport: transport, Timeout: 60 * time.Second},
		tokens: make(map[tokenKey]string),
	}, nil
}

// DownloadDepot grinds through every file in the manifest, writing them
// into req.OutputDir. Respects ctx cancellation and returns the first
// non-nil error.
func (d *Downloader) DownloadDepot(ctx context.Context, req DepotRequest) error {
	if err := os.MkdirAll(req.OutputDir, 0o755); err != nil {
		return err
	}

	chunkSem := make(chan struct{}, maxParallelChunks)
	g, gctx := errgroup.WithContext(ctx)

	// Count real files upfront (manifest entries include directories).
	var total int64
	for _, f := range req.Manifest.Files {
		if uint32(f.Flags)&uint32(steamlang.EDepotFileFlag_Directory) == 0 {
			total++
		}
	}
	var done int64
	var doneMu sync.Mutex

	for i := range req.Manifest.Files {
		file := req.Manifest.Files[i]
		// Directory entries — mkdir and move on.
		if uint32(file.Flags)&uint32(steamlang.EDepotFileFlag_Directory) != 0 {
			dirPath := filepath.Join(req.OutputDir, toOSPath(file.Filename))
			if err := os.MkdirAll(dirPath, 0o755); err != nil {
				return err
			}
			continue
		}
		g.Go(func() error {
			if err := d.downloadFile(gctx, req, file, chunkSem); err != nil {
				return fmt.Errorf("file %q: %w", file.Filename, err)
			}
			doneMu.Lock()
			done++
			doneMu.Unlock()
			verbose.StatusLine(fmt.Sprintf("[cdn] %d/%d  %s", done, total, file.Filename))
			return nil
		})
	}

	err := g.Wait()
	verbose.StatusDone()
	if err != nil {
		return err
	}
	return d.state.Flush()
}

func (d *Downloader) downloadFile(ctx context.Context, req DepotRequest, file steamcdn.FileData, chunkSem chan struct{}) error {
	rel := toOSPath(file.Filename)
	outPath := filepath.Join(req.OutputDir, rel)

	// Resume check: if the cached entry matches size+sha, skip entirely.
	if st, err := os.Stat(outPath); err == nil && uint64(st.Size()) == file.TotalSize {
		if e, ok := d.state.Get(req.DepotID, req.ManifestID, file.Filename); ok &&
			e.Size == st.Size() && e.SHA1 == fmt.Sprintf("%x", file.FileHash) {
			verbose.Vlog("[cdn] skip %s (cached)", file.Filename)
			return nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}

	// Empty file short-circuit.
	if file.TotalSize == 0 || len(file.Chunks) == 0 {
		if err := os.WriteFile(outPath, nil, 0o644); err != nil {
			return err
		}
		d.state.Set(req.DepotID, req.ManifestID, file.Filename, state.Entry{
			Size:  0,
			SHA1:  fmt.Sprintf("%x", file.FileHash),
			MTime: time.Now().Unix(),
		})
		return nil
	}

	partial := outPath + ".partial"
	f, err := os.OpenFile(partial, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Truncate(int64(file.TotalSize)); err != nil {
		return err
	}

	// Fan out chunks. Each chunk goroutine takes from the shared semaphore
	// so we get roughly maxParallelChunks in flight across all files.
	g, gctx := errgroup.WithContext(ctx)
	for i := range file.Chunks {
		chunk := file.Chunks[i]
		g.Go(func() error {
			select {
			case chunkSem <- struct{}{}:
			case <-gctx.Done():
				return gctx.Err()
			}
			defer func() { <-chunkSem }()

			data, err := d.downloadChunkWithRetry(gctx, req, chunk)
			if err != nil {
				return err
			}
			if _, err := f.WriteAt(data, int64(chunk.Offset)); err != nil {
				return err
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		_ = f.Close()
		_ = os.Remove(partial)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(partial, outPath); err != nil {
		return err
	}
	d.state.Set(req.DepotID, req.ManifestID, file.Filename, state.Entry{
		Size:  int64(file.TotalSize),
		SHA1:  fmt.Sprintf("%x", file.FileHash),
		MTime: time.Now().Unix(),
	})
	return nil
}

func (d *Downloader) downloadChunkWithRetry(ctx context.Context, req DepotRequest, chunk steamcdn.ChunkData) ([]byte, error) {
	var lastErr error
	backoff := 500 * time.Millisecond
	for attempt := 1; attempt <= maxChunkAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		srv := d.pool.Pick()
		host := srv.GetHost()
		token, err := d.getAuthToken(req.AppID, req.DepotID, host)
		if err != nil {
			// Token fetch failures are often transient; log and retry.
			lastErr = fmt.Errorf("auth token for %s: %w", host, err)
			verbose.Vlog("[cdn] chunk %x attempt %d/%d token err: %v",
				chunk.ChunkId[:4], attempt, maxChunkAttempts, err)
			goto sleep
		}
		{
			data, err := fetchAndDecodeChunk(ctx, d.httpc, host, req.DepotID, chunk, req.DepotKey, token)
			if err != nil {
				lastErr = err
				verbose.Vlog("[cdn] chunk %x attempt %d/%d dl err: %v",
					chunk.ChunkId[:4], attempt, maxChunkAttempts, err)
				goto sleep
			}
			sum := sha1.Sum(data)
			if !bytesEqual(sum[:], chunk.ChunkId) {
				lastErr = errors.New("chunk sha1 mismatch")
				verbose.Vlog("[cdn] chunk %x attempt %d/%d sha mismatch",
					chunk.ChunkId[:4], attempt, maxChunkAttempts)
				goto sleep
			}
			return data, nil
		}
	sleep:
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return nil, fmt.Errorf("chunk %x failed after %d attempts: %w",
		chunk.ChunkId[:4], maxChunkAttempts, lastErr)
}

func (d *Downloader) getAuthToken(appID, depotID uint32, host string) (string, error) {
	k := tokenKey{appID: appID, depotID: depotID, host: host}
	d.tokenMu.Lock()
	if t, ok := d.tokens[k]; ok {
		d.tokenMu.Unlock()
		return t, nil
	}
	d.tokenMu.Unlock()

	t, err := d.steam.GetCDNAuthToken(appID, depotID, host)
	if err != nil {
		return "", err
	}
	d.tokenMu.Lock()
	d.tokens[k] = t
	d.tokenMu.Unlock()
	return t, nil
}

// toOSPath converts manifest filenames (which use backslashes) to OS-native
// path separators.
func toOSPath(name string) string {
	// Manifests use backslashes — Windows native, Linux foreign.
	return filepath.FromSlash(strings.ReplaceAll(name, "\\", "/"))
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
