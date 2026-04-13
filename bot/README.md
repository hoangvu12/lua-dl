# lua-dl bot

Discord bot that generates `.bat` downloaders on demand.

## Setup (once)

1. Create an app at https://discord.com/developers/applications
2. Add a Bot user, copy its token
3. `cp .env.example .env`, fill in `DISCORD_TOKEN` and `DISCORD_APP_ID`
4. Install: `bun install`
5. Register the `/dl` slash command: `bun run register`
6. Invite the bot to a server:
   `https://discord.com/api/oauth2/authorize?client_id=<APP_ID>&scope=applications.commands%20bot&permissions=2048`

## Running

```bash
bun run start
```

Bot connects to Discord and listens. Keep the process alive (screen, tmux,
pm2, systemd, or a hobby host like an Oracle Cloud free VM).

## Updating the CLI version

Bump `CLI_VERSION` in `.env` to match the latest `lua-dl` release tag, then
restart the bot. This is only a fallback — the generated `.bat` always
prefers the live `releases/latest` lookup from the GitHub API. If that lookup
fails (rate limit, offline), the bat falls back to `CLI_VERSION`.

## Layout

- `src/index.ts` — gateway client, handles `/dl <appid>`
- `src/bat-template.ts` — `.bat` string template + `renderBat()`
- `src/register-commands.ts` — one-shot slash-command registration
