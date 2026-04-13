# lua-dl — post-compact context (v3)

**You are reading this after a conversation compact. Read it in full before doing anything.** v3 supersedes v2: ryuu.lol source added, mirror list cleaned, download-by-appid mode, retry logic, DLC support now comes for free.

## TL;DR

Working Bun TypeScript CLI that downloads Steam games end-to-end given either a SteamTools-style `.lua` file **or just an appid**. Zero credentials, zero captcha, anonymous Steam login. Validated on Wallpaper Engine (appid 431960).

**Best throughput:** 56.3s / 11.72 MB/s on WE content depot (660 MB). 6.25× faster than original POC. Single biggest win was moving pure-JS LZMA decompression off the main thread into a 16-worker Node thread pool.

**Tier 1 is effectively done.** Steps 1-5 all complete or obsoleted by new sources. No pending roadmap items of consequence.

## What's in the repo

```
steamtools-test/
├── CONTEXT.md                  ← this file
├── package.json                (bun, steam-user ^5.3.0, lzma)
├── tsconfig.json
├── .gitignore                  (node_modules/, out/, bun.lock, *.log)
└── src/
    ├── cli.ts                  entry — parse/probe/download; accepts file.lua OR bare appid
    ├── lua.ts                  lua parser + watermark stripper
    ├── steam.ts                anonymous Steam login (TCP), getAppDepots
    ├── manifest-resolver.ts    ryuu-first, GH mirror race fallback (resolveLua + resolveManifest)
    ├── ryuu-source.ts          ryuu.lol endpoints + minimal STORED-zip parser + per-appid cache
    ├── download.ts             key injection + file worker pool + resume + .partial + 4× retry
    ├── state.ts                StateCache: out/.lua-dl-state.json
    ├── verbose.ts              VERBOSE flag + vlog() + statusLine() TTY updater
    ├── http-patch.ts           global http/https Agent: keepAlive + maxSockets + TCP_NODELAY
    ├── cdn-patch.ts            monkey-patches steam-user CdnCompression.unzip → worker pool (16 threads)
    └── lzma-worker.ts          worker thread: pure-JS lzma decompress of VZip-wrapped LZMA_ALONE
```

`out/` is gitignored. Test without any local file: `bun run src/cli.ts download 431960 --out ./out/WE-by-id`.

## The pipeline

```
arg = "431960" or "foo.lua"  → if digits && not a file: resolveLua(appid), else readFileSync
      ↓
parseLua                     → strip zero-width watermark, extract appid + depot keys
      ↓
anonymous Steam login        → steam-user, protocol: TCP (NOT WebSocket — blocked on VN ISP)
      ↓
getProductInfo([appid])      → live manifest IDs per depot via PICS
      ↓
manifest resolver            → 1. try ryuu bundle (cached in-memory per appid)
                               2. else race GH mirrors for {depotId}_{manifestId}.manifest
                               Validate magic 0x71F617D0 (LE) before using
      ↓
ContentManifest.parse        → require('steam-user/components/content_manifest.js')
      ↓
Key injection                → Monkey-patch client.getDepotDecryptionKey
      ↓
Filename decryption          → If filenames_encrypted, ContentManifest.decryptFilenames
      ↓
Resume check (per file)      → existsSync + size + SHA1 (cached or hashed once) → skip
      ↓
downloadFile × 24 parallel   → writes to outPath+'.partial', 4× retry w/ exponential backoff,
                               atomic rename on success; LZMA routed to worker pool
      ↓
state.flush                  → JSON cache of verified files for next resume
```

## Sources (ryuu-first, mirrors fallback)

### ryuu.lol (preferred)

- `GET generator.ryuu.lol/resellerlua?appid=N&auth_code=RYUUMANIFEST-setapikeyforsteamtoolsversion9700` → text/plain lua
- `GET generator.ryuu.lol/secure_download?appid=N&auth_code=...` → STORED zip containing `{appid}.lua` + all `{depotId}_{manifestId}.manifest` for the app **including DLC manifests**

The zip is fetched once per appid via `fetchRyuuBundle()` and cached in a module-level `Map<appId, Promise<RyuuBundle>>`. Every depot after the first pays zero network cost. Zip is STORED-only — `ryuu-source.ts` has a ~40-line EOCD/central-directory walker, no zip library needed.

**Ryuu lua is RICHER than GH mirrors.** Example: WE 431960 — SPIN0ZAi serves a 3-entry lua, ryuu serves a 4-entry lua including DLC 1790230 with its depot key + manifest id. This is why ryuu is first: **DLC support is free when ryuu wins**.

