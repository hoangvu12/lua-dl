// lua-dl CLI.
//
// Usage:
//
//	lua-dl parse    <file.lua|appid>            [-v]
//	lua-dl probe    <file.lua|appid>            [-v]
//	lua-dl download <file.lua|appid> [--depots 1,2,3|--all] [--out DIR] [-v]
//
// Depot selection:
//   - no flag + TTY → interactive picker (base depot pre-selected, required)
//   - --all         → everything in the lua file
//   - --depots LIST → comma-separated depot IDs (base always included)
//
// A bare appid is treated as a source only if the argument is pure digits and
// no file of that name exists on disk.
package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Lucino772/envelop/pkg/steam/steamcdn"
	"golang.org/x/term"

	"github.com/hoangvu12/lua-dl/internal/cdn"
	"github.com/hoangvu12/lua-dl/internal/lua"
	"github.com/hoangvu12/lua-dl/internal/picker"
	"github.com/hoangvu12/lua-dl/internal/resolver"
	"github.com/hoangvu12/lua-dl/internal/sanitize"
	"github.com/hoangvu12/lua-dl/internal/state"
	"github.com/hoangvu12/lua-dl/internal/steam"
	"github.com/hoangvu12/lua-dl/internal/verbose"
)

const targetLang = "english"

// depotKind classifies a depot into exactly one bucket, in priority order.
// The numeric order is also the picker sort order (core at top).
type depotKind int

const (
	kindCore        depotKind = iota // base content, locked-on, required to run
	kindUserLocale                   // language pack matching targetLang
	kindOtherLocale                  // language pack for some other language
	kindDLC                          // DLC, optional
	kindWrongOS                      // binaries for a different OS
	kindOther                        // anything left (e.g. lowviolence variants)
)

// target is a downloadable depot paired with everything we need to display
// and classify it. Filled by buildCandidates; consumed by selectTargets.
type target struct {
	depotID    uint32
	manifestID uint64
	key        []byte
	name       string
	size       uint64
	kind       depotKind
	language   string
	oslist     []string
}

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

	source, sourceLabel, err := loadSource(ctx, arg)
	if err != nil {
		return err
	}

	parsed, err := lua.Parse(source)
	if err != nil {
		return err
	}

	if cmd == "parse" || verbose.Enabled() {
		printParsed(sourceLabel, parsed)
	}
	if cmd == "parse" {
		return nil
	}

	client, err := connectSteam()
	if err != nil {
		return err
	}

	switch cmd {
	case "probe":
		return cmdProbe(ctx, client, parsed)
	case "download":
		return cmdDownload(ctx, client, parsed, rest)
	default:
		usage()
		os.Exit(1)
		return nil
	}
}

// loadSource reads the lua source either from a file on disk or, if arg is
// a bare appid, by resolving it through the lua-dl resolver mirrors.
func loadSource(ctx context.Context, arg string) (source, label string, err error) {
	if isDigits(arg) && !fileExists(arg) {
		appID64, _ := strconv.ParseUint(arg, 10, 32)
		appID := uint32(appID64)
		resCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		rl, err := resolver.ResolveLua(resCtx, appID)
		if err != nil {
			return "", "", err
		}
		return rl.Text, fmt.Sprintf("appid %d via %s", appID, rl.Source), nil
	}
	b, err := os.ReadFile(arg)
	if err != nil {
		return "", "", fmt.Errorf("read %s: %w", arg, err)
	}
	return string(b), arg, nil
}

func printParsed(label string, parsed *lua.ParseResult) {
	fmt.Printf("\n== Parsed %s ==\n", label)
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
}

func connectSteam() (*steam.Client, error) {
	// Silence envelop's "Following packet was not handled" log.Println spam.
	// These are cosmetic — envelop routes unknown incoming messages through
	// the standard log package. Dropping them keeps our output clean.
	if !verbose.Enabled() {
		log.SetOutput(io.Discard)
	}
	client := steam.NewClient()
	if err := client.Connect(30 * time.Second); err != nil {
		return nil, fmt.Errorf("steam connect: %w", err)
	}
	if err := client.LogInAnonymously(); err != nil {
		return nil, fmt.Errorf("steam login: %w", err)
	}
	return client, nil
}

