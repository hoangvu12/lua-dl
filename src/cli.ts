/**
 * lua-dl CLI (Phase 1 POC)
 *
 * Subcommands:
 *   parse <file.lua>     — parse + print what was extracted
 *   probe <file.lua>     — parse + anonymous-login to Steam + fetch live manifest IDs
 */

import "./http-patch"; // MUST be first — tunes globalAgent before steam-user loads
import "./bundle-prelude"; // force bun --compile to bundle steam-user transitive deps
import { shutdownLzmaPool } from "./cdn-patch"; // LZMA decompress → worker pool

if (process.env.CPU_SAMPLE === "1") {
  let prevCpu = process.cpuUsage();
  let prevT = process.hrtime.bigint();
  setInterval(() => {
    const cpu = process.cpuUsage(prevCpu);
    const t = process.hrtime.bigint();
    const elapsedMs = Number(t - prevT) / 1e6;
    const totalCpuMs = (cpu.user + cpu.system) / 1000;
    const pct = ((totalCpuMs / elapsedMs) * 100).toFixed(0);
    process.stderr.write(
      `[cpu] main=${pct}% user=${(cpu.user / 1000).toFixed(0)}ms sys=${(cpu.system / 1000).toFixed(0)}ms\n`
    );
    prevCpu = process.cpuUsage();
    prevT = process.hrtime.bigint();
  }, 2000).unref();
}
import { readFileSync, existsSync } from "node:fs";
import { join } from "node:path";
import { parseLua } from "./lua";
import { anonymousLogin, getAppDepots, getAppInfo } from "./steam";
import { injectDepotKeys, downloadDepot } from "./download";
import { setVerbose } from "./verbose";
import { StateCache } from "./state";
import { resolveLua } from "./manifest-resolver";
import { sanitizeFolderName } from "./sanitize";

const [, , cmd, arg, ...rest] = process.argv;
if (rest.includes("--verbose") || rest.includes("-v")) setVerbose(true);

if (!cmd || !arg) {
  console.error("Usage: bun run src/cli.ts <parse|probe|download> <file.lua|appid> [--depot ID] [--out DIR]");
  process.exit(1);
}

function flag(name: string): string | undefined {
  const i = rest.indexOf(name);
  return i >= 0 ? rest[i + 1] : undefined;
}

// arg is either a .lua file path or a bare appid. Bare appid → fetch from mirror.
let source: string;
let sourceLabel: string;
if (/^\d+$/.test(arg) && !existsSync(arg)) {
  const appId = Number(arg);
  const { source: mirror, text } = await resolveLua(appId);
  source = text;
  sourceLabel = `appid ${appId} via ${mirror}`;
} else {
  source = readFileSync(arg, "utf8");
  sourceLabel = arg;
}
const parsed = parseLua(source);

console.log(`\n== Parsed ${sourceLabel} ==`);
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

  const client = await anonymousLogin();
  injectDepotKeys(client, parsed.depots);

  let state: StateCache | undefined;
  try {
    const appInfo = await getAppInfo(client, parsed.appId);
    const outDir =
      flag("--out") ?? join(".", sanitizeFolderName(appInfo.name));
    state = new StateCache(join(outDir, ".lua-dl-state.json"));

    console.error(`\n== Game: ${appInfo.name} ==`);
    console.error(`== Output: ${outDir} ==`);

    const targets = appInfo.depots.filter((d) => {
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
        outDir,
        state
      );
    }
  } finally {
    state?.flush();
    client.logOff();
    shutdownLzmaPool();
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