### GitHub mirrors (fallback, ordered by freshness)

`src/manifest-resolver.ts:MIRRORS` — checked 2026-04-13:

1. `SPIN0ZAi/SB_manifest_DB` — fork of DMCA'd `SteamAutoCracks/ManifestHub` (parent now 404). 62k+ branches, 2026-04-12 push. Also carries `.lua`/`.json`/`key.vdf` per branch.
2. `tymolu233/ManifestAutoUpdate-fix` — 2026-04-12 push
3. `BlankTMing/ManifestAutoUpdate` — 2026-03-18 push
4. `Auiowu/ManifestAutoUpdate` — 2026-02-24 push
5. `pjy612/SteamManifestCache` — 2025-07 push (safety fallback)

**Dropped** (dead, stale, or unreliable): `luomojim/ManifestAutoUpdate` (2023), `hulovewang/ManifestAutoUpdate` (2024-02), `xhcom/ManifestAutoUpdate-R` (2024-01), `tymolu233/ManifestAutoUpdate` (non-fix, 2025-03).

## CLI

```bash
bun run src/cli.ts <parse|probe|download> <file.lua|appid> [--depot ID] [--out DIR] [-v]
```

Arg detection: if it's pure digits AND not a path that exists on disk, treat as appid and call `resolveLua(appid)`. Else read the file.

## Throughput journey (don't re-debate)

Original POC: 1.87 MB/s on WE content depot (660 MB in 352s).

### Dead ends — do NOT retry

1. **HTTP keep-alive alone** — 352s unchanged. Handshake tax was NOT the bottleneck.
2. **CDN server rotation** — wrong theory.
3. **"ISP peering ceiling"** — wrong; user's real Steam client runs fast on same network.
4. **`lzma-native` package** — `aloneDecoder` throws `LZMA_DATA_ERROR: Data is corrupt` on real Steam chunks. steam-user's `requireWithFallback('lzma-native', 'lzma')` will pick it up if installed, then silently break everything. **Don't install.**

### The fix (cdn-patch.ts + lzma-worker.ts)

Pure-JS `lzma.decompress()` ran at ~1.43 MB/s single-threaded, serializing all 64 in-flight chunks behind one JS thread. Spawn 16 Node worker threads; monkey-patch `require('steam-user/components/cdn_compression.js').unzip` to route VZip chunks to the pool. Workers decode VZip→LZMA_ALONE→raw, postMessage back with transferable ArrayBuffer. Main thread is freed.

| Change | Time | MB/s | Delta |
|---|---|---|---|
| Worker pool (8), CONCURRENCY=16 | 65.2s | 10.1 | baseline |
| Workers → 16, CONCURRENCY → 24 | 58.7s | 11.25 | +11% |
| + TCP_NODELAY in http-patch.ts | **56.3s** | **11.72** | +4% |

~12 MB/s ≈ 100 Mbps from this connection to Steam SG CDN. Network ceiling. Remaining invasive wins (patching `download()` to drop `setEncoding('binary')` + `Buffer.concat` O(n²)) are 5-10% and not worth the debt.

## Critical tricks (don't re-learn)

1. **WSS to Steam CM is blocked on VN network.** Always `protocol: SteamUser.EConnectionProtocol.TCP`.
2. **`GetManifestRequestCode` returns AccessDenied** for anonymous on paid apps. Bypass via mirror/ryuu `.manifest` binaries (magic `d0 17 f6 71` LE) fed directly to `ContentManifest.parse`.
3. **Openlua.cloud watermarks** the first line with zero-width Unicode. `src/lua.ts` strips `[\u200B-\u200D\uFEFF\u2060]` before regex.
4. **addappid regex label bug** fixed — use `[ \t]*(?:--[ \t]*([^\n]*))?` not `\s*(?:--\s*(.*))?`.
5. **CdnCompression shared via require cache.** `require('steam-user/components/cdn_compression.js')` in our patch gets the SAME module instance cdn.js uses. Mutating `.unzip` reroutes internal calls without forking steam-user.
6. **steam-user CDN pipeline** (from reading cdn.js directly):
   - `downloadFile` → per-file async queue of 4 chunk workers
   - each: `downloadChunk` → `download()` → HTTP GET → AES decrypt → `CdnCompression.unzip` → SHA1 verify → `FS.write` at offset
   - `downloadFile` with outputFilePath uses `FS.open` + `ftruncate` + seek writes — no full-file buffer
   - `download()` is module-private, un-patchable without replacing all of cdn.js
