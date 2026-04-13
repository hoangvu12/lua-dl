/**
 * lua-dl Discord bot.
 *
 * /dl supports two inputs:
 *   - appid:<int>        → direct, emits .bat immediately
 *   - query:<string>     → searches Steam store, shows embed results with
 *                          header images + a select menu; on pick, emits .bat
 *
 * The bot itself never touches Steam depots — all the heavy lifting is done
 * by lua-dl.exe when the friend runs the bat.
 *
 * Intents: `Guilds` only. Slash commands don't need MessageContent or any
 * privileged intent.
 */
import {
  ActionRowBuilder,
  AttachmentBuilder,
  Client,
  EmbedBuilder,
  Events,
  GatewayIntentBits,
  MessageFlags,
  StringSelectMenuBuilder,
  StringSelectMenuInteraction,
  type ChatInputCommandInteraction,
} from "discord.js";
import { renderBat } from "./bat-template";
import {
  missingInputError,
  pickLang,
  reply,
  searchHeader,
  searchNoResults,
  searchPickPrompt,
  type Lang,
} from "./i18n";
import { searchSteamApps, type SteamSearchResult } from "./steam-search";

const { DISCORD_TOKEN, CLI_VERSION, CLI_REPO } = process.env;
if (!DISCORD_TOKEN || !CLI_VERSION || !CLI_REPO) {
  console.error("Missing env: DISCORD_TOKEN, CLI_VERSION, CLI_REPO required");
  process.exit(1);
}

const PICK_PREFIX = "dl-pick:";

const client = new Client({ intents: [GatewayIntentBits.Guilds] });

client.once(Events.ClientReady, (c) => {
  console.log(`[bot] logged in as ${c.user.tag}`);
});

client.on(Events.InteractionCreate, async (i) => {
  if (i.isChatInputCommand() && i.commandName === "dl") {
    await handleDl(i);
    return;
  }
  if (i.isStringSelectMenu() && i.customId.startsWith(PICK_PREFIX)) {
    await handlePick(i);
    return;
  }
});

async function handleDl(i: ChatInputCommandInteraction) {
  const lang = pickLang(i.locale);
  const appid = i.options.getInteger("appid");
  const query = i.options.getString("query");

  if (appid) {
    await sendBat(i, appid, lang);
    return;
  }
  if (query) {
    await sendSearch(i, query, lang);
    return;
  }
  await i.reply({
    content: missingInputError(lang),
    flags: MessageFlags.Ephemeral,
  });
}

async function sendBat(
  i: ChatInputCommandInteraction,
  appid: number,
  lang: Lang
) {
  const bat = renderBat({ appid, version: CLI_VERSION!, repo: CLI_REPO! });
  await i.reply({
    content: reply(lang, appid),
    files: [
      new AttachmentBuilder(Buffer.from(bat, "utf8"), {
        name: `lua-dl-${appid}.bat`,
      }),
    ],
  });
}

async function sendSearch(
  i: ChatInputCommandInteraction,
  query: string,
  lang: Lang
) {
  await i.deferReply();
  let results: SteamSearchResult[];
  try {
    results = await searchSteamApps(query, 5);
  } catch (err) {
    console.error("[search]", err);
    await i.editReply({ content: searchNoResults(lang, query) });
    return;
  }
  if (results.length === 0) {
    await i.editReply({ content: searchNoResults(lang, query) });
    return;
  }

  const embeds = results.map((r, idx) =>
    new EmbedBuilder()
      .setTitle(`${idx + 1}. ${r.name}`)
      .setURL(`https://store.steampowered.com/app/${r.id}/`)
      .setImage(r.headerImage)
      .setFooter({
        text: [`App ${r.id}`, r.priceText, r.platforms]
          .filter(Boolean)
          .join("  •  "),
      })
  );

  const menu = new StringSelectMenuBuilder()
    .setCustomId(`${PICK_PREFIX}${i.user.id}`)
    .setPlaceholder(searchPickPrompt(lang))
    .addOptions(
      results.map((r, idx) => ({
        label: `${idx + 1}. ${r.name}`.slice(0, 100),
        description: `App ${r.id}${r.priceText ? `  •  ${r.priceText}` : ""}`.slice(
          0,
          100
        ),
        value: String(r.id),
      }))
    );

  await i.editReply({
    content: searchHeader(lang, query, results.length),
    embeds,
    components: [
      new ActionRowBuilder<StringSelectMenuBuilder>().addComponents(menu),
    ],
  });
}

async function handlePick(i: StringSelectMenuInteraction) {
  const lang = pickLang(i.locale);
  const expectedUser = i.customId.slice(PICK_PREFIX.length);
  if (expectedUser && i.user.id !== expectedUser) {
    await i.reply({
      content:
        lang === "vi"
          ? "Chỉ người gọi lệnh mới chọn được."
          : "Only the user who ran the command can pick.",
      flags: MessageFlags.Ephemeral,
    });
    return;
  }
  const appid = Number(i.values[0]);
  if (!Number.isFinite(appid) || appid <= 0) return;

  const bat = renderBat({ appid, version: CLI_VERSION!, repo: CLI_REPO! });
  await i.update({
    content: reply(lang, appid),
    embeds: [],
    components: [],
    files: [
      new AttachmentBuilder(Buffer.from(bat, "utf8"), {
        name: `lua-dl-${appid}.bat`,
      }),
    ],
  });
}

client.login(DISCORD_TOKEN);
