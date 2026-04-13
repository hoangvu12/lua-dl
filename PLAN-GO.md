# PLAN-GO — Go rewrite of lua-dl

**You are reading this fresh after a conversation clear. Read this file in full, then skim `CONTEXT.md` (pipeline details, gotchas, things-not-to-do) and `PLAN.md` (original Bun-based release/bot/bat plan). Together these three files are enough to execute.**

Written 2026-04-13. Supersedes `PLAN.md` Phase 1 (the Bun `build:exe` path). Phases 2–8 of `PLAN.md` (release workflow, Discord bot, `.bat` auto-updater, hosting, smoke tests) **still apply almost unchanged** — they just ship a Go binary instead of a Bun-compiled one. Only the release workflow's build step changes.

---

## TL;DR — why Go, and what exactly to do

Bun `--compile` fought us for hours on the existing TypeScript CLI:

- `steam-user`'s CJS package uses static `require('./xxx.json')` for 96 protobuf files — bun bundled them but something in the chain still crashes at runtime with "Cannot find package 'steamid'".
- Worker threads with `new Worker(new URL("./lzma-worker.ts", import.meta.url))` didn't get bundled (`ModuleNotFound: B:\~BUN\root\lzma-worker.ts`).
- Even after fixing `createRequire` → static imports in three files, Steam login times out in the compiled exe. Likely protobuf `.json` files not actually in the bundle.
- Compiled binary is **112 MB** even with `--minify`. The plan estimated 15–25.

The TypeScript version still runs perfectly under `bun run`. It's only `--compile` that's broken. So: **port the CLI to Go** for a ~10–15 MB single static binary that doesn't need a JS runtime at the other end.

**Three research findings shape this plan:**

1. **`Lucino772/envelop`** has a Go package `pkg/steam/steamcdn` with working `DownloadManifest`, `DownloadDepotChunk`, and a full `DepotManifest` parser. **If it handles chunks correctly, this eliminates the biggest block of porting work.** Day 1 of the port is validating this claim end-to-end.
2. **`paralin/go-steam`** (active TCP-only fork of go-steam, last real commit 2025-05) has the protocol wire layer, protobufs, and CM directory — but **no anonymous logon helper** (`Auth.LogOn` panics without a password) and **no PICS ProductInfo wrapper**. Both need to be hand-rolled. ~200 LOC total.
3. **No Go VDF/KeyValues parser is bundled** anywhere we checked. The PICS response ships app info as binary VDF in the `AppInfo.Buffer` field — we need a ~100 LOC binary-KV parser. SteamKit2's `KeyValue.cs TryReadAsBinary` is the reference.

