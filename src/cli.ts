/**
 * lua-dl CLI (Phase 1 POC)
 *
 * Subcommands:
 *   parse <file.lua>     — parse + print what was extracted
 *   probe <file.lua>     — parse + anonymous-login to Steam + fetch live manifest IDs
 */

import { readFileSync } from "node:fs";
import { parseLua } from "./lua";
import { anonymousLogin, getAppDepots } from "./steam";
import { injectDepotKeys, downloadDepot } from "./download";

const [, , cmd, file, ...rest] = process.argv;

if (!cmd || !file) {
  console.error("Usage: bun run src/cli.ts <parse|probe|download> <file.lua> [--depot ID] [--out DIR]");
  process.exit(1);
}

function flag(name: string): string | undefined {
  const i = rest.indexOf(name);
  return i >= 0 ? rest[i + 1] : undefined;
}

const source = readFileSync(file, "utf8");
const parsed = parseLua(source);

console.log(`\n== Parsed ${file} ==`);
console.log(`App ID: ${parsed.appId}`);
console.log(`Entries: ${parsed.depots.length}`);
for (const d of parsed.depots) {
  const key = d.key ? d.key.slice(0, 12) + "…" : "(no key)";
  const mid = d.manifestId ? ` manifest=${d.manifestId}` : "";
  const label = d.label ? ` — ${d.label}` : "";
  console.log(`  ${d.id}  key=${key}${mid}${label}`);
}

if (cmd === "parse") process.exit(0);

if (cmd === "download") {
  const onlyDepot = flag("--depot") ? Number(flag("--depot")) : undefined;
  const outDir = flag("--out") ?? "./out";

  const client = await anonymousLogin();
  injectDepotKeys(client, parsed.depots);

  try {
    const liveDepots = await getAppDepots(client, parsed.appId);
    const targets = liveDepots.filter((d) => {
      if (!d.manifestId) return false;
      if (onlyDepot && d.depotId !== onlyDepot) return false;
      // Only download depots we have keys for
      return parsed.depots.some((l) => l.id === d.depotId && l.key);
    });

    if (targets.length === 0) {
      console.error("No downloadable depots matched filter.");
      process.exit(1);
    }

    console.error(
      `\n== Downloading ${targets.length} depot(s) to ${outDir} ==`
    );
    for (const t of targets) {
      await downloadDepot(
        client,
        parsed.appId,
        t.depotId,
        t.manifestId!,
        outDir
      );
    }
  } finally {
    client.logOff();
    setTimeout(() => process.exit(0), 500);
  }
}

if (cmd === "probe") {
  console.log("\n== Probing Steam for live manifest IDs ==");
  const client = await anonymousLogin();
  try {
    const depots = await getAppDepots(client, parsed.appId);
    console.log(`Steam returned ${depots.length} depot entries:`);
    for (const d of depots) {
      const lua = parsed.depots.find((x) => x.id === d.depotId);
      const hasKey = lua?.key ? "✓ key" : "  ";
      const size = d.maxSize ? ` (${(d.maxSize / 1e9).toFixed(2)} GB)` : "";
      console.log(
        `  ${hasKey}  ${d.depotId}  manifest=${d.manifestId ?? "(none)"}${size}  ${d.name ?? ""}`
      );
    }
  } finally {
    client.logOff();
    // steam-user keeps sockets alive; force exit after logoff
    setTimeout(() => process.exit(0), 500);
  }
}
