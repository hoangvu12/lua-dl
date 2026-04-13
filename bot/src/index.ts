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
      `3. Double-click it. First run downloads ~24 MB of \`lua-dl.exe\`.\n` +
      `4. Game lands in a folder next to the \`.bat\`.\n\n` +
      `⚠️ Windows may warn "protected your PC" on first run — click **More info** → **Run anyway**. One-time per PC.`,
    files: [file],
  });
});

client.login(token);