func cmdProbe(ctx context.Context, client *steam.Client, parsed *lua.ParseResult) error {
	fmt.Println("\n== Probing Steam for live manifest IDs ==")
	info, err := steam.FetchAppInfo(ctx, client, parsed.AppID)
	if err != nil {
		return err
	}
	fmt.Printf("Steam returned %d depot entries:\n", len(info.Depots))
	for _, d := range info.Depots {
		hasKey := "  "
		for _, le := range parsed.Depots {
			if le.ID == d.DepotID && le.Key != "" {
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

func cmdDownload(ctx context.Context, client *steam.Client, parsed *lua.ParseResult, rest []string) error {
	selectAll := hasFlag(rest, "--all")
	depotFilter, err := parseDepotFilter(flagVal(rest, "--depots"))
	if err != nil {
		return err
	}

	info, err := steam.FetchAppInfo(ctx, client, parsed.AppID)
	if err != nil {
		return fmt.Errorf("app info: %w", err)
	}

	outDir := flagVal(rest, "--out")
	if outDir == "" {
		outDir = filepath.Join(".", sanitize.FolderName(info.Name))
	}

	candidates, err := buildCandidates(parsed, info)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return fmt.Errorf("no downloadable depots found in lua file")
	}
	enrichNames(ctx, client, candidates)
	candidates = prepareCandidates(candidates, depotFilter)

	targets, err := selectTargets(candidates, depotFilter, selectAll, info, parsed)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return fmt.Errorf("no depots selected")
	}

	var totalPick uint64
	for _, t := range targets {
		totalPick += t.size
	}
	sizeHint := ""
	if totalPick > 0 {
		sizeHint = fmt.Sprintf(" (~%.2f GB)", float64(totalPick)/1e9)
	}
	fmt.Fprintf(os.Stderr, "\n%s (%d)\n%d depot(s)%s → %s\n\n",
		info.Name, parsed.AppID, len(targets), sizeHint, outDir)

	stateCache := state.New(filepath.Join(outDir, ".lua-dl-state.json"))
	return runDownloads(ctx, client, targets, outDir, parsed.AppID, stateCache)
}

func parseDepotFilter(v string) (map[uint32]bool, error) {
	if v == "" {
		return nil, nil
	}
	out := map[uint32]bool{}
	for _, s := range strings.Split(v, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		n, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("--depots: bad id %q: %w", s, err)
		}
		out[uint32(n)] = true
	}
	return out, nil
}

// buildCandidates walks the lua entries that have a key and a resolvable
// manifest id, enriching each with PICS metadata (name/size/filter flags)
// when available.
func buildCandidates(parsed *lua.ParseResult, info *steam.AppInfo) ([]target, error) {
	picsByID := make(map[uint32]steam.Depot, len(info.Depots))
	for _, d := range info.Depots {
		picsByID[d.DepotID] = d
	}

	targetOS := steamOS()
	var out []target
	for _, le := range parsed.Depots {
		if le.Key == "" {
			continue
		}
		kb, err := hex.DecodeString(le.Key)
		if err != nil {
			return nil, fmt.Errorf("depot %d: bad lua key: %w", le.ID, err)
		}
		pd, inPICS := picsByID[le.ID]
		mid := resolveManifestID(pd, inPICS, le.ManifestID)
		if mid == 0 {
			continue
		}
		out = append(out, target{
			depotID:    le.ID,
			manifestID: mid,
			key:        kb,
			name:       pd.Name,
			size:       pd.MaxSize,
			language:   pd.Language,
			oslist:     pd.OSList,
			kind:       classify(pd, inPICS, targetOS),
		})
	}
	return out, nil
}

// classify maps a PICS depot entry to a single depotKind bucket. Follows
// DepotDownloader's install filter (ContentDownloader.cs): a depot is core
// (always installed) only when it has no language, no DLC link, matches the
// user's OS, and isn't a lowviolence variant. Anything else is optional.
//
// lua-only depots (not in PICS) are treated as DLCs — they're almost always
// DLCs the parent app doesn't list for anonymous access.
func classify(pd steam.Depot, inPICS bool, targetOS string) depotKind {
	wrongOS := len(pd.OSList) > 0 && !slices.Contains(pd.OSList, targetOS)
	switch {
	case pd.Language == targetLang:
		return kindUserLocale
	case pd.Language != "":
		return kindOtherLocale
	case pd.DLCAppID != 0 || !inPICS:
		return kindDLC
	case wrongOS:
		return kindWrongOS
	case pd.LowViolence:
		return kindOther
	}
	return kindCore
}

// prepareCandidates drops wrong-OS depots (unless force-added via --depots),
// sorts by kind for a scannable picker, and installs a safety-net core if
// classification found none.
func prepareCandidates(candidates []target, depotFilter map[uint32]bool) []target {
	// Drop other-OS depots entirely — lua-dl is Windows-only so macOS/Linux
	// binaries are dead weight. --depots can still force-add them by id.
	kept := candidates[:0]
	for _, c := range candidates {
		if c.kind == kindWrongOS && !depotFilter[c.depotID] {
			continue
		}
		kept = append(kept, c)
	}

	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].kind != kept[j].kind {
			return kept[i].kind < kept[j].kind
		}
		return kept[i].depotID < kept[j].depotID
	})

	// Safety net: if nothing classified as core (PICS may be sparse on older
	// games), promote the smallest-id depot so at least one thing is locked.
	hasCore := false
	for _, c := range kept {
		if c.kind == kindCore {
			hasCore = true
			break
		}
	}
	if !hasCore && len(kept) > 0 {
		kept[0].kind = kindCore
	}
	return kept
}

