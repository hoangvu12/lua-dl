/**
 * lua-dl Discord bot.
 *
 * /dl supports two inputs:
 *   - appid:<int>        → direct, emits .bat immediately
 *   - query:<string>     → searches Steam store, shows embed results with
 *                          header images + a select menu; on pick, emits .bat
 *
 * Picker flow:
 *   1. Root pick: one row per game. Soundtracks/DLC hits are pivoted back
 *      to their parent game (see steam-search.ts) so "yapyap" shows the
 *      game once, not game + OST as siblings.
 *   2. If the picked game has children (soundtrack / DLC / demo), a second
 *      multi-select appears with the base game pre-offered plus each child.
 *      On submit, a single .bat is emitted that runs lua-dl once per pick.
 *   3. If the game has no children, the .bat is emitted immediately.
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
import { renderBat, type BatApp } from "./bat-template";
import {
  childHeader,
  childPickPrompt,
  labelBaseGame,
  labelType,
  missingInputError,
  pickLang,
  reply,
  searchHeader,
  searchNoResults,
  searchPickPrompt,
  type Lang,
} from "./i18n";
import {
  fetchAppDetails,
  searchSteamApps,
  type SteamSearchResult,
} from "./steam-search";

const { DISCORD_TOKEN, CLI_VERSION, CLI_REPO } = process.env;
if (!DISCORD_TOKEN || !CLI_VERSION || !CLI_REPO) {
  console.error("Missing env: DISCORD_TOKEN, CLI_VERSION, CLI_REPO required");
  process.exit(1);
}

const PICK_PREFIX = "dl-pick:";
const CHILD_PREFIX = "dl-child:";

const client = new Client({ intents: [GatewayIntentBits.Guilds] });

client.once(Events.ClientReady, (c) => {
  console.log(`[bot] logged in as ${c.user.tag}`);
});

client.on(Events.InteractionCreate, async (i) => {
  if (i.isChatInputCommand() && i.commandName === "dl") {
    await handleDl(i);
    return;
  }
  if (i.isStringSelectMenu() && i.customId.startsWith(CHILD_PREFIX)) {
    await handleChildPick(i);
    return;
  }
  if (i.isStringSelectMenu() && i.customId.startsWith(PICK_PREFIX)) {
    await handleRootPick(i);
    return;
  }
});

async function handleDl(i: ChatInputCommandInteraction) {
  const lang = pickLang(i.locale);
  const appid = i.options.getInteger("appid");
  const query = i.options.getString("query");

  if (appid) {
    const det = await fetchAppDetails(appid);
    await sendBat(i, [{ appid, name: det?.name ?? `App ${appid}` }], lang);
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
  apps: BatApp[],
  lang: Lang
) {
  const bat = renderBat({ apps, version: CLI_VERSION!, repo: CLI_REPO! });
  const name = batFilename(apps);
  await i.reply({
    content: reply(lang, apps),
    files: [new AttachmentBuilder(Buffer.from(bat, "utf8"), { name })],
  });
}

// Builds a human-friendly .bat filename from the root app's name. Multi-app
// bundles get a `-bundle` suffix so the user can tell at a glance it
// downloads more than the base game.
function batFilename(apps: BatApp[]): string {
  const root = apps[0];
  const slug = sanitizeName(root.name);
  const base = slug || `app-${root.appid}`;
  const suffix = apps.length > 1 ? "-bundle" : "";
  return `lua-dl-${base}${suffix}.bat`;
}

// Windows-safe filename slug. Strips reserved chars (<>:"/\|?*), collapses
// whitespace to single dashes, drops control chars, trims trailing dots and
// spaces (Windows rejects those), and caps length so the final filename stays
// well under the 255-char limit.
function sanitizeName(name: string): string {
  const cleaned = name
    // eslint-disable-next-line no-control-regex
    .replace(/[\u0000-\u001f<>:"/\\|?*]+/g, "")
    .replace(/\s+/g, "-")
    .replace(/^[-.]+|[-. ]+$/g, "");
  return cleaned.slice(0, 60);
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

  const embeds = results.map((r, idx) => {
    const extras: string[] = [`App ${r.id}`, r.priceText, r.platforms].filter(
      Boolean
    );
    if (r.children.length > 0) {
      extras.push(
        lang === "vi"
          ? `+${r.children.length} nội dung thêm`
          : `+${r.children.length} extras`
      );
    }
    const e = new EmbedBuilder()
      .setTitle(`${idx + 1}. ${r.name}`)
      .setURL(`https://store.steampowered.com/app/${r.id}/`)
      .setFooter({ text: extras.join("  •  ") });
    if (r.headerImage) e.setImage(r.headerImage);
    return e;
  });

  const menu = new StringSelectMenuBuilder()
    .setCustomId(`${PICK_PREFIX}${i.user.id}`)
    .setPlaceholder(searchPickPrompt(lang))
    .addOptions(
      results.map((r, idx) => ({
        label: `${idx + 1}. ${r.name}`.slice(0, 100),
        description:
          `App ${r.id}${r.priceText ? `  •  ${r.priceText}` : ""}`.slice(0, 100),
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

function guardOwner(
  i: StringSelectMenuInteraction,
  prefix: string,
  lang: Lang
): string | null {
  const rest = i.customId.slice(prefix.length);
  const expectedUser = rest.split(":")[0];
  if (expectedUser && i.user.id !== expectedUser) {
    void i.reply({
      content:
        lang === "vi"
          ? "Chỉ người gọi lệnh mới chọn được."
          : "Only the user who ran the command can pick.",
      flags: MessageFlags.Ephemeral,
    });
    return null;
  }
  return rest;
}

async function handleRootPick(i: StringSelectMenuInteraction) {
  const lang = pickLang(i.locale);
  if (guardOwner(i, PICK_PREFIX, lang) == null) return;

  const appid = Number(i.values[0]);
  if (!Number.isFinite(appid) || appid <= 0) return;

  // Re-resolve the picked app to decide if we need the child selector.
  // Details are cached from the initial search so this is almost always a
  // cache hit; we still defer the update in case we need the network.
  const det = await fetchAppDetails(appid);
  const rootName = det?.name ?? `App ${appid}`;
  const childIds = det?.dlc ?? [];
  if (childIds.length === 0) {
    await updateWithBat(i, [{ appid, name: rootName }], lang);
    return;
  }

  // Look each child up so we can render type labels. fetchAppDetails is
  // cached; these are the same calls the search made.
  const children = (
    await Promise.all(
      childIds.slice(0, 24).map(async (id) => {
        const cd = await fetchAppDetails(id);
        return cd ? { id, name: cd.name, type: cd.type } : null;
      })
    )
  ).filter((c): c is { id: number; name: string; type: string } => !!c);

  if (children.length === 0) {
    await updateWithBat(i, [{ appid, name: rootName }], lang);
    return;
  }

  const options = [
    {
      label: `${labelBaseGame(lang)} — ${rootName}`.slice(0, 100),
      description: `App ${appid}`.slice(0, 100),
      value: String(appid),
    },
    ...children.map((c) => ({
      label: `${labelType(lang, c.type)} — ${c.name}`.slice(0, 100),
      description: `App ${c.id}`.slice(0, 100),
      value: String(c.id),
    })),
  ];

  const menu = new StringSelectMenuBuilder()
    .setCustomId(`${CHILD_PREFIX}${i.user.id}:${appid}`)
    .setPlaceholder(childPickPrompt(lang))
    .setMinValues(1)
    .setMaxValues(options.length)
    .addOptions(options);

  await i.update({
    content: childHeader(lang, rootName),
    embeds: [],
    components: [
      new ActionRowBuilder<StringSelectMenuBuilder>().addComponents(menu),
    ],
  });
}

async function handleChildPick(i: StringSelectMenuInteraction) {
  const lang = pickLang(i.locale);
  if (guardOwner(i, CHILD_PREFIX, lang) == null) return;

  const appids = i.values
    .map((v) => Number(v))
    .filter((n) => Number.isFinite(n) && n > 0);
  if (appids.length === 0) return;

  const apps = await Promise.all(
    appids.map(async (appid): Promise<BatApp> => {
      const det = await fetchAppDetails(appid);
      return { appid, name: det?.name ?? `App ${appid}` };
    })
  );

  await updateWithBat(i, apps, lang);
}

async function updateWithBat(
  i: StringSelectMenuInteraction,
  apps: BatApp[],
  lang: Lang
) {
  const bat = renderBat({ apps, version: CLI_VERSION!, repo: CLI_REPO! });
  const name = batFilename(apps);
  await i.update({
    content: reply(lang, apps),
    embeds: [],
    components: [],
    files: [new AttachmentBuilder(Buffer.from(bat, "utf8"), { name })],
  });
}

client.login(DISCORD_TOKEN);
