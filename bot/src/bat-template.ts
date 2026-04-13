/**
 * Renders a per-appid .bat that:
 *   1. Queries GitHub Releases API for the latest lua-dl.exe tag
 *   2. Caches it in %LOCALAPPDATA%\lua-dl\lua-dl-<version>.exe
 *   3. Runs it with the baked-in appid
 *
 * The output dir for the game is %CD% — the folder the friend double-clicks
 * the bat from. The CLI picks a sanitized game name subfolder itself.
 *
 * Gotchas baked in (don't remove):
 *  - `chcp 65001` so Unicode game names in the CLI's stderr render correctly
 *  - `^(` / `^)` caret-escapes inside the `if` block's echoes
 *  - PowerShell fallback for the GitHub API call because bat can't parse JSON
 */

// The `.bat` template wraps a PowerShell command in `for /f "usebackq" %%v
// in (`…`)`. That inner backtick would terminate our JS template literal,
// so split the template around it and rejoin via string concat.
const BT = "`";

const TEMPLATE_HEAD = String.raw`@echo off
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
for /f "usebackq delims=" %%v in (`;

const PS_COMMAND = String.raw`powershell -NoProfile -ExecutionPolicy Bypass -Command "try { (Invoke-RestMethod 'https://api.github.com/repos/%REPO%/releases/latest' -TimeoutSec 5).tag_name.TrimStart('v') } catch { '' }"`;

const TEMPLATE_TAIL = String.raw`) do set VERSION=%%v
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
  REM Evict any previously-cached versions so the cache dir stays lean.
  REM This only runs when we're already about to download a new exe, so it
  REM never re-downloads on same-version re-runs.
  del /q "%EXE_DIR%\lua-dl-*.exe" 2>nul
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

const TEMPLATE = TEMPLATE_HEAD + BT + PS_COMMAND + BT + TEMPLATE_TAIL;

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
