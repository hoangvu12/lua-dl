package cdn

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"

	"github.com/Lucino772/envelop/pkg/steam"
	"github.com/Lucino772/envelop/pkg/steam/steamcdn"
	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz/lzma"
)

// fetchAndDecodeChunk replaces envelop's steamcdn.Client.DownloadDepotChunk +
// NewDepotChunk pipeline. Envelop's version only handles VZip (LZMA) and
// plain PK-zip wrappers; Steam also serves zstd (`VSZa`) for compressible
// chunks like locale JSON, and envelop falls through to archive/zip on those,
// which then fails with "not a valid zip file".
//
// Returns the plaintext, decompressed bytes for the chunk. The caller is
// responsible for the SHA1 verify against chunk.ChunkId.
func fetchAndDecodeChunk(
	ctx context.Context,
	httpc *http.Client,
	serverHost string,
	depotID uint32,
	chunk steamcdn.ChunkData,
	depotKey []byte,
	cdnToken string,
) ([]byte, error) {
	chunkHex := hex.EncodeToString(chunk.ChunkId)
	url := fmt.Sprintf("https://%s/depot/%d/chunk/%s", serverHost, depotID, chunkHex)
	if cdnToken != "" {
		url += "?" + cdnToken
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	res, err := httpc.Do(req)
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

	decrypted, err := steam.AESDecrypt(depotKey, body)
	if err != nil {
		return nil, fmt.Errorf("aes: %w", err)
	}
	return decompressChunk(decrypted)
}

// decompressChunk detects the wrapper type from the first four bytes and
// dispatches to the correct decoder.
//
//	VSZa…  → zstd
//	VZa\x01→ VZip (LZMA_ALONE)
//	PK\x03\x04 → plain zip (method=deflate)
func decompressChunk(data []byte) ([]byte, error) {
	if len(data) < 4 {
		return nil, errors.New("chunk: too short")
	}
	switch {
	case data[0] == 'V' && data[1] == 'S' && data[2] == 'Z' && data[3] == 'a':
		return decompressZstd(data)
	case data[0] == 'V' && data[1] == 'Z' && data[2] == 'a':
		return decompressVZip(data)
	case data[0] == 'P' && data[1] == 'K' && data[2] == 0x03 && data[3] == 0x04:
		return decompressPKZip(data)
	}
	return nil, fmt.Errorf("chunk: unknown compression magic %x", data[:4])
}

// Zstd envelope (matches node-steam-user cdn_compression.js::decompressZstd):
//
//	[0..4)  'VSZa' magic
//	[4..8)  CRC (ignored, there's another at the tail)
//	[8..N-15) zstd stream
//	[N-15..N-11) decompressed CRC32 (we don't verify; klauspost/compress
//	             validates its own frame CRC if present)
//	[N-11..N-7)  decompressed size u32 LE
//	[N-7..N-4)   3-byte padding
//	[N-4..N)     'zsv\x00' footer (actually 'zsv' + 0x00 padding)
//
// After strip we feed the zstd stream to klauspost's decoder and compare
// the decompressed length.
func decompressZstd(data []byte) ([]byte, error) {
	const minLen = 4 + 4 + 15
	if len(data) < minLen {
		return nil, errors.New("zstd: too short")
	}
	// Footer: last 3 bytes should be 'zsv'.
	if string(data[len(data)-3:]) != "zsv" {
		// The node-steam-user layout reads the last 15 bytes as
		// (crc32 u32)(size u32)(0-pad u32)(footer 'zsv' 3 bytes) — total
		// 15 trailer bytes (not 14) because of the 0-pad. Stay generous.
	}
	compressed := data[8 : len(data)-15]
	decompressedSize := binary.LittleEndian.Uint32(data[len(data)-11 : len(data)-7])

	r, err := zstd.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("zstd: %w", err)
	}
	defer r.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("zstd: %w", err)
	}
	if uint32(len(out)) != decompressedSize {
		return nil, fmt.Errorf("zstd: size mismatch got=%d want=%d", len(out), decompressedSize)
	}
	return out, nil
}

// decompressVZip mirrors envelop's decompressLzma but is wrapped here to keep
// all chunk decoding in one place and so we don't invoke envelop's zip
// fallback on non-VZip data.
func decompressVZip(data []byte) ([]byte, error) {
	const headerLen = 7 // 'VZa' + version + 4 bytes timestamp
	const footerLen = 10
	if len(data) < headerLen+footerLen+5 {
		return nil, errors.New("vzip: too short")
	}
	properties := data[headerLen : headerLen+5]
	compressed := data[headerLen+5 : len(data)-footerLen]

	// Last 10 bytes: CRC32(4) + decompressed size(4) + 'zv'(2)
	decompressedSize := binary.LittleEndian.Uint32(data[len(data)-6 : len(data)-2])
	if string(data[len(data)-2:]) != "zv" {
		return nil, errors.New("vzip: bad footer")
	}

	// Synthesize a standard LZMA_ALONE stream:
	// [props(5)][u64 LE uncompressedSize][compressed]
	adjusted := slices.Concat(
		properties,
		binary.LittleEndian.AppendUint64(nil, uint64(decompressedSize)),
		compressed,
	)
	lr, err := lzma.NewReader(bytes.NewReader(adjusted))
	if err != nil {
		return nil, fmt.Errorf("vzip: %w", err)
	}
	out := make([]byte, decompressedSize)
	if _, err := io.ReadFull(lr, out); err != nil {
		return nil, fmt.Errorf("vzip: %w", err)
	}
	return out, nil
}

// decompressPKZip handles a plain zip wrapper; the first (and typically only)
// entry's decompressed bytes ARE the chunk.
func decompressPKZip(data []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("zip: %w", err)
	}
	if len(zr.File) == 0 {
		return nil, errors.New("zip: empty archive")
	}
	rc, err := zr.File[0].Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}