func resolveManifestID(pd steam.Depot, inPICS bool, luaManifest string) uint64 {
	if inPICS && pd.ManifestID != 0 {
		return pd.ManifestID
	}
	if luaManifest == "" {
		return 0
	}
	n, _ := strconv.ParseUint(luaManifest, 10, 64)
	return n
}

// enrichNames fills in missing names by looking up each depot id as an app id.
// Steam DLC appids usually equal the depot id, so this often pulls back a
// real title ("Wallpaper Engine — Workshop") without the parent app having
// to list the DLC explicitly. Best-effort, runs in parallel.
func enrichNames(ctx context.Context, client *steam.Client, candidates []target) {
	var wg sync.WaitGroup
	for i := range candidates {
		c := &candidates[i]
		if c.name != "" {
			continue
		}
		wg.Add(1)
		go func(c *target) {
			defer wg.Done()
			if sub, err := steam.FetchAppInfo(ctx, client, c.depotID); err == nil && sub.Name != "" {
				c.name = sub.Name
			}
		}(c)
	}
	wg.Wait()
}

func selectTargets(candidates []target, depotFilter map[uint32]bool, selectAll bool, info *steam.AppInfo, parsed *lua.ParseResult) ([]target, error) {
	switch {
	case depotFilter != nil:
		var out []target
		for _, c := range candidates {
			if c.kind == kindCore || depotFilter[c.depotID] {
				out = append(out, c)
			}
		}
		return out, nil
	case selectAll:
		return candidates, nil
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, fmt.Errorf("no TTY for interactive picker — pass --all or --depots 1,2,3")
	}
	items := make([]picker.Item, len(candidates))
	for i, c := range candidates {
		// Default-on: core (locked) + the user's language (unlocked).
		defaultOn := c.kind == kindCore || c.kind == kindUserLocale
		items[i] = picker.Item{
			Label:    candidateLabel(c),
			Hint:     candidateHint(c),
			Tag:      candidateTag(c),
			Selected: defaultOn,
			Locked:   c.kind == kindCore,
		}
	}
	title := fmt.Sprintf("\r\n%s (%d) — select depots to download:", info.Name, parsed.AppID)
	picked, err := picker.Run(title, items)
	if err != nil {
		return nil, err
	}
	var out []target
	for i, it := range picked {
		if it.Selected {
			out = append(out, candidates[i])
		}
	}
	return out, nil
}

func candidateTag(c target) string {
	switch c.kind {
	case kindCore:
		return "[core]"
	case kindUserLocale, kindOtherLocale:
		return "[" + c.language + "]"
	case kindDLC:
		return "[DLC]"
	case kindWrongOS:
		if len(c.oslist) > 0 {
			return "[" + strings.Join(c.oslist, "/") + "]"
		}
		return "[other OS]"
	}
	return ""
}

func candidateHint(c target) string {
	if c.size == 0 {
		return ""
	}
	return fmt.Sprintf("%.2f GB", float64(c.size)/1e9)
}

func candidateLabel(c target) string {
	if c.name != "" {
		return fmt.Sprintf("%d  %s", c.depotID, c.name)
	}
	return strconv.FormatUint(uint64(c.depotID), 10)
}

func runDownloads(ctx context.Context, client *steam.Client, targets []target, outDir string, appID uint32, stateCache *state.Cache) error {
	dl, err := cdn.NewDownloader(client, stateCache)
	if err != nil {
		return err
	}

	start := time.Now()
	for _, t := range targets {
		verbose.Vlog("\n[depot %d] manifest=%d", t.depotID, t.manifestID)

		mCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		rm, err := resolver.ResolveManifest(mCtx, appID, t.depotID, t.manifestID)
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

		req := cdn.DepotRequest{
			AppID:      appID,
			DepotID:    t.depotID,
			ManifestID: t.manifestID,
			DepotKey:   t.key,
			Manifest:   manifest,
			OutputDir:  outDir,
		}
		if err := dl.DownloadDepot(ctx, req); err != nil {
			_ = stateCache.Flush()
			return fmt.Errorf("depot %d: %w", t.depotID, err)
		}
	}

	elapsed := time.Since(start).Seconds()
	if elapsed > 0.5 {
		fmt.Fprintf(os.Stderr, "\nDone in %.1fs.\n", elapsed)
	} else {
		fmt.Fprintf(os.Stderr, "\nDone (resumed from cache).\n")
	}
	return stateCache.Flush()
}

// steamOS maps Go's GOOS to Steam's config.oslist tokens. Steam uses "macos"
// where Go uses "darwin"; "windows" and "linux" match.
func steamOS() string {
	if runtime.GOOS == "darwin" {
		return "macos"
	}
	return runtime.GOOS
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

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
	fmt.Fprintln(os.Stderr, "Usage: lua-dl <parse|probe|download> <file.lua|appid> [--all|--depots 1,2,3] [--out DIR] [-v]")
}
