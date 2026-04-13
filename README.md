# lua-dl

Download Steam games without a Steam account or captcha. Give it a Steam App ID, get the game.

## Quick start (non-technical users)

Join the Discord bot server, run `/dl <appid>`, download the `.bat` it replies with, drop it in an empty folder, double-click. Done.

Example: `/dl 431960` → downloads Wallpaper Engine.

**First run warning**: Windows Defender / SmartScreen may show "Windows protected your PC". Click **More info** → **Run anyway**. This is a one-time click per machine — the binary is unsigned because the project is a hobby.

## Quick start (CLI)

```bash
bun install
bun run src/cli.ts download 431960
```

Or compile to a single Windows exe:

```bash
bun run build:exe
./lua-dl.exe download 431960
```

## How it works

See `CONTEXT.md` for architecture, source list, throughput notes, and things not to try.
