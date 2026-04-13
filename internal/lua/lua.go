// Package lua parses openlua.cloud-style SteamTools lua files.
//
// Handles:
//   - Zero-width watermark chars (openlua.cloud fingerprints leaked files)
//   - addappid(APPID, 1, "DEPOT_KEY")
//   - setManifestid(DEPOTID, "MANIFEST_ID", 0)
package lua

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

type DepotEntry struct {
	ID         uint32
	Key        string // lowercase hex
	ManifestID string // present only if setManifestid was used
	Label      string // from trailing comment, best-effort
}

type ParseResult struct {
	AppID  uint32
	Depots []DepotEntry
	Raw    string
}

var (
	zeroWidth = regexp.MustCompile("[\u200B-\u200D\uFEFF\u2060]")
	addRe     = regexp.MustCompile(`addappid\s*\(\s*(\d+)\s*,\s*\d+\s*,\s*"([0-9a-fA-F]+)"\s*\)[ \t]*(?:--[ \t]*([^\n]*))?`)
	setRe     = regexp.MustCompile(`setManifestid\s*\(\s*(\d+)\s*,\s*"(\d+)"`)
)

func Parse(source string) (*ParseResult, error) {
	clean := zeroWidth.ReplaceAllString(source, "")

	// Ordered map: insertion order matters for reproducibility.
	type entry struct {
		idx int
		e   DepotEntry
	}
	depots := make(map[uint32]*entry)
	order := 0

	for _, m := range addRe.FindAllStringSubmatch(clean, -1) {
		id, err := strconv.ParseUint(m[1], 10, 32)
		if err != nil {
			continue
		}
		key := strings.ToLower(m[2])
		label := ""
		if len(m) > 3 {
			label = strings.TrimSpace(m[3])
		}
		depots[uint32(id)] = &entry{
			idx: order,
			e:   DepotEntry{ID: uint32(id), Key: key, Label: label},
		}
		order++
	}

	for _, m := range setRe.FindAllStringSubmatch(clean, -1) {
		id, err := strconv.ParseUint(m[1], 10, 32)
		if err != nil {
			continue
		}
		if existing, ok := depots[uint32(id)]; ok {
			existing.e.ManifestID = m[2]
		} else {
			depots[uint32(id)] = &entry{
				idx: order,
				e:   DepotEntry{ID: uint32(id), ManifestID: m[2]},
			}
			order++
		}
	}

	// Flatten preserving insertion order.
	out := make([]DepotEntry, len(depots))
	for _, e := range depots {
		out[e.idx] = e.e
	}
	// Compact in case of gaps (should not occur with single-threaded parse).
	compact := out[:0]
	for _, e := range out {
		if e.ID != 0 {
			compact = append(compact, e)
		}
	}

	// First entry with a key is the "main" app.
	var appID uint32
	for _, d := range compact {
		if d.Key != "" {
			appID = d.ID
			break
		}
	}
	if appID == 0 {
		return nil, errors.New("no addappid(...) entries found")
	}

	return &ParseResult{AppID: appID, Depots: compact, Raw: clean}, nil
}