**Effort estimate:** best case (envelop works) ~5–7 days. Worst case (envelop's chunk pipeline is broken or incomplete) ~2 weeks.

---

## What's already done (don't redo)

The working TypeScript CLI at `src/` is the **reference implementation**. Do NOT delete it — it's our oracle for testing the Go port against known-good output.

Validated working (56.3s for Wallpaper Engine 660 MB under `bun run src/cli.ts`):

```
src/
├── cli.ts                  entry — parse/probe/download; accepts file.lua OR bare appid
├── lua.ts                  lua parser + watermark stripper
├── steam.ts                anonymous Steam login (TCP), getAppInfo (name + depots)
├── manifest-resolver.ts    ryuu-first, GH mirror race fallback
├── ryuu-source.ts          ryuu.lol endpoints + STORED-zip parser + per-appid cache
├── download.ts             key injection + file worker pool + resume + .partial + 4× retry
├── state.ts                StateCache: out/.lua-dl-state.json
├── verbose.ts              VERBOSE flag + vlog() + statusLine() TTY updater
├── http-patch.ts           global http/https Agent tuning
├── cdn-patch.ts            monkey-patches steam-user CdnCompression.unzip → worker pool
├── lzma-worker.ts          worker thread: pure-JS lzma decompress of VZip-wrapped LZMA_ALONE
├── sanitize.ts             (Phase 1 addition) Windows filename sanitizer
└── bundle-prelude.ts       (failed --compile attempt artifact — can delete)
```

**The Phase 1 edits from `PLAN.md`** were all completed before the pivot:

- `src/sanitize.ts` (new) — Windows folder-name sanitizer
- `src/steam.ts` — added `getAppInfo()` that returns `{name, depots}` alongside the existing `getAppDepots()`
- `src/cli.ts` — uses `getAppInfo`, defaults outDir to `./<sanitized game name>/` when `--out` is not passed
- `package.json` — `build:exe` script added (the one that doesn't work)
- `.gitignore` — bot/ and `*.exe` entries
- `README.md` — minimal user-facing doc

Light smoke test passed: `bun run src/cli.ts download 431960 --depot 431966` → files land in `./Wallpaper Engine/`.

**Uncommitted git state** at time of writing:

```
modified: CONTEXT.md (unmodified since the pivot), src/cli.ts, src/download.ts,
          src/lua.ts, src/manifest-resolver.ts, src/steam.ts, package.json, .gitignore
added:    PLAN.md, PLAN-GO.md (this file), README.md,
          src/sanitize.ts, src/bundle-prelude.ts, src/cdn-patch.ts, src/http-patch.ts,
          src/lzma-worker.ts, src/ryuu-source.ts, src/state.ts, src/verbose.ts
```

None of it is committed yet. **Get explicit user OK before committing anything.**

Note: `src/cdn-patch.ts`, `src/download.ts`, `src/lzma-worker.ts` were edited during the failed `--compile` attempt to replace `createRequire` with static ESM imports. Those edits are strictly improvements (they still work under `bun run` — verified) and should stay.

---

## Research findings — details for the port

### Reference implementations (open these in a tab while porting)

Pinned commits so a future agent can fetch stable URLs:

- **SteamKit2 `DepotManifest.cs`** — 593 LOC, the canonical manifest parser.
  `https://github.com/SteamRE/SteamKit/blob/f5caf601556db0327e9f55e0675bec3ee0cec1ac/SteamKit2/SteamKit2/Types/DepotManifest.cs`
- **SteamKit2 `Client.cs`** (the CDN one) — 355 LOC, CDN HTTP pipeline + chunk decrypt + LZMA.
  `https://github.com/SteamRE/SteamKit/blob/f5caf601556db0327e9f55e0675bec3ee0cec1ac/SteamKit2/SteamKit2/Steam/CDN/Client.cs`
- **DepotDownloader `ContentDownloader.cs`** — 1,433 LOC monster that does orchestration, resume, diffing, parallelism. Our CLI doesn't need license acquisition or diffing, so most of it is reference-only.
  `https://github.com/SteamRE/DepotDownloader/blob/4aac37401cff628d3938b50b5e6ab2d6fc93c69f/DepotDownloader/ContentDownloader.cs`
- **node-steam-user internals** — already on disk at `node_modules/steam-user/components/{cdn.js,cdn_compression.js,content_manifest.js}`. Use these as the byte-level oracle since our TypeScript CLI already works against them.

### The existing Go library to build on: `paralin/go-steam`

Repo: `github.com/paralin/go-steam` · master last pushed 2026-03-31 · 23 stars · Go 1.24 · **no CGo**. Maintained in a narrow sense (occasional dep bumps) but paralin doesn't merge community PRs. Expect to fork.

**What's in the box:**

- TCP-only wire layer. `connection.go` dials with `VT01` magic. **No WebSocket code path at all** — perfect for our VN-ISP constraint.
- `steam_directory.go` + `InitializeSteamDirectory()` — fetches fresh CM list from `api.steampowered.com/ISteamDirectory/GetCMList/v1/?cellId=0`. Falls back to a hardcoded `CMServers` slice in `servers.go` if not called.
- `protocol/protobuf/content_manifest.pb.go` — **full schema** for `ContentManifestPayload`, `ContentManifestMetadata`, `ContentManifestSignature`.
- `protocol/protobuf/client_server.pb.go` — contains `CMsgClientPICSProductInfoRequest/Response` and the `_AppInfo` sub-message with the `Buffer []byte` field (binary VDF blob).
- `protocol/steamlang/enums.go` — `EMsg_ClientPICSProductInfoRequest = 8903`, `EMsg_ClientPICSProductInfoResponse = 8904`, `EMsg_ClientLogon = 5514`, `EUniverse_Public`, `EAccountType_AnonUser`.
- Message send/recv plumbing: `client.Write(protocol.NewClientMsgProtobuf(emsg, pbBody))` to send; implement `PacketHandler` + `client.RegisterPacketHandler(h)` for recv. `p.ReadProtoMsg(new(PbType))` to decode.

**What's NOT in the box — you will write these:**

1. **Anonymous logon.** `Auth.LogOn` panics on empty password. The `steamId` field on the client is private, so you can't set it directly — you either fork `auth.go` to allow empty password + `EAccountType_AnonUser`, or bypass `Auth.LogOn` entirely and construct your own `CMsgClientLogon` with `AccountName="anonymous"` and stamp the steamid via unsafe/reflection. **Recommendation: fork `paralin/go-steam` into the project, vendor it, patch `auth.go` to add a `LogOnAnonymous()` method.** Keeps the change small and auditable.
2. **PICS wrapper.** Send `CMsgClientPICSProductInfoRequest` with `Apps: [{Appid: appid}]`. Register a handler for `EMsg_ClientPICSProductInfoResponse`. Responses are fragmented — loop collecting responses until `GetResponsePending() == false`, accumulating `Apps`. Also handle the `HttpHost`/`HttpMinSize` branch where Steam asks you to fetch large payloads over HTTPS (rare for single-app requests, but has to not crash).
3. **Binary VDF/KV parser.** The `AppInfo.Buffer` field is a binary KV blob. Type tags: `0x00` nested object open, `0x01` string, `0x02` int32, `0x08` end-of-object. Null-terminated keys and string values. Reference: SteamKit2 `KeyValue.cs TryReadAsBinary`. ~100 LOC. We only need to extract `common.name` and `depots.<depotId>.manifests.public.gid` — all other fields can be thrown away.

### node-steam-user byte-level details (our oracle)

**Content server list fetch:**

```
GET https://api.steampowered.com/IContentServerDirectoryService/GetServersForSteamPipe/v1/?cell_id=<N>
-> { response: { servers: [...] } }
```

Filter: `server.type == 'CDN' || 'SteamCache'`. Skip if `allowed_app_ids` is set and the app isn't in it. Pick uniformly at random (NOT weighted). Cache ~1 hour.

**Manifest binary format.** No outer wrapper — three/four concatenated sections:

```
magic(u32LE)  length(u32LE)  protobuf_bytes[length]
```

Magic numbers (all LE):

```
PAYLOAD_MAGIC          = 0x71F617D0   -> ContentManifestPayload  -> manifest.files (proto field: "mappings")
METADATA_MAGIC         = 0x1F4812BE   -> ContentManifestMetadata -> merged flat onto manifest
SIGNATURE_MAGIC        = 0x1B81B817   -> skip (steam-user doesn't parse)
ENDOFMANIFEST_MAGIC    = 0x32C415AB   -> no length field, terminator
STEAM3_MANIFEST_MAGIC  = 0x16349781   -> unsupported, throw
```

`ContentManifestPayload.mappings` → our files list. Field is named `mappings` in the proto, NOT `files`.

**Outer compression is separate.** Before `parse()` ever sees the buffer, the raw HTTP body goes through `cdn_compression.js::unzip`, detected by first 4 bytes:

```
'VSZa'      -> zstd envelope
'VZa\x01'   -> VZip (LZMA_ALONE) envelope
'PK\x03\x04'-> plain zip (used for manifests, NOT chunks)
```

**VZip envelope** (used for chunks, little-endian):

```
offset  size  field
 0       3    'VZa'                   magic (ASCII)
 3       1    version byte            (usually 0x01, ignored)
 4       4    timestamp-or-CRC        (ignored)
 8       5    LZMA properties (lc/lp/pb + dict size)
13       N    compressed LZMA stream  (N = total - 23)
-10      4    decompressed CRC32      (zlib CRC32, NOT CRC32C)
 -6      4    decompressed size (u32)
 -2      2    'zv'                    footer magic
```

Decompress by synthesizing a standard LZMA_ALONE stream: `[properties(5)][uncompressedSize as u64 LE, high 4 bytes zero][compressedData]` and feeding to an LZMA_ALONE decoder.

**`github.com/ulikunitz/xz/lzma` reads LZMA_ALONE natively** via `lzma.NewReader`. No native deps.

**Chunk URL:** `{urlBase}/depot/{depotID}/chunk/{chunkSha1Lower}{token}` where `urlBase = (https_support=='mandatory' ? 'https://' : 'http://') + Host`. Token is empty in practice.

**Manifest URL:** `{urlBase}/depot/{depotID}/manifest/{manifestID}/5/{requestCode}{token}`. The `5` is a literal manifest version. **Irrelevant for us** — we get manifests from ryuu.lol / GitHub mirrors, not from Steam CDN, because anonymous `GetManifestRequestCode` is AccessDenied.

**Steam symmetric decryption** (used for both filename decrypt and chunk decrypt):

1. First 16 bytes of ciphertext = AES-256-**ECB** encrypted IV. Decrypt with depot key, no padding → 16-byte IV.
2. Remaining bytes = AES-256-**CBC**/PKCS7 with that IV and the same key.
3. Key is always 32 bytes (depot key). No HMAC mode here.

Go: `crypto/aes.NewCipher(key)` → `.Decrypt(ivPlain, ciphertext[:16])` for step 1, then `cipher.NewCBCDecrypter(block, ivPlain).CryptBlocks(dst, ciphertext[16:])` + PKCS7 unpad for step 2.

**SHA1 on chunks:** computed on the DECRYPTED + DECOMPRESSED bytes, compared to `chunk.sha` from the manifest. Same SHA is the chunk ID used in the URL. No separate compressed-SHA check.

**File writes:** `FS.open('w')` → `FS.ftruncate(fileSize)` → positional `FS.write(fd, data, 0, len, chunk.offset, cb)`. Go: `os.OpenFile` + `f.Truncate(size)` + per chunk `f.WriteAt(data, int64(chunk.offset))`. Empty files short-circuit (Steam stores all-zero SHA for them).

**Filename decryption** (if `manifest.filenames_encrypted`):

```
for each file:
  ct = base64_decode(file.filename)
  pt = SteamCrypto.symmetricDecrypt(ct, depotKey)   // ECB-IV + CBC, same as chunks
  file.filename = pt[:pt.indexOf(0)].toString('utf8')  // null-terminate
```

**Non-obvious gotchas:**

- Empty files: Steam stores SHA1 of all zeros as `sha_content`. Skip the hash check.
- Manifest parser mutates buffers to hex/decimal strings. In Go, keep them as `[]byte` / `uint64` natively.
- VZip "version" byte is ignored — don't assert.
- node-steam-user retries each chunk up to 5 times, rotating CDN servers via `assignServer(serverIdx)` (splicing from available list). Port this — real transient CDN timeouts happen on our network.
- `downloadChunk` lowercases the chunk SHA1 before the URL. Case-sensitive path.
- Depot keys are normally cached in `depot_key_<app>_<depot>.bin` (4-byte LE unix timestamp + 32-byte AES key, 14-day validity). **We skip this entirely** — our keys come from the .lua file / ryuu.lol bundle, never from Steam.

### Existing Go depot downloader candidate — `Lucino772/envelop`

**Day 1 priority: validate or discard this.**

- Repo: `https://github.com/Lucino772/envelop`
- Last push: 2026-01-19. 0 stars. Looks like a hobbyist game-server installer.
- Relevant package: `pkg/steam/steamcdn/` with:
  - `client.go` — `DownloadManifest` + `DownloadDepotChunk` hitting `depot/manifest/5/:code` and `depot/chunk/:hex`
  - `manifest.go` (~12 KB) — full `DepotManifest` parser with `NewDepotManifest(data, depotKey)` (protobuf parse + filename decrypt)
- `pkg/steam/steamdl/download.go` wires it into a higher-level `SteamDownloadClient.DownloadDepotManifest`.

**What to verify Day 1:**

1. Does `NewDepotManifest(data, depotKey)` correctly parse a known-good manifest binary from ryuu.lol for app 431960?
2. Does `DownloadDepotChunk` handle LZMA/LZMA2/VZip decompression, or just raw chunks? (If not, we bolt on VZip support ourselves — but at least the manifest parser + HTTP shape is saved.)
3. Does it use the same two-stage AES decrypt (ECB IV + CBC body)?
4. What's its dependency tree? Any CGo? Any weird transitive imports?

**If envelop works:** vendor `pkg/steam/steamcdn` into our repo (MIT/Apache check first), adapt its API to our needs, focus remaining effort on orchestration (parallelism, resume, retry, progress UI).

**If envelop is broken or incomplete:** fall back to writing the CDN client from scratch against SteamKit2's `DepotManifest.cs` + `Client.cs` as the spec. ~600 LOC for the manifest parser + another ~400 LOC for the chunk pipeline.

---

## Architecture decisions (made; do not re-litigate)

1. **Go 1.24+**, single static binary, pure Go, no CGo. `go build -ldflags="-w"` (keep `-s` off — see Defender note).
2. **Repo layout**: Go at repo root, TypeScript reference stays in `src/` untouched. No monorepo split, no separate directory — they coexist on the same branch until v0.1.0 ships.
3. **No external Steam depot library beyond paralin/go-steam.** We vendor it (via `go.mod replace` or a subtree) so we can patch `auth.go` for anonymous logon without maintaining a public fork.
4. **Validate `Lucino772/envelop` on Day 1** before committing to writing the CDN pipeline from scratch. Time-box to 4 hours.
5. **Keep the existing `.lua` parser and manifest resolver logic in spirit.** Port them straight across; no behavioral changes. Same CLI surface: `lua-dl parse|probe|download <file.lua|appid> [--depot ID] [--out DIR] [-v]`.
6. **Default output directory** = `./<sanitized game name>/` (same as the TS version's Phase 1 edit). Reuse the `sanitizeFolderName` logic — a direct port.
7. **Resume via a `.lua-dl-state.json` file** inside the output dir. Same shape as the TypeScript `StateCache`: keyed by `depotId_manifestId/filepath` → `{size, sha1, mtime}`. Tolerate missing/corrupt cache.
8. **Retry logic**: 4 attempts per file with exponential backoff (500ms → 1s → 2s → done). Same as the TypeScript version.
9. **Parallelism**: up to 24 concurrent chunk downloads across the pool of CDN servers (matching our TS CONCURRENCY=24 setting). Use a simple `chan struct{}` semaphore + `errgroup`.
10. **LZMA**: `github.com/ulikunitz/xz/lzma` for LZMA_ALONE. No workers/goroutines-per-chunk ceremony — `goroutine` per chunk and Go's runtime handles multi-core automatically. No equivalent of the TS worker pool needed; Go's goroutines ARE real threads via the runtime scheduler.
11. **No Defender mitigations until proven needed.** Ship unsigned. Drop `-s -w` flags (keep `-w` only — see Defender section). Submit to MS false-positive portal per release only if friends report quarantines.
12. **Release pipeline unchanged in spirit.** `PLAN.md`'s Phase 2 release workflow still applies — swap the build step from `bun run build:exe` to `go build`. Everything downstream (Discord bot, `.bat` template, `%LOCALAPPDATA%\lua-dl\lua-dl-<version>.exe` cache, auto-update via GitHub API) is unchanged.

---

## Target repo layout (after port)

```
lua-dl/
├── go.mod                      module: github.com/hoangvu12/lua-dl
├── go.sum
├── main.go                     entry: flag parsing, subcommand dispatch
├── internal/
│   ├── lua/                    parser + watermark strip (port of src/lua.ts)
│   │   └── lua.go
│   ├── ryuu/                   ryuu.lol endpoints + STORED-zip parser + per-appid cache
│   │   └── ryuu.go
│   ├── resolver/               ryuu-first + GH mirror race fallback for lua and .manifest
│   │   └── resolver.go
│   ├── steam/                  anon logon + PICS wrapper on top of paralin/go-steam
│   │   ├── client.go           (fork of paralin auth.go's LogOn; anon path)
│   │   └── pics.go             (PICS request + response assembly + VDF extraction)
│   ├── vdf/                    binary KV parser (targeted: extract name + depot manifest ids)
│   │   └── binary.go
│   ├── manifest/               .manifest binary format (magic sections + protobuf + decrypt)
│   │   └── manifest.go
│   ├── cdn/                    content-server list + chunk HTTP + AES + VZip + write
│   │   ├── servers.go
│   │   ├── chunk.go
│   │   ├── vzip.go             LZMA_ALONE envelope unwrap via ulikunitz/xz/lzma
│   │   └── download.go         per-file orchestration: parallelism, resume, retry
│   ├── sanitize/               (port of src/sanitize.ts)
│   │   └── sanitize.go
│   ├── state/                  StateCache JSON, same shape as TS
│   │   └── state.go
│   └── verbose/                VERBOSE flag + vlog + statusLine
│       └── verbose.go
├── third_party/
│   └── go-steam/               vendored + patched paralin/go-steam (optional, see decisions)
├── src/                        ← existing TypeScript reference, kept untouched
├── CONTEXT.md                  existing, pipeline handoff doc (TS-focused; still accurate for the algorithms)
├── PLAN.md                     existing, Bun-based release/bot/bat plan — phases 2-8 still apply
├── PLAN-GO.md                  this file
├── README.md                   existing, needs a small update after port
├── .github/
│   └── workflows/
│       └── release.yml         NEW or UPDATED: go build on tag push
└── (node_modules/, out/, etc. gitignored)
```

---

## Open confirmations required before execution

Ask the user explicitly before Phase G0:

1. **Keep the TypeScript source in `src/`** alongside Go? (Assumption: yes, as reference. Otherwise confirm they want it moved to `ts-reference/` or deleted.)
2. **Repo name** still `hoangvu12/lua-dl`? (Original PLAN.md assumption, still holds unless user changed their mind.)
3. **Vendoring paralin/go-steam**: fine to include a patched subtree at `third_party/go-steam/`, or prefer a public fork at `github.com/hoangvu12/go-steam`? (Subtree is simpler for a solo project and keeps the patch auditable in one repo.)
4. **Commit strategy**: one big commit for the Go rewrite, or split by internal package? (Solo project → one commit per package is clean but a single "go: rewrite CLI" commit is fine too.)

---

## Phase G0 — Prerequisites (local, no network)

Time box: 30 min.

1. Confirm `go version` >= 1.22 (1.24 preferred — paralin requires 1.24 for `go.mod` parse).
2. `go env GOOS GOARCH` — should be `windows amd64` on the dev machine.
3. `git status` — capture current uncommitted state. Do NOT commit or discard anything.
4. Read `CONTEXT.md` sections "The pipeline", "Sources", "Critical tricks". These describe the end-to-end flow and the non-obvious decisions.
5. Read the three node-steam-user files on disk to confirm the byte-level notes in this plan match the actual source:
   - `node_modules/steam-user/components/cdn.js`
   - `node_modules/steam-user/components/cdn_compression.js`
   - `node_modules/steam-user/components/content_manifest.js`

---

## Phase G1 — Validate `Lucino772/envelop` (Day 1, 4h time-box)

Goal: prove or disprove that envelop's `steamcdn` package can parse a real manifest and download a real chunk end-to-end. If it works, we vendor it. If not, we write from scratch.

1. Clone to a scratch directory outside our repo: `git clone https://github.com/Lucino772/envelop /tmp/envelop`.
2. Inspect `pkg/steam/steamcdn/client.go`, `pkg/steam/steamcdn/manifest.go`, `pkg/steam/steamdl/download.go`. Confirm the claims: `DownloadManifest`, `DownloadDepotChunk`, `NewDepotManifest(data, depotKey)`.
3. Check the license — MIT/Apache OK, GPL is a no-go for a solo-author private-friendly release.
4. Check dependencies via `go mod graph`. Any CGo? Any weird transitive imports?
5. Write a tiny test harness (scratch Go file) that:
   - Reads a known-good manifest from `./Wallpaper Engine/.lua-dl-state.json` (or re-fetch via the existing TS CLI with `-v` to capture a raw `{depotId}_{manifestId}.manifest` binary from ryuu).
   - Parses it with `NewDepotManifest(data, depotKeyBytes)`.
   - Prints the file list. Compare against the TS parser output for the same depot.
6. If manifest parses cleanly: write a second test harness that calls `DownloadDepotChunk(serverHost, depotId, chunkSha1, depotKey)` for a small file's first chunk. Compare decrypted+decompressed bytes against the known SHA1 from the manifest.
7. If both checks pass: **envelop is good**, decision made, vendor it.
8. If either fails: debug-briefly (<1h), then fall back to Phase G3-alt: write from scratch against SteamKit2 as spec.

Deliverable: a `PHASE-G1-FINDINGS.md` scratch note (can be deleted later) with verdict + paths to vendor / patches needed / any broken behavior discovered.

---

## Phase G2 — Go module skeleton + anon logon + PICS

Time box: 1-2 days.

### G2.1 — `go.mod` and vendoring

```bash
go mod init github.com/hoangvu12/lua-dl
go get github.com/paralin/go-steam@latest
go get github.com/ulikunitz/xz@latest
go get google.golang.org/protobuf@latest
```

If vendoring paralin/go-steam to patch `auth.go`:

```bash
# option A: replace directive + local subtree
mkdir -p third_party/go-steam
git -C third_party/go-steam init  # or: cp -r /tmp/go-steam .
# in go.mod:
replace github.com/paralin/go-steam => ./third_party/go-steam
```

### G2.2 — `internal/steam/client.go`: anon logon

Patch paralin/go-steam's `auth.go` to add an `(*Auth).LogOnAnonymous()` method, OR write a bypass in our `internal/steam/client.go` that constructs and writes the `CMsgClientLogon` directly. Recommended patch (in `third_party/go-steam/auth.go`):

```go
// LogOnAnonymous performs an anonymous logon. No credentials required.
func (a *Auth) LogOnAnonymous() {
    a.client.steamId = SteamId(steamid.NewIdAdv(0, 1,
        int32(steamlang.EUniverse_Public),
        steamlang.EAccountType_AnonUser))

    logon := &protobuf.CMsgClientLogon{
        ProtocolVersion: proto.Uint32(steamlang.MsgClientLogon_CurrentProtocol),
    }
    name := "anonymous"
    logon.AccountName = &name

    msg := protocol.NewClientMsgProtobuf(steamlang.EMsg_ClientLogon, logon)
    a.client.Write(msg)
}
```

Note: `client.steamId` is a private field. The patch needs to be inside the `steam` package to access it. This is why the fork/vendor is the cleanest path — surgical and reviewable.

### G2.3 — `internal/steam/client.go`: event loop + anon login

```go
func AnonymousLogin(ctx context.Context) (*steam.Client, error) {
    steam.InitializeSteamDirectory()  // fresh CM list
    client := steam.NewClient()
    client.Connect()

    loginDone := make(chan error, 1)
    go func() {
        for ev := range client.Events() {
            switch e := ev.(type) {
            case *steam.ConnectedEvent:
                client.Auth.LogOnAnonymous()
            case *steam.LoggedOnEvent:
                if e.Result == steamlang.EResult_OK {
                    loginDone <- nil
                } else {
                    loginDone <- fmt.Errorf("logon failed: %v", e.Result)
                }
            case steam.FatalErrorEvent:
                loginDone <- fmt.Errorf("fatal: %v", e)
            }
        }
    }()

    select {
    case err := <-loginDone:
        return client, err
    case <-time.After(20 * time.Second):
        return nil, errors.New("login timed out after 20s")
    case <-ctx.Done():
        return nil, ctx.Err()
    }
}
```

### G2.4 — `internal/steam/pics.go`: PICS wrapper

```go
type AppInfo struct {
    Name   string
    Depots []DepotInfo
}
type DepotInfo struct {
    DepotID    uint32
    Name       string
    ManifestID uint64  // 0 if missing
    MaxSize    uint64  // 0 if missing
}

func GetAppInfo(client *steam.Client, appID uint32) (*AppInfo, error) {
    // 1. Send CMsgClientPICSProductInfoRequest with Apps=[{Appid: appID}]
    // 2. Register a packet handler for EMsg_ClientPICSProductInfoResponse
    // 3. Loop: accumulate fragmented responses until GetResponsePending() == false
    // 4. Collect Apps[0].Buffer (binary VDF)
    // 5. Pass buffer to internal/vdf.Parse
    // 6. Extract common.name and depots.*.manifests.public.gid
}
```

Reference: SteamKit2's `SteamApps.PICSGetProductInfo` for the fragmentation loop.

### G2.5 — `internal/vdf/binary.go`: binary KV parser

```go
// TryParse reads a binary VDF KeyValues blob and returns a generic tree.
// Type tags:
//   0x00 object-open  (key follows, then children, then 0x08)
//   0x01 string       (null-terminated value)
//   0x02 int32        (4 bytes LE)
//   0x07 uint64       (8 bytes LE)  [needed for manifestId]
//   0x08 object-close
func Parse(data []byte) (*Node, error) { ... }

// Targeted helpers we actually need:
func (n *Node) GetString(path ...string) (string, bool)
func (n *Node) GetUint64(path ...string) (uint64, bool)
```

Only ~100 LOC. Write it focused on just the three fields we need: `common.name`, `depots.<depotId>.name`, `depots.<depotId>.manifests.public.gid`.

Add a test fixture: hex-dump the `Buffer` from a live PICS call captured via the existing TS CLI, drop into `internal/vdf/testdata/431960.bin`. Unit test parses it and asserts `common.name == "Wallpaper Engine"` and the right depot IDs.

### G2.6 — `main.go` + `internal/verbose` + tiny `probe` subcommand

Minimum viable CLI: `lua-dl probe 431960` → anon login → PICS → print depots + name. Validates the entire Phase G2 stack before touching any downloading code.

**Phase G2 deliverable:** `lua-dl probe 431960` prints:

```
Game: Wallpaper Engine
Depots:
  431961  name="Wallpaper Engine Content"   manifest=<gid>
  431966  name="Localization"               manifest=<gid>
  ...
```

and exits cleanly.

---

## Phase G3 — Port the CDN download pipeline

Time box: 2-4 days (best case envelop works; worst case from scratch).

### Branch A — envelop works

1. Copy `pkg/steam/steamcdn/` into `internal/cdn/` or `third_party/steamcdn/`.
2. Adapt APIs to match our needs — we pass depot keys in, not out-of-Steam-session.
3. Wire it into a `Download(appID, depotID, manifestID, depotKey, outDir) error` function in `internal/cdn/download.go` that:
   - Calls `GetContentServers(cellID)` → list of `{host, vhost, httpsSupport}`
   - Fetches the manifest binary from **our resolver** (ryuu.lol / GH mirrors, NOT from Steam CDN), not from `DownloadManifest`. Envelop's `DownloadManifest` is irrelevant to us — we already have this code in the TS version, port it straight.
   - Parses the manifest via envelop's `NewDepotManifest(data, depotKey)`
   - For each file, spawns chunk goroutines (semaphore-limited, ~24 concurrent)
   - Each goroutine: pick a random server → fetch chunk via envelop's `DownloadDepotChunk` (or our own if needed) → verify SHA1 → write to file at offset
   - Retries per chunk up to 5 times, rotating servers
   - Updates `StateCache` on success
4. If envelop's chunk decompression doesn't support VZip, write `internal/cdn/vzip.go` using `ulikunitz/xz/lzma` and wire it in between envelop's chunk fetch and the SHA1 verify step.

### Branch B — envelop is broken, write from scratch

Everything in `internal/manifest/` and `internal/cdn/` written by hand:

1. `internal/manifest/manifest.go` — parse the concatenated magic/length/protobuf sections. ~150 LOC.
2. `internal/manifest/crypto.go` — Steam symmetric decrypt (ECB-IV + CBC body) + filename decrypt. ~60 LOC.
3. `internal/cdn/servers.go` — HTTP GET the IContentServerDirectoryService endpoint, parse JSON, filter + uniform random pick. ~80 LOC.
4. `internal/cdn/chunk.go` — HTTP GET the chunk URL, apply symmetric decrypt, run VZip envelope unwrap, SHA1 verify. ~150 LOC.
5. `internal/cdn/vzip.go` — VZip envelope unwrap + LZMA_ALONE decompress via `ulikunitz/xz/lzma`. ~60 LOC.
6. `internal/cdn/download.go` — per-file orchestration: truncate, chunk goroutines, resume, retry, progress. ~200 LOC.

Total ~700 LOC for Branch B. Use SteamKit2's `DepotManifest.cs` and `Client.cs` as the spec — they're the canonical reference.

### Both branches converge on

- **Concurrency:** `errgroup` with a 24-slot semaphore for chunk fetches across the entire depot (not per-file). Simple and effective.
- **Progress:** reuse the statusLine approach from `src/verbose.ts` — one TTY line updated every N chunks or every 100ms, whichever is sooner.
- **Resume:** on start, read `StateCache`. For each file: if state has an entry AND the on-disk file matches size + SHA1, skip. If only size matches, re-hash and cache the result. SHA1 is the arbitration.
- **Retry:** max 4 attempts per file, exponential backoff 500ms → 1s → 2s → done.

---

## Phase G4 — Port `.lua` parser, resolver, ryuu source

Time box: 1 day. These are all pure logic ports from TypeScript to Go, no Steam protocol involvement.

### G4.1 — `internal/lua/lua.go`

Port `src/lua.ts`:
- Strip zero-width watermark characters: `[\u200B-\u200D\uFEFF\u2060]`.
- Regex for `addappid(<id>)` / `addappid(<id>, <locked>, "<key>")` with optional `-- label` trailer.
- Regex for `setManifestid(<depotId>, "<manifestId>")`.
- Output a `Lua{ AppID uint32, Depots []DepotEntry }` struct.

Reuse the **exact** regexes from the TS version (watch the `[ \t]*(?:--[ \t]*([^\n]*))?` comment-label gotcha — don't "simplify" it to `\s*`).

### G4.2 — `internal/ryuu/ryuu.go`

Port `src/ryuu-source.ts`:
- `FetchBundle(appID uint32) (*Bundle, error)` — cached in a `sync.Map[uint32]*Bundle` (or `singleflight`).
- Endpoint 1: `GET https://generator.ryuu.lol/resellerlua?appid=N&auth_code=RYUUMANIFEST-setapikeyforsteamtoolsversion9700` → text/plain lua.
- Endpoint 2: `GET https://generator.ryuu.lol/secure_download?appid=N&auth_code=...` → STORED zip.
- Port the ~40-line EOCD/central-directory walker from `src/ryuu-source.ts`. Or: use `archive/zip` from stdlib — it handles STORED just fine.

### G4.3 — `internal/resolver/resolver.go`

Port `src/manifest-resolver.ts`:
- `ResolveLua(appID uint32) (*Lua, string, error)` — try ryuu first (per-app cached), then race GH mirrors.
- `ResolveManifest(appID, depotID uint32, manifestID uint64) ([]byte, string, error)` — try ryuu bundle first, then race GH mirrors for `{depotId}_{manifestId}.manifest`.
- Validate manifest magic `0x71F617D0` LE before returning.
- Mirror list (ordered by freshness, from `src/manifest-resolver.ts`):
  1. `SPIN0ZAi/SB_manifest_DB`
  2. `tymolu233/ManifestAutoUpdate-fix`
  3. `BlankTMing/ManifestAutoUpdate`
  4. `Auiowu/ManifestAutoUpdate`
  5. `pjy612/SteamManifestCache`

### G4.4 — `internal/sanitize/sanitize.go`

Port `src/sanitize.ts`. Tiny (~20 LOC). Windows forbidden chars + reserved names + length cap.

### G4.5 — `internal/state/state.go`

Port `src/state.ts`. JSON file, lazy mkdir-on-flush, tolerant of missing/corrupt file. Same key shape `depotId_manifestId/filepath`, same entry shape `{size, sha1, mtime}`.

---

## Phase G5 — CLI + end-to-end test

Time box: 1 day.

### G5.1 — `main.go`

Subcommand dispatch: `parse`, `probe`, `download`. Flag parsing: stdlib `flag` or `github.com/urfave/cli/v3` (prefer stdlib for a smaller binary).

Same CLI surface as the TS version:

```
lua-dl parse    <file.lua|appid>
lua-dl probe    <file.lua|appid>
lua-dl download <file.lua|appid> [--depot ID] [--out DIR] [-v]
```

Download default outDir = `./<sanitizeFolderName(appInfo.Name)>/`.

### G5.2 — End-to-end smoke tests

```bash
# light
./lua-dl download 431960 --depot 431966
# expect: ~75 files in ./Wallpaper Engine/locale/, ~8 MB, under 10 seconds

# full
./lua-dl download 431960
# expect: WE content depot 431961 (660 MB) + localization (8 MB) + DLC 1790230
# under 10 minutes on the dev connection (Go single-threaded LZMA is fine)

# by-file (legacy)
./lua-dl download "/c/Users/HP MEDIA/Downloads/431960.lua" --out ./out/WE-by-file
```

### G5.3 — Go vs TS parity check

For the same appid + depot, compare:
- File count matches
- Every file's final SHA1 matches what the TS cache has (`out/.lua-dl-state.json` from a prior TS run)
- Output folder structure matches

If mismatch: bisect — first check manifest file listing, then per-file chunk lists, then decrypted chunk bytes.

---

## Phase G6 — Build + release (swap into PLAN.md phases 2-8)

From this point on, `PLAN.md`'s phases 2 onward apply almost verbatim. Only the build step changes.

### G6.1 — `build` script

Replace `package.json:"build:exe"` with a cross-platform Go build. Either keep it in `package.json` for consistency or write a Makefile. For Windows:

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
    go build -trimpath -ldflags="-w -X main.Version=$(git describe --tags --always)" \
    -o lua-dl.exe .
```

**Why `-w` but NOT `-s`:** per the Defender research, stripping symbols (`-s`) correlates with higher false-positive rates. `-w` (disables DWARF debug info) is fine, still saves ~30% size. Don't use UPX — it makes Defender FPs dramatically worse.

Expected binary size: **8-15 MB**. If it's >25 MB, investigate (stray imports, debug info leak).

### G6.2 — `.github/workflows/release.yml`

Replace the Bun-based workflow from `PLAN.md` Phase 2.1 with a Go build:

```yaml
name: Release
on:
  push:
    tags: ['v*']
permissions:
  contents: write
jobs:
  build:
    runs-on: windows-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.24'
          cache: true
      - name: Build Windows exe
        shell: bash
        run: |
          CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
            go build -trimpath -ldflags="-w -X main.Version=${GITHUB_REF_NAME}" \
            -o lua-dl.exe .
      - name: Upload release asset
        uses: softprops/action-gh-release@v2
        with:
          files: lua-dl.exe
          generate_release_notes: true
          fail_on_unmatched_files: true
```

### G6.3 — Phases 3-8 from `PLAN.md` apply unchanged

- **Phase 3**: `gh repo create hoangvu12/lua-dl --public` (if not already done)
- **Phase 4**: tag `v0.1.0`, watch CI, verify `lua-dl.exe` downloadable from the release
- **Phase 4.4**: manual download-and-run on a clean temp folder. Watch for Defender. (Per user preference, we are NOT preempting this — just verifying.)
- **Phase 5**: Discord bot scaffold under `bot/`, unchanged. The `.bat` template from `PLAN.md` Phase 5.4 works as-is — same `lua-dl.exe` name, same `%LOCALAPPDATA%\lua-dl\lua-dl-<version>.exe` cache, same GitHub Releases URL pattern.
- **Phase 6**: hosting, unchanged.
- **Phase 7**: end-to-end smoke test via Discord, unchanged.
- **Phase 8**: Defender fallback ladder, unchanged.

---

## Gotchas specific to the Go port

1. **`client.steamId` is a private field** in paralin/go-steam. To set it for anon logon you must either patch the `steam` package directly (vendored fork) or use unsafe/reflection. Vendored patch is cleaner.

2. **Paralin's `Auth.LogOn` panics on empty password.** Do NOT call it. Either patch to add `LogOnAnonymous()` or construct + write the raw `CMsgClientLogon` yourself.

3. **PICS response assembly.** Responses are fragmented across multiple `CMsgClientPICSProductInfoResponse` messages. Loop until `GetResponsePending() == false`. SteamKit2's `SteamApps.cs PICSGetProductInfo` shows the pattern.

4. **PICS HTTP fallback branch.** For large payloads, Steam returns `http_host` + `http_min_size` instead of inlining the buffer. Rare for single-app requests but must not crash. Fetch from `https://{http_host}/appinfo/v1/{appid}/<hash>` (confirm exact shape in SteamKit2). Defer until you hit it in testing.

5. **Binary VDF type tags we actually care about:**
   ```
   0x00  ObjectOpen   → recurse
   0x01  String       → null-terminated ASCII/UTF-8
   0x02  Int32        → 4 bytes LE
   0x07  Uint64       → 8 bytes LE   (this is how manifest GIDs are stored)
   0x08  ObjectClose
   ```
   `manifests.public.gid` is a string OR a uint64 depending on Steam's mood. Handle both.

6. **VZip version byte at offset 3 is ignored.** Don't assert on it.

7. **LZMA_ALONE uncompressed-size field must be 8 bytes, not 5.** `ulikunitz/xz/lzma` expects `[properties(5)][size(8 LE)][data]`. The compressed data from VZip is missing the size field entirely — synthesize it from the VZip footer's 4-byte decompressed-size and pad the high 4 bytes with zeros.

8. **Empty files**: Steam stores SHA1 of all zeros as `sha_content`. Skip the check for `fileSize == 0`. Same for chunks that happen to be empty.

9. **Chunk SHA1 is the URL path** — lowercase hex, and the SHA is over the DECRYPTED + DECOMPRESSED bytes (plaintext). Case-sensitive on the CDN.

10. **CRC32 inside the VZip footer is zlib-style CRC32** (`hash/crc32.ChecksumIEEE`), NOT CRC32C. The TS version checks this; the Go port should too.

11. **Steam symmetric decrypt is two-stage**: AES-256-ECB on the first 16 bytes (no padding) to get the IV, then AES-256-CBC/PKCS7 on the rest. Both stages use the same 32-byte depot key. Go: `aes.NewCipher(key).Decrypt(iv, ct[:16])` + `cipher.NewCBCDecrypter(block, iv).CryptBlocks(dst, ct[16:])` + PKCS7 unpad.

12. **TLS on CDN hosts**: check `https_support` on each content server. If `"mandatory"`, use `https://`. Otherwise `http://`. Don't force HTTPS for all — some servers break.

13. **`go build` on Windows with `CGO_ENABLED=0`** produces a static binary. If any dependency pulls in CGo (check with `go list -f '{{.ImportPath}} {{.CgoFiles}}' ./...`), replace or vendor it.

14. **`ulikunitz/xz/lzma`** reads a raw LZMA_ALONE stream via `lzma.NewReader`. It expects the 5-byte props header + 8-byte size field + payload. Our VZip envelope gives us props + compressed bytes; we manually build the 8-byte size.

15. **Go's default HTTP client has no keepalive timeout override for our use case.** Tune `http.Transport` explicitly:
    ```go
    tr := &http.Transport{
        MaxIdleConns:        100,
        MaxIdleConnsPerHost: 24,
        IdleConnTimeout:     90 * time.Second,
        TLSHandshakeTimeout: 10 * time.Second,
        ExpectContinueTimeout: 1 * time.Second,
        DisableCompression:  true, // we handle VZip ourselves
    }
    ```

16. **Goroutines ≠ JS workers.** A goroutine running `lzma.Decode` blocks on CPU just like any other function — but Go's runtime schedules across all OS threads automatically, so many goroutines doing LZMA in parallel naturally use all cores. No pool, no `sync.WaitGroup` ceremony beyond `errgroup`. The whole TS worker-pool story becomes two lines of Go.

17. **Don't call `InitializeSteamDirectory()` in a test loop** — it hits the Steam Web API. Cache the result once per process or mock it in tests.

18. **Windows Defender `Wacatac.B!ml` false positives** are a real risk for Go binaries doing crypto + network + file writes. Specifically: drop `-s` from ldflags, keep `-w`. Avoid UPX entirely. If a friend reports a quarantine, submit to MS false-positive portal.

---

## Dead ends — do NOT retry

Copied from CONTEXT.md + extended with Go-era learnings:

1. **`lzma-native` npm package** — throws `LZMA_DATA_ERROR` on real Steam chunks. Irrelevant now that we're in Go, but: don't install any JS LZMA package.
2. **WebSocket-to-Steam-CM** — blocked on VN network. Always TCP. Paralin's lib is TCP-only anyway.
3. **`bun --compile`** — 112 MB output, fights us on worker threads and CJS protobuf requires, Steam login times out in the compiled exe. This is why we're in Go.
4. **`GetManifestRequestCode` on anonymous accounts** — AccessDenied on paid apps. Bypass via ryuu/GH mirrors feeding `.manifest` binaries to our parser.
5. **openlua.cloud automation** — Cloudflare Turnstile, single-use tokens. Ignore.
6. **UPX compression of the Go binary** — dramatically increases Defender false-positive rate. Don't.
7. **`-s` ldflag (strip symbols)** — correlated with higher Defender FP rates per research. Keep `-w` only.
8. **Weighted/cell-aware content server selection** — node-steam-user uses uniform random. Matches SteamKit2's default. Don't overengineer this.

---

## User preferences / constraints (unchanged from `PLAN.md`/`CONTEXT.md`)

- **No credentials, no captcha, no headless browsers.** Rejected.
- **CLI over userscript.** Rejected userscript for the download side.
- **Never `bun dev`** — lint/typecheck only. (No longer relevant for Go work, but still applies to the TS `src/` reference.)
- **Terse responses.**
- **Research before guessing.** The Bun `--compile` rabbit hole is the exact counter-example — we thrashed for an hour before stopping to research. Don't repeat.
- **`gh` CLI is available and authenticated as `hoangvu12`.** `gh auth status` should show `repo, workflow, read:org, gist` scopes.
- **Use Explore/Plan subagents when a task spans multiple files or needs research.** Don't hand-grep through 50 files when a subagent can bring back a summary.
- **Confirm before destructive git actions.** No `git reset --hard`, no force-pushing master, no deletion of the TS `src/` directory without explicit OK.

---

## Execution checklist (for the next agent — copy into TaskCreate)

- [ ] **G0.1** Read CONTEXT.md sections: pipeline, sources, critical tricks
- [ ] **G0.2** Confirm `go version`, `go env GOOS GOARCH`, `git status`
- [ ] **G0.3** Open user confirmations: TS in `src/` stays? vendor paralin? commit strategy?
- [ ] **G1.1** Clone `Lucino772/envelop` to /tmp, inspect `pkg/steam/steamcdn`
- [ ] **G1.2** Check envelop license + dep graph
- [ ] **G1.3** Capture a known-good manifest from a live TS `-v` run (depot 431966 is fastest)
- [ ] **G1.4** Write scratch harness: `NewDepotManifest(data, key)` → compare file list to TS
- [ ] **G1.5** Write scratch harness: `DownloadDepotChunk` → compare decrypted bytes by SHA1
- [ ] **G1.6** Decide vendor-envelop vs write-from-scratch. Write PHASE-G1-FINDINGS.md
- [ ] **G2.1** `go mod init`, add deps, vendor paralin if chosen
- [ ] **G2.2** Patch paralin `auth.go` for `LogOnAnonymous()` (or bypass)
- [ ] **G2.3** `internal/steam/client.go` — anon login + event loop
- [ ] **G2.4** `internal/vdf/binary.go` + testdata + unit test
- [ ] **G2.5** `internal/steam/pics.go` — send PICS + assemble fragmented response + VDF extract
- [ ] **G2.6** `main.go probe` subcommand — end-to-end anon login + PICS + print
- [ ] **G2.7** Smoke test: `./lua-dl probe 431960` prints "Wallpaper Engine" + depot list
- [ ] **G3** CDN pipeline — Branch A (envelop) or Branch B (from scratch)
- [ ] **G3.test** Compare Go-downloaded bytes to TS-downloaded bytes on depot 431966
- [ ] **G4.1** `internal/lua/lua.go` + regex tests
- [ ] **G4.2** `internal/ryuu/ryuu.go` + per-appid cache + STORED zip parser
- [ ] **G4.3** `internal/resolver/resolver.go` + mirror race + magic validation
- [ ] **G4.4** `internal/sanitize/sanitize.go`
- [ ] **G4.5** `internal/state/state.go`
- [ ] **G5.1** `main.go` full CLI with all three subcommands and `--out` default
- [ ] **G5.2** Smoke: `./lua-dl download 431960 --depot 431966`
- [ ] **G5.3** Full smoke: `./lua-dl download 431960` — WE content + localization + DLC
- [ ] **G5.4** Parity check: Go vs TS SHA1 on every file
- [ ] **G6.1** Build script / Makefile with proper `-ldflags` + trimpath
- [ ] **G6.2** Measure final exe size (expect 8-15 MB)
- [ ] **G6.3** Write/update `.github/workflows/release.yml` for Go build
- [ ] **G6.4** Return to `PLAN.md` Phase 3 onward (create repo, tag, release, bot, etc.)
- [ ] **Final** Get user confirmation before committing the Go rewrite

---

## Things NOT to do

- Don't delete `src/` (the TypeScript reference). It's our oracle.
- Don't try to make the Bun `--compile` path work. That ship sailed.
- Don't install `lzma-native` or any other JS LZMA package. (Doesn't matter for Go work but kept for continuity.)
- Don't use `-s` in the Go ldflags — correlates with Defender FPs.
- Don't use UPX — same reason, worse.
- Don't fetch Steam depot keys through `GetDepotDecryptionKey` over the wire. We use the keys from the .lua file. Our anonymous account cannot get them otherwise.
- Don't add weighted/cell-aware CDN server selection as a "while we're at it" improvement. Uniform random matches parity; premature.
- Don't port `file-manager`, depot-key-caching, web-API endpoint, VDF text parser, or anything else from node-steam-user that isn't on the critical path. Scope creep kills rewrites.
- Don't force-push `master`. Don't `git reset --hard`. Don't commit without user OK.
- Don't commit `lua-dl.exe` or `node_modules/` or `out/` — `.gitignore` handles them but worth re-checking after the new files land.
- Don't pay for a code-signing cert on spec. Ship unsigned, observe real Defender behavior, decide later.
- Don't use `github.com/Philipp15b/go-steam` (the original, unmaintained). Use `github.com/paralin/go-steam` — it's the active fork with regenerated protobufs.
- Don't spawn worker threads or per-chunk goroutine pools manually. `errgroup` + a `chan struct{}` semaphore is enough; Go's runtime handles parallelism automatically.
- Don't over-abstract the CLI. Three subcommands, stdlib `flag` package, done. No cobra, no urfave/cli — they bloat the binary and add no value here.

---

## Key references (keep these tabs open)

- **Research reports from 2026-04-13** (pre-compact): paralin/go-steam deep-dive, node-steam-user byte-level spec, Defender + refs report. All three are in the conversation transcript that was cleared; this file is the distilled version.
- **SteamKit2 DepotManifest.cs**: `https://github.com/SteamRE/SteamKit/blob/f5caf601556db0327e9f55e0675bec3ee0cec1ac/SteamKit2/SteamKit2/Types/DepotManifest.cs`
- **SteamKit2 CDN Client.cs**: `https://github.com/SteamRE/SteamKit/blob/f5caf601556db0327e9f55e0675bec3ee0cec1ac/SteamKit2/SteamKit2/Steam/CDN/Client.cs`
- **DepotDownloader ContentDownloader.cs**: `https://github.com/SteamRE/DepotDownloader/blob/4aac37401cff628d3938b50b5e6ab2d6fc93c69f/DepotDownloader/ContentDownloader.cs`
- **Lucino772/envelop**: `https://github.com/Lucino772/envelop` — the candidate for vendoring
- **paralin/go-steam**: `https://github.com/paralin/go-steam`
- **ulikunitz/xz**: `https://github.com/ulikunitz/xz` — LZMA_ALONE decoder
- **node-steam-user on disk**: `node_modules/steam-user/components/{cdn.js,cdn_compression.js,content_manifest.js}` + `node_modules/@doctormckay/steam-crypto/index.js`
- **Existing TypeScript reference**: `src/download.ts`, `src/cdn-patch.ts`, `src/lzma-worker.ts`, `src/manifest-resolver.ts`, `src/ryuu-source.ts`

Good luck.
