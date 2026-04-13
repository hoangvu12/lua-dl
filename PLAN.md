# PLAN — v0.1.0 release + Discord bot + `.bat` distribution

**You are reading this fresh after a conversation clear. Read this file in full, then `CONTEXT.md` for background on the CLI itself. Together they're enough to execute.**

Written 2026-04-13. The CLI side of lua-dl is done and working (see `CONTEXT.md`). This document covers the next phase: shipping a GitHub release of the CLI as a Windows `.exe`, and wrapping it in a Discord bot + auto-updating `.bat` file so non-technical friends can download a Steam game by typing `/dl <appid>` in Discord.

---

## TL;DR — the end-user flow

1. Friend types `/dl 431960` in a Discord server where the bot lives.
2. Bot replies with a short message + attaches a file called `lua-dl-431960.bat`.
3. Friend downloads the `.bat`, drops it into an empty folder (e.g. `Desktop\Games\`), double-clicks.
4. First run: the `.bat` queries GitHub Releases API for the latest `lua-dl.exe`, caches it in `%LOCALAPPDATA%\lua-dl\lua-dl-<version>.exe`, then runs it.
5. CLI downloads the game into `./<Sanitized Game Name>/` next to the `.bat`. No appid-numbered folder — the friend sees `Wallpaper Engine/` show up, not `431960/`.
6. Subsequent runs use the cached `.exe` unless the GitHub API reports a newer version → then auto-updates.

Zero install, zero bandwidth cost to you, and auto-updating.

---

## Current repo state (verified 2026-04-13, pre-execution)

Working directory: `C:\Users\HP MEDIA\Desktop\nguyenvu\steamtools-test`

```
branch:           master
remote:           (none — local only)
gh auth:          hoangvu12, scopes: repo, workflow, read:org, gist
hoangvu12/lua-dl: does NOT exist yet on GitHub (clear to create)
commits:          2 (POC + CONTEXT.md)
modified files:   CONTEXT.md, src/cli.ts, src/download.ts, src/lua.ts,
                  src/manifest-resolver.ts, src/steam.ts
untracked files:  src/cdn-patch.ts, src/http-patch.ts, src/lzma-worker.ts,
                  src/ryuu-source.ts, src/state.ts, src/verbose.ts
```

**The uncommitted work is the ryuu.lol integration, retry logic, worker-pool LZMA, and download-by-appid mode** — all the stuff described in CONTEXT.md. It MUST be committed before tagging a release. The user has not yet explicitly OK'd the commit — confirm before doing it.

---

## Architecture decisions (made; do not re-litigate)

1. **One public monorepo** at `hoangvu12/lua-dl`, not two split repos.
   - Public is required: GitHub Releases URLs must be fetchable unauthenticated from the `.bat`.
   - Bot source has no secrets (Discord token lives in env vars), so public bot code is fine.
   - Single version bump: bump tag → CI builds exe → done.

2. **Bot + CLI live in the same repo**, under `bot/` subdirectory.
   - Trivially kept in sync; one release covers both.

3. **Discord bot**: standard `discord.js` gateway client (long-lived WebSocket), NOT Cloudflare Workers HTTP interactions.
   - HTTP interactions require ed25519 signature verification, more scaffolding, and CF Workers edge quirks. Not worth the complexity for a hobby bot.
   - Bot process is idle 99.9% of the time; any tiny host works (user's PC, Oracle free tier, Railway).

4. **Auto-update strategy**: `.bat` queries GitHub API for latest release tag on every run. Downloads new `.exe` only if not already cached. Falls back to a baked-in version string if the API call fails.
   - Rate limit (60 req/hr unauthenticated per IP) is not a real concern for friend-group scale.
   - PowerShell one-liner parses the JSON because bat itself can't.

5. **Output folder naming**: CLI defaults to `./<sanitized common.name from Steam PICS>/` when `--out` is not provided. `.bat` does not pass `--out`, so the CLI picks this automatically.
   - Much friendlier than `./431960/`.
   - Collision handling (two games with same name) is deferred — unlikely in practice.

6. **AV/Defender risk is accepted, not preemptively mitigated**.
   - Bun-compiled exes historically get flagged (oven-sh/bun#16981).
   - First-run SmartScreen warning is expected on every friend's machine.
   - Plan: document the click-through in the Discord bot's reply + the README. Submit to MSRC false-positive portal if Defender eats the binary. Code-signing cert only if this becomes a recurring blocker.

7. **Branch**: keep `master` for now. Rename to `main` post-release if wanted (non-blocking).

---

## Target repo layout

```
lua-dl/ (public, hoangvu12/lua-dl)
├── src/                            ← existing CLI, unchanged except for edits below
│   ├── cli.ts                      edit: use getAppInfo + default outDir
│   ├── steam.ts                    edit: add getAppInfo (name + depots)
│   ├── sanitize.ts                 NEW: Windows filename sanitizer
│   ├── download.ts                 unchanged
│   ├── manifest-resolver.ts        unchanged
│   ├── ryuu-source.ts              unchanged
│   ├── lua.ts                      unchanged
│   ├── state.ts                    unchanged
│   ├── verbose.ts                  unchanged
│   ├── http-patch.ts               unchanged
│   ├── cdn-patch.ts                unchanged
│   └── lzma-worker.ts              unchanged
├── bot/                            NEW
│   ├── src/
│   │   ├── index.ts                client + slash-command handler
│   │   ├── bat-template.ts         renderBat({appid,version,repo}): string
│   │   └── register-commands.ts    one-shot: registers /dl globally
│   ├── package.json                discord.js + dotenv
│   ├── tsconfig.json
│   ├── .env.example                DISCORD_TOKEN, DISCORD_APP_ID, CLI_VERSION, CLI_REPO
│   └── README.md                   how to run the bot locally
├── .github/
│   └── workflows/
│       └── release.yml             NEW: on v* tag → build + upload exe
├── package.json                    edit: add build:exe script
├── tsconfig.json                   unchanged
├── bun.lock                        existing
├── .gitignore                      edit: add bot/node_modules, bot/.env
├── README.md                       NEW: short usage doc
├── CONTEXT.md                      existing (CLI handoff doc)
└── PLAN.md                         this file
```

---

## Open confirmations required before execution

The user must explicitly OK these BEFORE running Phase 1 commands:

1. **Repo name**: `hoangvu12/lua-dl` — OK, or prefer a different name?
2. **Public visibility** — required for unauthenticated `.bat` downloads. OK?
3. **Permission to commit** the combined changes (existing uncommitted CLI work + the new edits from Phase 1 below) as a **single** commit before pushing. The user's global rule is no auto-commits without explicit ask; this plan requires one.
4. **Branch naming**: keep `master`, or rename to `main` on first push?

---

## Phase 1 — CLI edits (local only, no network yet)

### 1.1 New file: `src/sanitize.ts`

```ts
/**
 * Sanitize a string for use as a Windows folder name.
 *
 * Windows forbids: < > : " | ? * \ / and control chars (0x00-0x1f).
 * Also silently strips trailing dots and spaces from folder names,
 * and reserves names like CON, PRN, AUX, NUL, COM1-9, LPT1-9.
 */
const RESERVED = /^(con|prn|aux|nul|com[1-9]|lpt[1-9])(\..*)?$/i;

export function sanitizeFolderName(name: string): string {
  let s = name
    .replace(/[<>:"|?*\\/\x00-\x1f]/g, "-")
    .replace(/\s+/g, " ")
    .replace(/[. ]+$/g, "")
    .trim();
  if (!s) s = "game";
  if (RESERVED.test(s)) s = `_${s}`;
  // Windows MAX_PATH leaves us comfortable room; cap at 120 chars for sanity.
  if (s.length > 120) s = s.slice(0, 120).trimEnd();
  return s;
}
```

### 1.2 Edit `src/steam.ts`

Add a new `AppInfo` type and a new `getAppInfo` function. Keep `getAppDepots` for backward compat OR replace callers — pick one. Simpler to just replace the one caller in `cli.ts`.

```ts
export interface AppInfo {
  name: string;
  depots: DepotInfo[];
}

export async function getAppInfo(
  client: SteamUser,
  appId: number
): Promise<AppInfo> {
  const info: any = await client.getProductInfo([appId], [], true);
  const app = info.apps[appId];
  if (!app) throw new Error(`No product info returned for app ${appId}`);

  const name: string = app.appinfo?.common?.name ?? `app-${appId}`;
  const rawDepots = app.appinfo?.depots ?? {};
  const depots: DepotInfo[] = [];
  for (const [key, raw] of Object.entries<any>(rawDepots)) {
    if (!/^\d+$/.test(key)) continue;
    depots.push({
      depotId: Number(key),
      name: raw?.name,
      manifestId: raw?.manifests?.public?.gid,
      maxSize: raw?.maxsize ? Number(raw.maxsize) : undefined,
    });
  }
  return { name, depots };
}
```

Keep `getAppDepots` exported as a thin wrapper so `probe` subcommand still works:

```ts
export async function getAppDepots(
  client: SteamUser,
  appId: number
): Promise<DepotInfo[]> {
  return (await getAppInfo(client, appId)).depots;
}
```

### 1.3 Edit `src/cli.ts`

In the `download` subcommand branch, replace the `getAppDepots` call with `getAppInfo`, and default `outDir` to the sanitized name.

Changes (diff-ish sketch — exact lines depend on current file):

```ts
// at top of file imports
import { anonymousLogin, getAppInfo } from "./steam";
import { sanitizeFolderName } from "./sanitize";

// in the `download` branch:
const client = await anonymousLogin();
injectDepotKeys(client, parsed.depots);
const appInfo = await getAppInfo(client, parsed.appId);
const outDir = flag("--out") ?? join(".", sanitizeFolderName(appInfo.name));
const state = new StateCache(join(outDir, ".lua-dl-state.json"));

console.error(`\n== Game: ${appInfo.name} ==`);
console.error(`== Output: ${outDir} ==`);

// use appInfo.depots where liveDepots was used before
const targets = appInfo.depots.filter((d) => { /* ... same logic ... */ });
```

Also ensure the output dir is created before `StateCache` writes to it. Check existing `downloadDepot` — it already calls `mkdirSync(outputDir, {recursive: true})` per depot, but `StateCache` is instantiated earlier with a path inside outDir. Verify: either have the CLI mkdir outDir upfront, or ensure `StateCache` tolerates a missing directory at construction time (it probably doesn't, so mkdir upfront).

### 1.4 Edit `package.json`

Add a build script:

```json
{
  "scripts": {
    "build:exe": "bun build --compile --minify --target=bun-windows-x64 src/cli.ts --outfile=lua-dl.exe"
  }
}
```

Note: bun's `--compile` embeds the Bun runtime (~80MB uncompressed, ~15-25MB after `--minify`). Verify size after first build.

### 1.5 Edit `.gitignore`

Add:
```
bot/node_modules/
bot/.env
lua-dl.exe
*.exe
```

### 1.6 Write minimal `README.md`

Contents (short, friendly):

```markdown
# lua-dl

Download Steam games without a Steam account or captcha. Give it a Steam App ID, get the game.

## Quick start (non-technical users)

Join the Discord bot server, run `/dl <appid>`, download the `.bat` it replies with, drop it in an empty folder, double-click. Done.

Example: `/dl 431960` → downloads Wallpaper Engine.

**First run warning**: Windows Defender / SmartScreen will show "Windows protected your PC". Click **More info** → **Run anyway**. This is a one-time click per machine — the binary is unsigned because the project is a hobby.

## Quick start (CLI)

```bash
bun install
bun run src/cli.ts download 431960 --out ./out/wallpaper-engine
```

Or compile to a single Windows exe:
```bash
bun run build:exe
./lua-dl.exe download 431960
```

## How it works

See `CONTEXT.md` for architecture, source list, throughput notes, and things not to try.
```

### 1.7 Local smoke test

After edits:
```bash
bun run src/cli.ts download 431960
```

Expect: output lands in `./Wallpaper Engine/`. Verify 4-entry lua (ryuu) still works, DLC depot 1790230 still downloads.

Then:
```bash
bun run build:exe
./lua-dl.exe download 431960
```

Verify the compiled exe produces identical output. Note the exe size — report back if > 50 MB after minify (may need a tracking note; current expectation ~15-25 MB).

---

## Phase 2 — GitHub Actions release workflow

### 2.1 Create `.github/workflows/release.yml`

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

      - uses: oven-sh/setup-bun@v2
        with:
          bun-version: latest

      - name: Install dependencies
        run: bun install --frozen-lockfile

      - name: Build Windows exe
        run: bun run build:exe

      - name: Upload release asset
        uses: softprops/action-gh-release@v2
        with:
          files: lua-dl.exe
          generate_release_notes: true
          fail_on_unmatched_files: true
```

Rationale:
- `windows-latest` runner: build natively on Windows to dodge cross-compile edge cases with any win32 syscalls steam-user or our http-patch might use.
- `oven-sh/setup-bun@v2`: official, installs latest stable Bun.
- `--frozen-lockfile`: reproducible installs.
- `softprops/action-gh-release@v2`: de-facto standard for creating releases from tag pushes.

---

## Phase 3 — Push to GitHub

After Phase 1 edits are committed locally:

```bash
# 3.1 Create the public repo
gh repo create hoangvu12/lua-dl \
  --public \
  --source=. \
  --remote=origin \
  --description="Download Steam games from a .lua file or bare appid. Zero credentials." \
  --push

# If --push doesn't work (known flakiness on some gh versions), fall back to:
# gh repo create hoangvu12/lua-dl --public --source=. --remote=origin ...
# git push -u origin master
```

Verify:
```bash
gh repo view hoangvu12/lua-dl --web  # opens in browser
```

---

## Phase 4 — First release

```bash
# 4.1 Tag and push
git tag v0.1.0
git push origin v0.1.0

# 4.2 Watch the action
gh run watch
# (or: gh run list --limit 1)

# 4.3 Verify the release
gh release view v0.1.0
```

Expected asset: `lua-dl.exe` downloadable from
`https://github.com/hoangvu12/lua-dl/releases/download/v0.1.0/lua-dl.exe`

### 4.4 Manual verification (critical — do this before building the bot)

```bash
# Pretend to be a friend:
mkdir C:\temp\lua-dl-test
cd C:\temp\lua-dl-test
curl -L -o lua-dl.exe https://github.com/hoangvu12/lua-dl/releases/download/v0.1.0/lua-dl.exe

# Does it run?
./lua-dl.exe download 431960
```

If Defender quarantines the exe here, **stop and handle it** before proceeding (see Phase 8). Shipping a bot that delivers an instantly-quarantined binary wastes everyone's time.

---

## Phase 5 — Discord bot scaffold

### 5.1 Create `bot/package.json`

```json
{
  "name": "lua-dl-bot",
  "private": true,
  "type": "module",
  "scripts": {
    "start": "bun run src/index.ts",
    "register": "bun run src/register-commands.ts"
  },
  "dependencies": {
    "discord.js": "^14.16.0"
  }
}
```

No dotenv needed — Bun auto-loads `.env`.

### 5.2 `bot/tsconfig.json`

```json
{
  "compilerOptions": {
    "target": "ESNext",
    "module": "ESNext",
    "moduleResolution": "bundler",
    "strict": true,
    "esModuleInterop": true,
    "skipLibCheck": true,
    "types": ["bun-types"]
  },
  "include": ["src/**/*.ts"]
}
```

### 5.3 `bot/.env.example`

```bash
# From https://discord.com/developers/applications → your app → Bot
DISCORD_TOKEN=

# Same page → General Information → Application ID
DISCORD_APP_ID=

# Fallback version baked into the bat — should match the latest tag you've shipped.
# Only used if the .bat's GitHub-API call to fetch the latest release fails.
CLI_VERSION=0.1.0

# GitHub repo that hosts releases
CLI_REPO=hoangvu12/lua-dl
```

### 5.4 `bot/src/bat-template.ts`

The `.bat` is a template string with simple `{{PLACEHOLDER}}` substitution. Kept as raw text for easy editing.

```ts
const TEMPLATE = String.raw`@echo off
chcp 65001 >nul
setlocal enabledelayedexpansion
title lua-dl — app {{APPID}}

set APPID={{APPID}}
set REPO={{REPO}}
set FALLBACK_VERSION={{VERSION}}
set EXE_DIR=%LOCALAPPDATA%\lua-dl

echo === lua-dl ===
echo App ID: %APPID%
echo.

REM Resolve latest version from GitHub API; fall back to baked version on failure.
echo [lua-dl] Checking for latest version...
for /f "usebackq delims=" %%v in (`powershell -NoProfile -ExecutionPolicy Bypass -Command "try { (Invoke-RestMethod 'https://api.github.com/repos/%REPO%/releases/latest' -TimeoutSec 5).tag_name.TrimStart('v') } catch { '' }"`) do set VERSION=%%v
if "%VERSION%"=="" (
  echo [lua-dl] Couldn't reach GitHub API, using fallback v%FALLBACK_VERSION%
  set VERSION=%FALLBACK_VERSION%
) else (
  echo [lua-dl] Latest version: v%VERSION%
)

set EXE=%EXE_DIR%\lua-dl-%VERSION%.exe
set URL=https://github.com/%REPO%/releases/download/v%VERSION%/lua-dl.exe

if not exist "%EXE%" (
  echo [lua-dl] Downloading lua-dl v%VERSION% ^(one-time per version^)...
  if not exist "%EXE_DIR%" mkdir "%EXE_DIR%"
  curl -L --fail --progress-bar -o "%EXE%" "%URL%"
  if errorlevel 1 (
    echo.
    echo [lua-dl] Download failed. Check your internet.
    echo [lua-dl] If Windows Defender blocked it, click "More info"
    echo          then "Run anyway" when it warns you.
    pause
    exit /b 1
  )
)

echo.
echo [lua-dl] Starting download to %CD%\ ...
echo.
"%EXE%" download %APPID%
set RC=%errorlevel%

echo.
if %RC% neq 0 (
  echo [lua-dl] Download failed with code %RC%. See error above.
) else (
  echo [lua-dl] Done.
)
pause
exit /b %RC%
`;

export interface BatParams {
  appid: number;
  version: string;
  repo: string;
}

export function renderBat({ appid, version, repo }: BatParams): string {
  return TEMPLATE
    .replace(/\{\{APPID\}\}/g, String(appid))
    .replace(/\{\{VERSION\}\}/g, version)
    .replace(/\{\{REPO\}\}/g, repo);
}
```

Notes:
- `chcp 65001` switches the console to UTF-8 so Unicode game names in log output render correctly.
- `String.raw` + no backtick nesting keeps the template readable.
- Caret-escape `^(` needed inside the echo because `(` would close a block context.
- `%CD%` is the folder the `.bat` was double-clicked from — by default Windows sets CWD to the folder containing the shortcut/bat when double-clicked.

### 5.5 `bot/src/register-commands.ts`

One-shot script — registers the `/dl` slash command globally. Run manually once per schema change.

```ts
import { REST, Routes, SlashCommandBuilder } from "discord.js";

const token = process.env.DISCORD_TOKEN;
const appId = process.env.DISCORD_APP_ID;
if (!token || !appId) {
  console.error("Missing DISCORD_TOKEN or DISCORD_APP_ID in env");
  process.exit(1);
}

const commands = [
  new SlashCommandBuilder()
    .setName("dl")
    .setDescription("Get a .bat file to download a Steam game")
    .addIntegerOption((opt) =>
      opt
        .setName("appid")
        .setDescription("Steam App ID (e.g. 431960 for Wallpaper Engine)")
        .setRequired(true)
        .setMinValue(1)
    )
    .toJSON(),
];

const rest = new REST({ version: "10" }).setToken(token);

console.log("Registering slash commands globally...");
await rest.put(Routes.applicationCommands(appId), { body: commands });
console.log("Done. Commands may take up to 1 hour to propagate globally.");
console.log("For faster testing, register per-guild via Routes.applicationGuildCommands(appId, guildId).");
```

### 5.6 `bot/src/index.ts`

```ts
import {
  Client,
  Events,
  GatewayIntentBits,
  MessageFlags,
  AttachmentBuilder,
} from "discord.js";
import { renderBat } from "./bat-template";

const token = process.env.DISCORD_TOKEN;
const cliVersion = process.env.CLI_VERSION;
const cliRepo = process.env.CLI_REPO;
if (!token || !cliVersion || !cliRepo) {
  console.error("Missing env: DISCORD_TOKEN, CLI_VERSION, CLI_REPO required");
  process.exit(1);
}

const client = new Client({ intents: [GatewayIntentBits.Guilds] });

client.once(Events.ClientReady, (c) => {
  console.log(`[bot] logged in as ${c.user.tag}`);
});

client.on(Events.InteractionCreate, async (i) => {
  if (!i.isChatInputCommand() || i.commandName !== "dl") return;

  const appid = i.options.getInteger("appid", true);
  if (appid <= 0 || appid > 10_000_000) {
    await i.reply({
      content: "❌ Invalid App ID.",
      flags: MessageFlags.Ephemeral,
    });
    return;
  }

  const bat = renderBat({ appid, version: cliVersion, repo: cliRepo });
  const file = new AttachmentBuilder(Buffer.from(bat, "utf8"), {
    name: `lua-dl-${appid}.bat`,
  });

  await i.reply({
    content:
      `📥 **lua-dl** — Steam App \`${appid}\`\n` +
      `1. Download the attached \`.bat\` file.\n` +
      `2. Put it in an **empty folder** (e.g. \`Desktop\\Games\`).\n` +
      `3. Double-click it. First run downloads ~20 MB of \`lua-dl.exe\`.\n` +
      `4. Game lands in a folder next to the \`.bat\`.\n\n` +
      `⚠️ Windows will warn "protected your PC" on first run — click **More info** → **Run anyway**. One-time per PC.`,
    files: [file],
  });
});

