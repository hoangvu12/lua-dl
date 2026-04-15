/**
 * Renders a .bat that:
 *   1. Queries GitHub Releases API for the latest lua-dl.exe tag
 *   2. Caches it in %LOCALAPPDATA%\lua-dl\lua-dl-<version>.exe
 *   3. Runs `lua-dl.exe download <appid>` once per appid, sequentially
 *
 * Multiple appids are used when the user picks a base game plus its
 * soundtrack / DLC children from the bot's picker. Each appid has its
 * own lua and its own depots, so the CLI is invoked once per pick.
 *
 * The output dir for the game is %CD% — the folder the friend double-clicks
 * the bat from. The CLI picks a sanitized game name subfolder itself.
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

  return TEMPLATE.replace(/\{\{TITLE\}\}/g, title)
    .replace(/\{\{DOWNLOADS\}\}/g, downloads)
    .replace(/\{\{VERSION\}\}/g, version)
    .replace(/\{\{REPO\}\}/g, repo)
    .replace(/\{\{BT\}\}/g, "`");
}
