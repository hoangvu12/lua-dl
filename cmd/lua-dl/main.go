// lua-dl CLI.
//
// Usage:
//
//	lua-dl parse    <file.lua|appid>            [-v]
//	lua-dl probe    <file.lua|appid>            [-v]
//	lua-dl download <file.lua|appid> [--depot N] [--out DIR] [-v]
//
// A bare appid is treated as a source only if the argument is pure digits and
// no file of that name exists on disk.
package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/Lucino772/envelop/pkg/steam/steamcdn"

	"github.com/hoangvu12/lua-dl/internal/cdn"
	"github.com/hoangvu12/lua-dl/internal/lua"
	"github.com/hoangvu12/lua-dl/internal/resolver"
	"github.com/hoangvu12/lua-dl/internal/sanitize"
	"github.com/hoangvu12/lua-dl/internal/state"
	"github.com/hoangvu12/lua-dl/internal/steam"
	"github.com/hoangvu12/lua-dl/internal/verbose"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 3 {
		usage()
		os.Exit(1)
	}
	cmd := os.Args[1]
	arg := os.Args[2]
	rest := os.Args[3:]
	if hasFlag(rest, "-v") || hasFlag(rest, "--verbose") {
		verbose.Set(true)
	}

	ctx := context.Background()

	// 1) Load source (lua text) — from file or by-appid.
	var source, sourceLabel string
	if isDigits(arg) && !fileExists(arg) {
		appID64, _ := strconv.ParseUint(arg, 10, 32)
		appID := uint32(appID64)
		resCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		rl, err := resolver.ResolveLua(resCtx, appID)
		cancel()
		if err != nil {
			return err
		}
		source = rl.Text
		sourceLabel = fmt.Sprintf("appid %d via %s", appID, rl.Source)
	} else {
		b, err := os.ReadFile(arg)
		if err != nil {
			return fmt.Errorf("read %s: %w", arg, err)
		}
		source = string(b)
		sourceLabel = arg
	}

	parsed, err := lua.Parse(source)
	if err != nil {
		return err
	}

	fmt.Printf("\n== Parsed %s ==\n", sourceLabel)
	fmt.Printf("App ID: %d\n", parsed.AppID)
	fmt.Printf("Entries: %d\n", len(parsed.Depots))
	for _, d := range parsed.Depots {
		keyDisp := "(no key)"
		if d.Key != "" {
			keyDisp = d.Key[:12] + "…"
		}
		manif := ""
		if d.ManifestID != "" {
			manif = " manifest=" + d.ManifestID
		}
		label := ""
		if d.Label != "" {
			label = " — " + d.Label
		}
		fmt.Printf("  %d  key=%s%s%s\n", d.ID, keyDisp, manif, label)
	}

	if cmd == "parse" {
		return nil
	}

	// 2) Steam connection — shared by probe and download.
	client := steam.NewClient()
	if err := client.Connect(30 * time.Second); err != nil {
		return fmt.Errorf("steam connect: %w", err)
	}
	if err := client.LogInAnonymously(); err != nil {
		return fmt.Errorf("steam login: %w", err)
	}

	if cmd == "probe" {
		fmt.Println("\n== Probing Steam for live manifest IDs ==")
		info, err := client.GetAppInfo(parsed.AppID)
		if err != nil {
			return err
		}
		fmt.Printf("Steam returned %d depot entries:\n", len(info.Depots))
		for _, d := range info.Depots {
			hasKey := "  "
			for _, lua := range parsed.Depots {
				if lua.ID == d.DepotID && lua.Key != "" {
					hasKey = "✓ key"
					break
				}
			}
			sz := ""
			if d.MaxSize > 0 {
				sz = fmt.Sprintf(" (%.2f GB)", float64(d.MaxSize)/1e9)
			}
			fmt.Printf("  %s  %d  manifest=%d%s  %s\n", hasKey, d.DepotID, d.ManifestID, sz, d.Name)
		}
		return nil
	}

	if cmd != "download" {
		usage()
		os.Exit(1)
	}

	// 3) Download.
	onlyDepot := uint32(0)
	if v := flagVal(rest, "--depot"); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return fmt.Errorf("--depot: %w", err)
		}
		onlyDepot = uint32(n)
	}

	info, err := client.GetAppInfo(parsed.AppID)
	if err != nil {
		return fmt.Errorf("PICS: %w", err)
	}

	outDir := flagVal(rest, "--out")
	if outDir == "" {
		outDir = filepath.Join(".", sanitize.FolderName(info.Name))
	}
	fmt.Fprintf(os.Stderr, "\n== Game: %s ==\n== Output: %s ==\n", info.Name, outDir)

	stateCache := state.New(filepath.Join(outDir, ".lua-dl-state.json"))

	// Build a lookup of lua depot keys.
	luaByID := make(map[uint32]lua.DepotEntry, len(parsed.Depots))
	for _, d := range parsed.Depots {
		luaByID[d.ID] = d
	}

	// Decide manifest id per depot. Prefer PICS (live); fall back to lua's
	// setManifestid value when PICS has none (common for DLCs not owned by
	// the main app).
	type target struct {
		depotID    uint32
		manifestID uint64
		key        []byte
	}
	var targets []target

	// First pass: PICS depots that have a lua key.
	picsSeen := make(map[uint32]bool)
	for _, d := range info.Depots {
		picsSeen[d.DepotID] = true
		if onlyDepot != 0 && d.DepotID != onlyDepot {
			continue
		}
		le, ok := luaByID[d.DepotID]
		if !ok || le.Key == "" {
			continue
		}
		if d.ManifestID == 0 {
			continue
		}
		kb, err := hex.DecodeString(le.Key)
		if err != nil {
			return fmt.Errorf("depot %d: bad lua key: %w", d.DepotID, err)
		}
		targets = append(targets, target{d.DepotID, d.ManifestID, kb})
	}
	// Second pass: lua-only depots (e.g. DLCs not visible via parent PICS).
	for _, le := range parsed.Depots {
		if picsSeen[le.ID] {
			continue
		}
		if le.Key == "" || le.ManifestID == "" {
			continue
		}
		if onlyDepot != 0 && le.ID != onlyDepot {
			continue
		}
		mid, err := strconv.ParseUint(le.ManifestID, 10, 64)
		if err != nil {
			continue
		}
		kb, err := hex.DecodeString(le.Key)
		if err != nil {
			continue
		}
		targets = append(targets, target{le.ID, mid, kb})
	}

	if len(targets) == 0 {
		return fmt.Errorf("no downloadable depots matched filter")
	}

	fmt.Fprintf(os.Stderr, "\n== Downloading %d depot(s) to %s ==\n", len(targets), outDir)

	dl, err := cdn.NewDownloader(client, stateCache)
	if err != nil {
		return err
	}

	for _, t := range targets {
		fmt.Fprintf(os.Stderr, "\n[depot %d] manifest=%d\n", t.depotID, t.manifestID)

		mCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		rm, err := resolver.ResolveManifest(mCtx, parsed.AppID, t.depotID, t.manifestID)
		cancel()
		if err != nil {
			_ = stateCache.Flush()
			return err
		}

		manifest, err := steamcdn.NewDepotManifest(rm.Buffer, t.key)
		if err != nil {
			_ = stateCache.Flush()
			return fmt.Errorf("depot %d parse: %w", t.depotID, err)
		}

		// Count non-directory files for progress.
		var files, bytes uint64
		for _, f := range manifest.Files {
			if f.TotalSize > 0 {
				files++
				bytes += f.TotalSize
			}
		}
		fmt.Fprintf(os.Stderr, "[depot %d] %d files, %.1f MB\n",
			t.depotID, files, float64(bytes)/1e6)

		req := cdn.DepotRequest{
			AppID:      parsed.AppID,
			DepotID:    t.depotID,
			ManifestID: t.manifestID,
			DepotKey:   t.key,
			Manifest:   manifest,
			OutputDir:  outDir,
		}
		start := time.Now()
		if err := dl.DownloadDepot(ctx, req); err != nil {
			_ = stateCache.Flush()
			return fmt.Errorf("depot %d: %w", t.depotID, err)
		}
		elapsed := time.Since(start).Seconds()
		if elapsed > 0.5 {
			fmt.Fprintf(os.Stderr, "[depot %d] done in %.1fs (%.2f MB/s)\n",
				t.depotID, elapsed, (float64(bytes)/1e6)/elapsed)
		} else {
			fmt.Fprintf(os.Stderr, "[depot %d] done in %.2fs (cached)\n",
				t.depotID, elapsed)
		}
	}

	return stateCache.Flush()
}

var digitsRe = regexp.MustCompile(`^\d+$`)

func isDigits(s string) bool { return digitsRe.MatchString(s) }

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

func flagVal(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: lua-dl <parse|probe|download> <file.lua|appid> [--depot ID] [--out DIR] [-v]")
}