client.login(token);
```

### 5.7 `bot/README.md`

```markdown
# lua-dl bot

Discord bot that generates `.bat` downloaders on demand.

## Setup (once)

1. Create app at https://discord.com/developers/applications
2. Add a Bot user, copy its token
3. Copy `.env.example` → `.env`, fill in `DISCORD_TOKEN` and `DISCORD_APP_ID`
4. Install: `bun install`
5. Register the `/dl` slash command: `bun run register`
6. Invite the bot to a server:
   `https://discord.com/api/oauth2/authorize?client_id=<APP_ID>&scope=applications.commands%20bot&permissions=2048`

## Running

```bash
bun run start
```

Bot connects to Discord and listens. Keep the process alive.

## Updating the CLI version

Bump `CLI_VERSION` in `.env`, restart the bot. New `.bat`s will use this as the fallback version if the GitHub API call fails. (The bat always prefers the latest release from the API.)
```

---

## Phase 6 — Bot hosting (deferred)

Three options, ranked for this use case:

| Host | Cost | Effort | Notes |
|---|---|---|---|
| User's PC | $0 | 0 | Best for first smoke test. Offline when PC sleeps. |
| Oracle Cloud Free VM | $0 forever | ~30 min | ARM/AMD, always-on. Best long-term. |
| Railway / Fly.io / Render | $0 with limits | ~15 min | Auto-deploy from GitHub. May sleep on free tier. |

Start on user's PC for end-to-end validation. Move to Oracle free VM once proven. Don't premature-optimize hosting.

---

## Phase 7 — End-to-end smoke test

Run this in order after Phase 5 is done and bot is running locally:

1. **Invite bot** to a personal test server (use the OAuth URL from Phase 5.7).
2. In that server, type `/dl 431960`. Verify:
   - Slash command autocompletes
   - Bot replies with a message + `.bat` attachment
   - Attachment name is `lua-dl-431960.bat`
3. **Download the `.bat`** from Discord to `C:\temp\lua-dl-smoke\`.
4. **Double-click the `.bat`**. Expect:
   - "Checking for latest version..." → "Latest version: v0.1.0"
   - "Downloading lua-dl v0.1.0..." → curl progress bar
   - "Starting download to C:\temp\lua-dl-smoke\ ..."
   - CLI logs game name, depot list, progress
   - On completion: `C:\temp\lua-dl-smoke\Wallpaper Engine\` exists with 660 MB of content
5. **Run the `.bat` a second time**. Expect:
   - Version check runs
   - Cached `.exe` is used (no re-download)
   - Resume logic kicks in — all files skipped
6. **Test update flow**:
   - Locally bump to v0.1.1 (add a trivial change)
   - Tag + push, wait for release to build
   - Run the same `.bat` again: expect new version detected and downloaded
7. **Test from a second machine** (friend's PC or a VM) to validate the Defender / SmartScreen behavior in the wild.

---

## Phase 8 — Handling Defender / SmartScreen (fallback)

Escalation ladder, stop at whichever works:

1. **Document the click-through** in the bot reply + README. First-run SmartScreen warning is acceptable friction.
2. **Submit false positive to MSRC** at https://www.microsoft.com/en-us/wdsi/filesubmission. Takes 1-2 days. Reduces Defender hits on subsequent releases.
3. **Ship a Bun-runtime fallback bat** alongside the binary bat. Generates a second `.bat` variant:
   ```bat
   where bun >nul 2>nul || powershell -c "irm bun.sh/install.ps1 | iex"
   bun run github:hoangvu12/lua-dl/src/cli.ts download %APPID%
   ```
   Bun's own installer is signed and reputable. Friend installs Bun (~50 MB) once, after that everything runs from TypeScript source with no binary.
   - Downside: slower cold start, needs internet every run for dependencies.
   - Emit this bat as a second attachment OR let the user request it with `/dl appid:431960 mode:bun`.
4. **Standard code-signing cert** (~$80-200/year from Sectigo/SSL.com). Removes Defender false positives immediately. SmartScreen still builds reputation over time.
5. **EV code-signing cert** (~$300-500/year). Instant SmartScreen reputation. Overkill.

Don't skip ahead — start at (1) and only escalate if friends report actual quarantines.

---

## Gotchas and edge cases (don't re-learn)

1. **`%CD%` when double-clicking a `.bat`**: Windows sets CWD to the folder containing the bat. Do NOT use `%~dp0` for the output dir — that works too but `%CD%` matches user expectation if they run the bat via command line from a different folder.

2. **PowerShell first-run latency**: ~400-600 ms cold, ~200 ms warm. Total bat startup overhead is ~1 second. Acceptable.

3. **`chcp 65001`**: must happen before any `echo` that contains non-ASCII, and the `.bat` file itself should be saved as UTF-8 without BOM (Bun's `Buffer.from(text, 'utf8')` does this).

4. **Caret escaping in the template**: `(` and `)` inside `echo` lines inside an `if` block need `^(` / `^)`. Already handled in the template above — don't remove.

5. **GitHub API rate limit**: 60/hr unauthenticated per IP. For a friend group this is invisible. For a viral bat, would break. Fallback to baked version on failure keeps the bat working regardless.

6. **Bun `--compile` output size**: expect ~15-25 MB after `--minify`. If the build output exceeds 50 MB, something is wrong (probably a stray dependency).

7. **Discord attachment size**: `.bat` files are ~2 KB. Nowhere near Discord's 25 MB limit.

8. **Slash command propagation**: global commands take up to 1 hour to appear. For faster testing, register to a single guild instead (swap `applicationCommands` → `applicationGuildCommands(appId, guildId)` in `register-commands.ts`).

9. **The bot uses `GatewayIntentBits.Guilds` only** — no `MessageContent`, no `GuildMembers`. Minimum intents needed for slash commands. Don't add more intents unnecessarily; Discord requires approval above 100 servers for privileged intents.

10. **Output dir collision** (two different games with same sanitized name): not handled. Rare. If it becomes an issue, append `(appid)` to the folder name.

11. **`StateCache` path**: instantiated before the output dir exists → it might error on write. Verify `StateCache` creates its parent dir on first flush. If not, mkdir outDir upfront in `cli.ts` before `new StateCache(...)`.

12. **Uncommitted .claude/scheduled_tasks.lock**: this file is in current git status. Should stay in `.gitignore` — it's a local Claude Code artifact, not project state.

---

## Roadmap (deferred — not in scope for v0.1.0)

- **Progress reporting back to Discord**: bot watches a status file or polls bat output. Would require the bat to ping back — a lot of infra for little value.
- **Library UI**: a web page listing available game fixes (online-fix.me integration). Out of scope; separate project.
- **Multi-platform builds**: currently Windows only. Linux/Mac exes possible (`--target=bun-linux-x64` / `bun-darwin-arm64`). Add when a non-Windows friend actually asks.
- **online-fix.me integration**: research done in previous conversation. URL pattern `/games/{cat}/{id}-{slug}-po-seti.html` → auth cookie on `uploads.online-fix.me:2053` → nginx autoindex. Feasible but out of scope; revisit after v0.1.0 ships.
- **Automatic MSRC submissions on new release**: can automate false-positive submission via Microsoft's portal API. Only worth it if Defender becomes a recurring problem.
- **Rename branch `master` → `main`**: cosmetic. Do whenever.

---

## Execution checklist (for the next agent)

Copy this into TaskCreate at start of execution.

- [ ] Phase 1.1 Write `src/sanitize.ts`
- [ ] Phase 1.2 Edit `src/steam.ts` (add `getAppInfo`)
- [ ] Phase 1.3 Edit `src/cli.ts` (use `getAppInfo`, default outDir)
- [ ] Phase 1.4 Edit `package.json` (add `build:exe` script)
- [ ] Phase 1.5 Edit `.gitignore`
- [ ] Phase 1.6 Write `README.md`
- [ ] Phase 1.7 Local smoke test (`bun run src/cli.ts download 431960` + `bun run build:exe`)
- [ ] Phase 2.1 Write `.github/workflows/release.yml`
- [ ] ⚠️ Get user confirmation: repo name, public, commit OK, branch
- [ ] Commit everything as one commit
- [ ] Phase 3.1 `gh repo create hoangvu12/lua-dl --public ...`
- [ ] Phase 3.2 Push master
- [ ] Phase 4.1 Tag `v0.1.0` + push
- [ ] Phase 4.2 Watch CI run
- [ ] Phase 4.3 Verify release asset exists
- [ ] Phase 4.4 Manual download-and-run test on your Windows box
- [ ] Phase 5.1 `bot/package.json`
- [ ] Phase 5.2 `bot/tsconfig.json`
- [ ] Phase 5.3 `bot/.env.example`
- [ ] Phase 5.4 `bot/src/bat-template.ts`
- [ ] Phase 5.5 `bot/src/register-commands.ts`
- [ ] Phase 5.6 `bot/src/index.ts`
- [ ] Phase 5.7 `bot/README.md`
- [ ] Commit bot scaffold
- [ ] ⚠️ User step: create Discord app, grab token, fill `.env`
- [ ] Register commands
- [ ] Run bot locally
- [ ] Phase 7 Smoke test end-to-end
- [ ] Phase 6 (later) Move bot to Oracle free VM or equivalent

---

## Things NOT to do

- Don't skip the manual Phase 4.4 exe run. Shipping a bot that hands out a quarantined binary is the worst possible failure mode.
- Don't build the web-app version we discussed and rejected. CLI + bot is the chosen path.
- Don't pay for a code-signing cert until Defender is actually a recurring problem in the wild.
- Don't use Cloudflare Workers for the bot — HTTP interactions need ed25519 sig verification and CF's edge quirks add more pain than value for a hobby bot.
- Don't commit `.env` (it'd contain the Discord token). `.gitignore` covers it.
- Don't commit `lua-dl.exe`. It's a build artifact; ships via Releases.
- Don't forget `chcp 65001` in the bat — Unicode game names will mojibake without it.
- Don't add more Discord intents than `Guilds`. Privileged intents gate the bot's scalability.
- Don't merge the bot and CLI into one `bun run` invocation. Keep CLI pure — the bot shells out via the compiled exe only.
- Don't break the existing `parse` / `probe` / `download <file.lua>` flows. The uncommitted CLI work must keep working after Phase 1 edits.
