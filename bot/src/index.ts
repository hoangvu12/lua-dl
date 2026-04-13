/**
 * lua-dl Discord bot.
 *
 * Minimal gateway client that responds to /dl <appid> by attaching a
 * per-appid .bat downloader. The bot itself never touches Steam — all the
 * heavy lifting is done by lua-dl.exe when the friend runs the bat.
 *
 * Intents: `Guilds` only. Slash commands don't need MessageContent or any
 * privileged intent; adding more would gate the bot above 100 servers
 * without Discord review for no benefit.
 */
import {
  AttachmentBuilder,
  Client,
  Events,
  GatewayIntentBits,
} from "discord.js";
import { renderBat } from "./bat-template";
import { pickLang, reply } from "./i18n";

const { DISCORD_TOKEN, CLI_VERSION, CLI_REPO } = process.env;
if (!DISCORD_TOKEN || !CLI_VERSION || !CLI_REPO) {
  console.error("Missing env: DISCORD_TOKEN, CLI_VERSION, CLI_REPO required");
  process.exit(1);
}

const client = new Client({ intents: [GatewayIntentBits.Guilds] });

client.once(Events.ClientReady, (c) => {
  console.log(`[bot] logged in as ${c.user.tag}`);
});

client.on(Events.InteractionCreate, async (i) => {
  if (!i.isChatInputCommand() || i.commandName !== "dl") return;

  // Range is enforced by the slash command's min/max — Discord rejects
  // out-of-range values before they reach us.
  const appid = i.options.getInteger("appid", true);
  const bat = renderBat({ appid, version: CLI_VERSION, repo: CLI_REPO });

  await i.reply({
    content: reply(pickLang(i.locale), appid),
    files: [
      new AttachmentBuilder(Buffer.from(bat, "utf8"), {
        name: `lua-dl-${appid}.bat`,
      }),
    ],
  });
});

client.login(DISCORD_TOKEN);
