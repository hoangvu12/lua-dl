/**
 * One-shot: registers the /dl slash command globally. Run once per schema
 * change with `bun run register`.
 *
 * The command is registered with both Guild and User install integration
 * types so it works as a "personal bot" — usable in any server (even ones
 * the bot isn't in), DMs, and group DMs — once a user installs it via the
 * Discord-provided install link.
 *
 * Global commands take up to 1 hour to propagate. For faster iteration during
 * testing, swap `Routes.applicationCommands(appId)` for
 * `Routes.applicationGuildCommands(appId, guildId)` and set GUILD_ID.
 */
import {
  ApplicationIntegrationType,
  InteractionContextType,
  REST,
  Routes,
  SlashCommandBuilder,
} from "discord.js";

const token = process.env.DISCORD_TOKEN;
const appId = process.env.DISCORD_APP_ID;
if (!token || !appId) {
  console.error("Missing DISCORD_TOKEN or DISCORD_APP_ID in env");
  process.exit(1);
}

const commands = [
  new SlashCommandBuilder()
    .setName("dl")
    .setDescription("Get a .bat file to download a Steam game")
    .setIntegrationTypes(
      ApplicationIntegrationType.GuildInstall,
      ApplicationIntegrationType.UserInstall,
    )
    .setContexts(
      InteractionContextType.Guild,
      InteractionContextType.BotDM,
      InteractionContextType.PrivateChannel,
    )
    .addIntegerOption((opt) =>
      opt
        .setName("appid")
        .setDescription("Steam App ID (e.g. 431960 for Wallpaper Engine)")
        .setRequired(false)
        .setMinValue(1)
        .setMaxValue(10_000_000)
    )
    .addStringOption((opt) =>
      opt
        .setName("query")
        .setDescription("Search by game name — shows picker with images")
        .setRequired(false)
        .setMinLength(2)
        .setMaxLength(100)
    )
    .toJSON(),
];

const rest = new REST({ version: "10" }).setToken(token);

console.log("Registering slash commands globally...");
await rest.put(Routes.applicationCommands(appId), { body: commands });
console.log("Done. Global commands may take up to 1 hour to propagate.");
