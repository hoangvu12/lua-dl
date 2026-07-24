/**
 * Renders a .bat that:
 *   1. Queries GitHub Releases API for the latest lua-dl.exe tag
 *   2. Caches it in %LOCALAPPDATA%\lua-dl\lua-dl-<version>.exe
 *   3. Runs the CLI: `download <appid>` (Steam) or `of <articleId>` (Online-Fix)
 *
 * For Steam (`renderBat`), multiple appids are used when the user picks a base
 * game plus its soundtrack / DLC children; the CLI is invoked once per pick.
 * For Online-Fix (`renderOfBat`), a single `of` call downloads and extracts the
 * full cracked build.
 *
 * The output dir is %CD% — the folder the friend double-clicks the bat from.
 * The Steam CLI picks a sanitized game-name subfolder itself; the Online-Fix
 * path is handed an explicit `--out "<folder>"`.
 *
 * Gotchas baked in (don't remove):
 *  - `chcp 65001` so Unicode game names in the CLI's stderr render correctly
 *  - `^(` / `^)` caret-escapes inside the `if` block's echoes
 *  - PowerShell fallback for the GitHub API call because bat can't parse JSON
 *  - `{{BT}}` placeholder stands in for the literal backticks around the
 *    PowerShell invocation — a real backtick would close this template literal
 */
const TEMPLATE = String.raw`@echo off
chcp 65001 >nul
setlocal enabledelayedexpansion
title lua-dl — {{TITLE}}

set REPO={{REPO}}
set FALLBACK_VERSION={{VERSION}}
set EXE_DIR=%LOCALAPPDATA%\lua-dl

REM Resolve latest version from GitHub API; fall back to baked version on failure.
for /f "usebackq delims=" %%v in ({{BT}}powershell -NoProfile -ExecutionPolicy Bypass -Command "try { (Invoke-RestMethod 'https://api.github.com/repos/%REPO%/releases/latest' -TimeoutSec 5).tag_name.TrimStart('v') } catch { '' }"{{BT}}) do set VERSION=%%v
if "%VERSION%"=="" set VERSION=%FALLBACK_VERSION%

set EXE=%EXE_DIR%\lua-dl-%VERSION%.exe
set URL=https://github.com/%REPO%/releases/download/v%VERSION%/lua-dl.exe

if not exist "%EXE%" (
  echo Downloading lua-dl v%VERSION% ^(~24MB, one-time per version^)...
  if not exist "%EXE_DIR%" mkdir "%EXE_DIR%"
  REM Evict any previously-cached versions so the cache dir stays lean.
  REM This only runs when we're already about to download a new exe, so it
  REM never re-downloads on same-version re-runs.
  del /q "%EXE_DIR%\lua-dl-*.exe" 2>nul
  curl -L --fail -s -o "%EXE%" "%URL%"
  if errorlevel 1 (
    echo.
    echo Download failed. Check your internet.
    echo If Windows Defender blocked it, click "More info" then "Run anyway".
    pause
    exit /b 1
  )
)

set WORST_RC=0
{{DOWNLOADS}}
if %WORST_RC% neq 0 (
  echo.
  echo One or more downloads failed. See errors above.
)
pause
exit /b %WORST_RC%
`;

export interface BatApp {
  appid: number;
  name: string; // human-readable label for echo output
}

export interface BatParams {
  apps: BatApp[];
  version: string;
  repo: string;
}

export function renderBat({ apps, version, repo }: BatParams): string {
  if (apps.length === 0) throw new Error("renderBat: apps is empty");

  const primary = apps[0].name;
  const title = apps.length === 1 ? primary : `${primary} (+${apps.length - 1})`;

  const downloads = apps
    .map((a) => {
      return [
        ``,
        `"%EXE%" download ${a.appid}`,
        `if errorlevel 1 set WORST_RC=%errorlevel%`,
      ].join("\n");
    })
    .join("\n");

  return fillTemplate(title, downloads, version, repo);
}

export interface BatOfGame {
  articleId: string; // online-fix article id — passed to `lua-dl of <id>`
  title: string; // human-readable label; also the extraction folder name
}

export interface BatOfParams {
  game: BatOfGame;
  version: string;
  repo: string;
}

/**
 * Renders a .bat that downloads a full game from online-fix.me via
 * `lua-dl of <articleId> --out "<folder>"`. The CLI logs in, resolves the
 * hosters, downloads every archive part, and extracts the game into <folder>
 * (a subfolder of wherever the friend double-clicks the bat).
 */
export function renderOfBat({ game, version, repo }: BatOfParams): string {
  const folder = batSafeFolder(game.title);
  const downloads = [
    ``,
    `"%EXE%" of ${game.articleId} --out "${folder}"`,
    `if errorlevel 1 set WORST_RC=%errorlevel%`,
  ].join("\n");
  return fillTemplate(batSafeTitle(game.title), downloads, version, repo);
}

function fillTemplate(
  title: string,
  downloads: string,
  version: string,
  repo: string
): string {
  return TEMPLATE.replace(/\{\{TITLE\}\}/g, title)
    .replace(/\{\{DOWNLOADS\}\}/g, downloads)
    .replace(/\{\{VERSION\}\}/g, version)
    .replace(/\{\{REPO\}\}/g, repo)
    .replace(/\{\{BT\}\}/g, "`");
}

// batSafeTitle strips cmd metacharacters that would break the `title` line
// (e.g. `&` chains a command, `%`/`!` expand variables).
function batSafeTitle(name: string): string {
  return (
    name
      .replace(/[&<>|%!^"()]/g, " ")
      .replace(/\s+/g, " ")
      .trim() || "online-fix"
  );
}

// batSafeFolder makes a Windows-safe folder name to pass to --out. It keeps
// ASCII letters, digits, spaces, dashes, underscores and dots, and drops
// everything else — including chars unsafe inside double quotes with delayed
// expansion on (%, !) and anything cmd/NTFS reject.
function batSafeFolder(name: string): string {
  const cleaned = name
    .replace(/[^A-Za-z0-9 ._-]+/g, "")
    .replace(/\s+/g, " ")
    .replace(/^[-. ]+|[-. ]+$/g, "");
  return cleaned.slice(0, 80) || "online-fix-game";
}
