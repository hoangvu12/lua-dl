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

echo === lua-dl ===
echo {{HEADER}}
echo.

REM Resolve latest version from GitHub API; fall back to baked version on failure.
echo [lua-dl] Checking for latest version...
for /f "usebackq delims=" %%v in ({{BT}}powershell -NoProfile -ExecutionPolicy Bypass -Command "try { (Invoke-RestMethod 'https://api.github.com/repos/%REPO%/releases/latest' -TimeoutSec 5).tag_name.TrimStart('v') } catch { '' }"{{BT}}) do set VERSION=%%v
if "%VERSION%"=="" (
  echo [lua-dl] Couldn't reach GitHub API, using fallback v%FALLBACK_VERSION%
  set VERSION=%FALLBACK_VERSION%
) else (
  echo [lua-dl] Latest version: v%VERSION%
)

set EXE=%EXE_DIR%\lua-dl-%VERSION%.exe
set URL=https://github.com/%REPO%/releases/download/v%VERSION%/lua-dl.exe

if not exist "%EXE%" (
  echo [lua-dl] Downloading lua-dl v%VERSION% ^(~24MB, one-time per version^)...
  if not exist "%EXE_DIR%" mkdir "%EXE_DIR%"
  REM Evict any previously-cached versions so the cache dir stays lean.
  REM This only runs when we're already about to download a new exe, so it
  REM never re-downloads on same-version re-runs.
  del /q "%EXE_DIR%\lua-dl-*.exe" 2>nul
  curl -L --fail -s -o "%EXE%" "%URL%"
  if errorlevel 1 (
    echo.
    echo [lua-dl] Download failed. Check your internet.
    echo [lua-dl] If Windows Defender blocked it, click "More info"
    echo          then "Run anyway" when it warns you.
    pause
    exit /b 1
  )
)

set WORST_RC=0
{{DOWNLOADS}}
echo.
if %WORST_RC% neq 0 (
  echo [lua-dl] One or more downloads failed. See errors above.
) else (
  echo [lua-dl] All done.
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
  const header =
    apps.length === 1
      ? primary
      : `${apps.length} items: ${apps.map((a) => a.name).join(", ")}`;

  const total = apps.length;
  const downloads = apps
    .map((a, idx) => {
      const step = total > 1 ? `${idx + 1}/${total}: ` : "";
      // Sanitize for echo: strip % (would be expanded by cmd) and carets.
      const echoName = a.name.replace(/[%^]/g, "");
      return [
        ``,
        `echo.`,
        `echo [lua-dl] Starting download ${step}${echoName} to %CD%\\ ...`,
        `echo.`,
        `"%EXE%" download ${a.appid}`,
        `if errorlevel 1 set WORST_RC=%errorlevel%`,
      ].join("\n");
    })
    .join("\n");

  return TEMPLATE.replace(/\{\{TITLE\}\}/g, title)
    .replace(/\{\{HEADER\}\}/g, header)
    .replace(/\{\{DOWNLOADS\}\}/g, downloads)
    .replace(/\{\{VERSION\}\}/g, version)
    .replace(/\{\{REPO\}\}/g, repo)
    .replace(/\{\{BT\}\}/g, "`");
}
