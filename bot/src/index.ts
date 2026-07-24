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
import { renderBat, renderOfBat, type BatApp } from "./bat-template";
import {
  childHeader,
  childPickPrompt,
  labelBaseGame,
  labelType,
  missingInputError,
  ofMissingInputError,
  ofReply,
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
import { fetchAppSizes, formatBytes } from "./steamcmd-net";
import {
  getOfGameById,
  searchOfCatalog,
  type OfCatalogGame,
} from "./of-catalog";

const { DISCORD_TOKEN, CLI_VERSION, CLI_REPO } = process.env;
if (!DISCORD_TOKEN || !CLI_VERSION || !CLI_REPO) {
  console.error("Missing env: DISCORD_TOKEN, CLI_VERSION, CLI_REPO required");
  process.exit(1);
}

const PICK_PREFIX = "dl-pick:";
const CHILD_PREFIX = "dl-child:";
const OF_PICK_PREFIX = "of-pick:";

const client = new Client({ intents: [GatewayIntentBits.Guilds] });

// Bump this on every deploy you want to confirm is live. If the log line below
// doesn't show this exact marker, the host is running stale code.
const BUILD_MARKER = "diag-2026-07-25-a";

client.once(Events.ClientReady, (c) => {
  const runtime =
    typeof Bun !== "undefined"
      ? `Bun ${Bun.version}`
      : `Node ${process.version} (undici ${process.versions.undici})`;
  console.log(
    `[bot] logged in as ${c.user.tag} | build=${BUILD_MARKER} | runtime=${runtime}`
  );
});

// After sending a file-bearing reply, log what Discord actually stored. If
// `attachments=0` the request succeeded but Discord dropped the file (a real
// upload/multipart problem); if `attachments=1` the .bat WAS delivered and the
// problem is on the viewing side (wrong channel, client cache, ad-blocker on
// the CDN link). `res` is the Message returned by reply/editReply.
function logSent(res: unknown, tag: string) {
  const msg = res as { id?: string; attachments?: { size?: number } } | null;
  const n = msg?.attachments?.size ?? "?";
  console.log(`[${tag}] sent message id=${msg?.id ?? "?"} attachments=${n}`);
}

client.on(Events.InteractionCreate, async (i) => {
  if (i.isChatInputCommand() && i.commandName === "dl") {
    await handleDl(i);
    return;
  }
  if (i.isChatInputCommand() && i.commandName === "of") {
    await handleOf(i);
    return;
  }
  if (i.isStringSelectMenu() && i.customId.startsWith(OF_PICK_PREFIX)) {
    await handleOfPick(i);
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
    // Defer FIRST. Resolving the name + install size hits appdetails and
    // api.steamcmd.net (up to a 6s timeout) — well past Discord's 3s ack
    // window. Without an early defer the interaction token expires and the
    // reply (with the .bat) is rejected as "Unknown interaction" (10062),
    // which looked like "the attachment failed to send".
    await i.deferReply();
    try {
      const det = await fetchAppDetails(appid);
      await sendBat(i, [{ appid, name: det?.name ?? `App ${appid}` }], lang);
    } catch (err) {
      console.error("[dl-appid]", err);
      await failBat(i, lang);
    }
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

// Surfaces a build/send failure to the user on an already-deferred interaction,
// swallowing any secondary error if the interaction is already gone.
async function failBat(
  i: ChatInputCommandInteraction | StringSelectMenuInteraction,
  lang: Lang
) {
  const content =
    lang === "vi"
      ? "Có lỗi khi tạo file .bat, thử lại nhé."
      : "Something went wrong building the .bat — try again.";
  try {
    await i.editReply({ content, embeds: [], components: [] });
  } catch {
    /* interaction expired or already resolved */
  }
}

// --- Online-Fix full-game flow (/of) ---------------------------------------

async function handleOf(i: ChatInputCommandInteraction) {
  const lang = pickLang(i.locale);
  const id = i.options.getString("id");
  const query = i.options.getString("query");

  if (id) {
    await i.deferReply();
    try {
      const game = await getOfGameById(id.trim());
      if (!game) {
        await i.editReply({ content: searchNoResults(lang, id) });
        return;
      }
      await sendOfBat(i, game, lang);
    } catch (err) {
      console.error("[of-id]", err);
      await i.editReply({ content: searchNoResults(lang, id) });
    }
    return;
  }
  if (query) {
    await sendOfSearch(i, query, lang);
    return;
  }
  await i.reply({
    content: ofMissingInputError(lang),
    flags: MessageFlags.Ephemeral,
  });
}

async function sendOfSearch(
  i: ChatInputCommandInteraction,
  query: string,
  lang: Lang
) {
  await i.deferReply();
  let results: OfCatalogGame[];
  try {
    results = await searchOfCatalog(query, 5);
  } catch (err) {
    console.error("[of-search]", err);
    await i.editReply({ content: searchNoResults(lang, query) });
    return;
  }
  if (results.length === 0) {
    await i.editReply({ content: searchNoResults(lang, query) });
    return;
  }

  const embeds = results.map((r, idx) => {
    const extras: string[] = [];
    if (r.version) extras.push(`v${r.version.replace(/^v/i, "")}`);
    if (r.steamAppId) extras.push(`App ${r.steamAppId}`);
    if (r.updatedAt) extras.push(ofUpdatedLabel(r.updatedAt));
    const e = new EmbedBuilder()
      .setTitle(`${idx + 1}. ${r.title}`.slice(0, 256))
      .setURL(r.originUrl);
    if (extras.length) e.setFooter({ text: extras.join("  •  ") });
    if (r.coverUrl) e.setImage(r.coverUrl);
    return e;
  });

  const menu = new StringSelectMenuBuilder()
    .setCustomId(`${OF_PICK_PREFIX}${i.user.id}`)
    .setPlaceholder(searchPickPrompt(lang))
    .addOptions(
      results.map((r, idx) => {
        const descParts: string[] = [];
        if (r.version) descParts.push(`v${r.version.replace(/^v/i, "")}`);
        if (r.steamAppId) descParts.push(`App ${r.steamAppId}`);
        return {
          label: `${idx + 1}. ${r.title}`.slice(0, 100),
          description: (descParts.join("  •  ") || "Online-Fix").slice(0, 100),
          value: r.id,
        };
      })
    );

  await i.editReply({
    content: searchHeader(lang, query, results.length),
    embeds,
    components: [
      new ActionRowBuilder<StringSelectMenuBuilder>().addComponents(menu),
    ],
  });
}

async function handleOfPick(i: StringSelectMenuInteraction) {
  const lang = pickLang(i.locale);
  if (guardOwner(i, OF_PICK_PREFIX, lang) == null) return;

  const id = i.values[0];
  if (!id) return;

  // Ack the component interaction first, then edit the message via the
  // interaction webhook. editReply attaches files reliably, whereas the
  // UPDATE_MESSAGE interaction callback can drop attachments on some clients.
  try {
    await i.deferUpdate();
    const game = await getOfGameById(id);
    if (!game) {
      console.warn(`[of-pick] game id ${id} not found in catalog`);
      await i.editReply({
        content: searchNoResults(lang, id),
        embeds: [],
        components: [],
      });
      return;
    }

    const bat = renderOfBat({
      game: { articleId: game.id, title: game.title },
      version: CLI_VERSION!,
      repo: CLI_REPO!,
    });
    const res = await i.editReply({
      content: ofReply(lang, game.title),
      embeds: [],
      components: [],
      files: [
        new AttachmentBuilder(Buffer.from(bat, "utf8"), {
          name: ofBatFilename(game.title),
        }),
      ],
    });
    logSent(res, "of-pick");
  } catch (err) {
    console.error("[of-pick] failed to send bat:", err);
    try {
      await i.followUp({
        content:
          lang === "vi"
            ? "Có lỗi khi tạo file .bat, thử lại nhé."
            : "Something went wrong building the .bat — try again.",
        flags: MessageFlags.Ephemeral,
      });
    } catch {
      /* interaction already gone */
    }
  }
}

async function sendOfBat(
  i: ChatInputCommandInteraction,
  game: OfCatalogGame,
  lang: Lang
) {
  const bat = renderOfBat({
    game: { articleId: game.id, title: game.title },
    version: CLI_VERSION!,
    repo: CLI_REPO!,
  });
  const res = await i.editReply({
    content: ofReply(lang, game.title),
    files: [
      new AttachmentBuilder(Buffer.from(bat, "utf8"), {
        name: ofBatFilename(game.title),
      }),
    ],
  });
  logSent(res, "of-id");
}

function ofBatFilename(title: string): string {
  const slug = sanitizeName(title) || "game";
  return `lua-dl-of-${slug}.bat`;
}

// ofUpdatedLabel renders an ISO/date-ish string as "updated 2026-05-12", or the
// raw value when it isn't parseable.
function ofUpdatedLabel(updatedAt: string): string {
  const d = new Date(updatedAt);
  if (Number.isNaN(d.getTime())) return `updated ${updatedAt}`.slice(0, 40);
  return `updated ${d.toISOString().slice(0, 10)}`;
}

async function sendBat(
  i: ChatInputCommandInteraction,
  apps: BatApp[],
  lang: Lang
) {
  const bat = renderBat({ apps, version: CLI_VERSION!, repo: CLI_REPO! });
  const name = batFilename(apps);
  const size = await totalSizeLabel(apps);
  // The caller defers before this runs, so edit the deferred reply. editReply
  // is the webhook-edit path that attaches files reliably.
  const res = await i.editReply({
    content: reply(lang, apps, size),
    files: [new AttachmentBuilder(Buffer.from(bat, "utf8"), { name })],
  });
  logSent(res, "dl-appid");
}

// Sum install-size over every chosen appid, formatted ("3.3 GB"). Returns
// undefined if no sizes are known — the reply silently drops the suffix.
async function totalSizeLabel(apps: BatApp[]): Promise<string | undefined> {
  const sizes = await Promise.all(apps.map((a) => fetchAppSizes(a.appid)));
  const total = sizes.reduce((n, s) => n + (s?.installBytes ?? 0), 0);
  return total > 0 ? formatBytes(total) : undefined;
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
    if (r.installBytes) extras.push(formatBytes(r.installBytes));
    if (r.onlineFixMatches > 0) {
      extras.push(
        r.onlineFixMatches === 1
          ? "Online-Fix"
          : `Online-Fix (${r.onlineFixMatches}${r.onlineFixMatches >= 10 ? "+" : ""})`
      );
    }
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
      results.map((r, idx) => {
        const descParts = [`App ${r.id}`];
        if (r.priceText) descParts.push(r.priceText);
        if (r.installBytes) descParts.push(formatBytes(r.installBytes));
        if (r.onlineFixMatches > 0) {
          descParts.push(
            r.onlineFixMatches === 1
              ? "Online-Fix"
              : `Online-Fix (${r.onlineFixMatches}${r.onlineFixMatches >= 10 ? "+" : ""})`
          );
        }
        return {
          label: `${idx + 1}. ${r.name}`.slice(0, 100),
          description: descParts.join("  •  ").slice(0, 100),
          value: String(r.id),
        };
      })
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

  // Ack within Discord's 3s window before any network. Re-resolving the app
  // and its size can hit appdetails + api.steamcmd.net (6s timeout); on a cold
  // cache that overruns the component-interaction deadline and the follow-up
  // .bat is rejected as "Unknown interaction".
  try {
    await i.deferUpdate();
  } catch {
    return; // token already gone; nothing we can do
  }

  try {
    await sendRootPick(i, appid, lang);
  } catch (err) {
    console.error("[dl-pick]", err);
    await failBat(i, lang);
  }
}

async function sendRootPick(
  i: StringSelectMenuInteraction,
  appid: number,
  lang: Lang
) {
  // Details are cached from the initial search so this is almost always a
  // cache hit; the early deferUpdate covers the cold-cache network case.
  const [det, rootSizes] = await Promise.all([
    fetchAppDetails(appid),
    fetchAppSizes(appid),
  ]);
  const rootName = det?.name ?? `App ${appid}`;
  const childIds = det?.dlc ?? [];
  if (childIds.length === 0) {
    await updateWithBat(i, [{ appid, name: rootName }], lang);
    return;
  }

  // Look each child up so we can render type labels + sizes. All helpers
  // are cached; repeat calls after the search are free.
  const children = (
    await Promise.all(
      childIds.slice(0, 24).map(async (id) => {
        const [cd, sz] = await Promise.all([
          fetchAppDetails(id),
          fetchAppSizes(id),
        ]);
        return cd
          ? { id, name: cd.name, type: cd.type, installBytes: sz?.installBytes }
          : null;
      })
    )
  ).filter(
    (
      c
    ): c is {
      id: number;
      name: string;
      type: string;
      installBytes: number | undefined;
    } => !!c
  );

  if (children.length === 0) {
    await updateWithBat(i, [{ appid, name: rootName }], lang);
    return;
  }

  const sizeSuffix = (b: number | undefined) => (b ? ` (${formatBytes(b)})` : "");
  const options = [
    {
      label: `${labelBaseGame(lang)} — ${rootName}`.slice(0, 100),
      description:
        `App ${appid}${sizeSuffix(rootSizes?.installBytes)}`.slice(0, 100),
      value: String(appid),
    },
    ...children.map((c) => ({
      label: `${labelType(lang, c.type)} — ${c.name}`.slice(0, 100),
      description: `App ${c.id}${sizeSuffix(c.installBytes)}`.slice(0, 100),
      value: String(c.id),
    })),
  ];

  const menu = new StringSelectMenuBuilder()
    .setCustomId(`${CHILD_PREFIX}${i.user.id}:${appid}`)
    .setPlaceholder(childPickPrompt(lang))
    .setMinValues(1)
    .setMaxValues(options.length)
    .addOptions(options);

  await i.editReply({
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

  try {
    await i.deferUpdate();
  } catch {
    return;
  }

  try {
    const apps = await Promise.all(
      appids.map(async (appid): Promise<BatApp> => {
        const det = await fetchAppDetails(appid);
        return { appid, name: det?.name ?? `App ${appid}` };
      })
    );
    await updateWithBat(i, apps, lang);
  } catch (err) {
    console.error("[dl-child]", err);
    await failBat(i, lang);
  }
}

async function updateWithBat(
  i: StringSelectMenuInteraction,
  apps: BatApp[],
  lang: Lang
) {
  const bat = renderBat({ apps, version: CLI_VERSION!, repo: CLI_REPO! });
  const name = batFilename(apps);
  const size = await totalSizeLabel(apps);
  // Caller defers first, so edit the deferred reply (reliable file attach path).
  const res = await i.editReply({
    content: reply(lang, apps, size),
    embeds: [],
    components: [],
    files: [new AttachmentBuilder(Buffer.from(bat, "utf8"), { name })],
  });
  logSent(res, "dl-pick");
}

client.login(DISCORD_TOKEN);