7. **`lzma-native` is broken.** See dead end #4. Don't install it, don't install any other LZMA package without testing real Steam chunks.
8. **Transient CDN timeouts** exist — a `getContentServers` webapi call can time out mid-download. `download.ts` now retries each file up to 4 times with exponential backoff (500ms → 1s → 2s). Don't rip this out.
9. **openlua.cloud is not automatable.** Checked 2026-04-13: Cloudflare Turnstile tokens are single-use; replaying fails with `CAPTCHA_FAILED`. Only works with solver service or live headed browser harvest. Ignore it.

## User preferences / constraints

- **No credentials, no captcha, no headless browsers.** Rejected.
- **CLI over userscript.** Rejected userscript for the download side.
- **Bun + TypeScript.** Don't suggest Python/Rust/C#.
- **Never `bun dev`** — only lint/typecheck. Per global CLAUDE.md.
- **Terse responses.**
- **Research before guessing.** User pushed back hard on the "ISP ceiling" theory that had no measurement backing it. Measure the suspicious thing in isolation before hypothesizing.
- **Use Explore/MCP research tools** — but remember they can be wrong (initial keep-alive theory was from Explore and wrong).

## Smoke tests

```bash
cd /c/Users/HP\ MEDIA/Desktop/nguyenvu/steamtools-test

# By-appid, no local file
bun run src/cli.ts parse 431960
# Expect: "[resolver] ✓ ryuu.lol/resellerlua (521 bytes lua)" and 4 entries including DLC 1790230

# Full download by appid
bun run src/cli.ts download 431960 --out ./out/WE-by-id
# Expect ~56s for 431961 content depot + ~1s for 431966 localization depot
# DLC 1790230 ALSO downloads now because ryuu lua includes it

# Legacy by-file path still works
bun run src/cli.ts download "/c/Users/HP MEDIA/Downloads/431960.lua" --out ./out/WE-by-file

# CPU sampling sanity check (workers should be busy)
CPU_SAMPLE=1 bun run src/cli.ts download 431960 --out ./out/cpu-check 2>&1 | grep '\[cpu\]'
# Expect [cpu] main=1000-1600% during active download (aggregated across threads)
```

## Roadmap — Tier 1 status

- [x] Step 1: Cleanup pass (lua regex, verbose gating, status line)
- [x] Step 2: Resume support (StateCache, `.partial` atomic rename, SHA1 caching)
- [x] Step 3 (bonus): Worker pool LZMA + TCP_NODELAY = 6.25× speedup
- [x] Step 4: Mirror list cleanup (dead mirrors dropped, SPIN0ZAi added, reordered)
- [x] Step 5: DLC support — **free** via ryuu lua, which inlines DLC depot keys + manifest IDs. When ryuu wins (always, now), DLCs just flow through the existing `targets[]` logic.
- [x] Bonus: Download-by-appid mode (no local .lua needed)
- [x] Bonus: Retry logic for transient CDN timeouts
- [x] Bonus: ryuu.lol source (richer than GH mirrors, DLC-aware, single-fetch per app)

No remaining Tier 1 items. Tier 2/3 from older plans (real argv parsing, watch mode, `bun build --compile`, README) are unchanged and still future work.

## Known minor items

1. **No stale-mirror detection.** If live PICS manifest ID is newer than what ryuu/mirrors have, we download an old build without warning. Low priority since ryuu's zip is typically day-fresh.
2. **Worker pool initial spin-up** shows as one big CPU burst in the first sample. Cosmetic.
3. **`keepAliveMsecs` + `maxSockets` in http-patch.ts** — unverified whether they contribute. TCP_NODELAY is what actually moved the needle. Don't over-claim.
4. **Uncommitted work.** Pre-compact, the ryuu + resolver + download retry changes are not committed yet. Status shows modified: cli.ts, download.ts, manifest-resolver.ts + new ryuu-source.ts. User hasn't explicitly asked to commit.

## Things NOT to do

- Don't suggest Steam credentials. Rejected.
- Don't suggest userscript. Rejected.
- Don't suggest openlua.cloud automation. Requires captcha solving — rejected.
- Don't install `lzma-native`. Broken.
- Don't install any other LZMA package without testing real Steam chunks.
- Don't `bun dev`.
- Don't re-theorize about keep-alive / CDN rotation / ISP ceilings. Throughput story is SOLVED.
- Don't redownload Wallpaper Engine unless measuring a specific change. Use `--depot 431966` (8 MB localization) for quick smoke tests.
- Don't add backwards-compat shims or re-export types. Delete dead code.
- Don't try to fix `download()` inside steam-user's cdn.js via a fork. Tier 3 territory.
- Don't rip out the retry loop in `download.ts` — it's there because real transient CDN timeouts happen.
