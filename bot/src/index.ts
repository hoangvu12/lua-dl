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
  Client,
  Events,
  GatewayIntentBits,
  MessageFlags,
  AttachmentBuilder,
} from "discord.js";
import { renderBat } from "./bat-template";
import { pickLang, reply } from "./i18n";

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

  const lang = pickLang(i.locale);
  await i.reply({
    content: reply(lang, appid),
    files: [file],
  });
});

client.login(token);
