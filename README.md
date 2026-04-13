# lua-dl

Download Steam games without a Steam account or captcha. Give it a Steam App ID, get the game.

## Quick start (non-technical users)

Join the Discord bot server, run `/dl <appid>`, download the `.bat` it replies with, drop it in an empty folder, double-click. Done.

Example: `/dl 431960` → downloads Wallpaper Engine.

**First run warning**: Windows Defender / SmartScreen may show "Windows protected your PC". Click **More info** → **Run anyway**. This is a one-time click per machine — the binary is unsigned because the project is a hobby.

## Quick start (CLI)

Grab the latest `lua-dl.exe` from [Releases](https://github.com/hoangvu12/lua-dl/releases), then:

```powershell
./lua-dl.exe download 431960
```

The game lands in `./Wallpaper Engine/` next to the exe. Resume is automatic — re-run the same command after interrupting and it skips finished files.

Useful flags:

- `--depot N` — only download one depot (smoke tests)
- `--out DIR` — pick a target directory (otherwise: sanitized game name)
- `-v` — verbose logging

Other subcommands: `parse <appid>` prints the lua without downloading; `probe <appid>` parses + queries live Steam for manifest IDs.

## Build from source

```bash
go build -trimpath -ldflags="-s -w" -o lua-dl.exe ./cmd/lua-dl
```

Pure Go, no CGo, cross-compiles everywhere. Needs Go 1.24+.

## How it works

Built on [Lucino772/envelop](https://github.com/Lucino772/envelop) for the Steam CM protocol (anonymous login, PICS, VDF, manifest parser). A thin shim bypasses envelop's paid-app access check and its chunk pipeline — which only speaks VZip + PK-zip — with a replacement that also handles Steam's zstd-wrapped chunks used for compressible assets.

Manifests come from [ryuu.lol](https://generator.ryuu.lol) first, then a race against a rotating list of ManifestAutoUpdate-style GitHub mirrors. Depot decryption keys come from the lua script the source hands out; we never ask Steam for a key. Chunks are fetched in parallel (24 in flight), decrypted with AES ECB-IV + CBC, decompressed, SHA1-verified against the manifest, and written positionally via `WriteAt` into a `.partial` file that is atomically renamed on completion.

See `CONTEXT.md` for TypeScript-era notes, `PLAN-GO.md` for the Go port post-mortem, and `PLAN.md` for the release/bot/bat distribution plan.
