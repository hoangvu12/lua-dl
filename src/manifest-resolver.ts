/**
 * Multi-mirror manifest binary resolver.
 *
 * Fetches a pre-cached {depotId}_{manifestId}.manifest file from a GitHub
 * ManifestAutoUpdate-style mirror, in parallel. First 200 response wins.
 *
 * This bypasses Steam's GetManifestRequestCode gate (which rejects anonymous
 * accounts for paid apps). We already know the correct manifestId from live
 * PICS — we just need someone else's already-fetched copy of the binary.
 */

import { fetchRyuuLua, fetchRyuuBundle } from "./ryuu-source";

// Ordered by freshness (most-recent pushed_at first, checked 2026-04-13).
// SPIN0ZAi/SB_manifest_DB is a fork of the DMCA'd SteamAutoCracks/ManifestHub
// (now 404) — 62k+ branches, most complete archive. It *also* contains
// {appid}.lua, {appid}.json, and key.vdf on each branch, so we can use it as
// a source for the .lua script itself in a future "download by appid" mode.
// Dead mirrors dropped: luomojim (2023), hulovewang (2024), xhcom (2024),
// tymolu233/ManifestAutoUpdate (non-fix, 2025-03).
const MIRRORS = [
  "SPIN0ZAi/SB_manifest_DB",
  "tymolu233/ManifestAutoUpdate-fix",
  "BlankTMing/ManifestAutoUpdate",
  "Auiowu/ManifestAutoUpdate",
  "pjy612/SteamManifestCache",
];

export async function resolveLua(appId: number): Promise<{ source: string; text: string }> {
  // Try ryuu first — it's typically richer (includes DLC entries).
  try {
    console.error(`[resolver] trying ryuu.lol for ${appId}.lua`);
    const text = await fetchRyuuLua(appId);
    console.error(`[resolver] ✓ ryuu.lol/resellerlua (${text.length} bytes lua)`);
    return { source: "ryuu.lol/resellerlua", text };
  } catch (err: any) {
    console.error(`[resolver] ryuu.lol failed (${err?.message ?? err}), racing ${MIRRORS.length} GH mirrors`);
  }

  const controller = new AbortController();
  const attempts = MIRRORS.map(async (repo) => {
    const url = `https://raw.githubusercontent.com/${repo}/${appId}/${appId}.lua`;
    const res = await fetch(url, { signal: controller.signal });
    if (!res.ok) throw new Error(`${repo}: HTTP ${res.status}`);
    const text = await res.text();
    if (!/addappid\s*\(/i.test(text)) throw new Error(`${repo}: not a lua script`);
    return { name: repo, text };
  });
  try {
    const { name, text } = await Promise.any(attempts);
    controller.abort();
    console.error(`[resolver] ✓ ${name} (${text.length} bytes lua)`);
    return { source: name, text };
  } catch (err: any) {
    const errors: string[] =
      err?.errors?.map((e: Error) => e.message) ?? [err?.message ?? String(err)];
    throw new Error(`All sources failed to serve ${appId}.lua:\n  - ${errors.join("\n  - ")}`);
  }
}

export interface ResolvedManifest {
  buffer: Buffer;
  source: string;       // which mirror provided it
  bytes: number;
}

const MANIFEST_MAGIC = 0x71f617d0;

function validateManifest(name: string, buf: Buffer): Buffer {
  if (buf.length < 4 || buf.readUInt32LE(0) !== MANIFEST_MAGIC) {
    throw new Error(`${name}: bad magic 0x${buf.readUInt32LE(0).toString(16)}`);
  }
  return buf;
}

export async function resolveManifest(
  appId: number,
  depotId: number,
  manifestId: string
): Promise<ResolvedManifest> {
  const filename = `${depotId}_${manifestId}.manifest`;

  // Try ryuu first — fetched once per appid then cached in-memory, so every
  // depot after the first pays zero network cost.
  try {
    const bundle = await fetchRyuuBundle(appId);
    const entry = bundle.files.get(filename);
    if (entry) {
      const buf = validateManifest("ryuu.lol/secure_download", entry);
      console.error(`[resolver] ✓ ryuu.lol/secure_download ${filename} (${buf.length} bytes)`);
      return { buffer: buf, source: "ryuu.lol/secure_download", bytes: buf.length };
    }
    console.error(`[resolver] ryuu bundle missing ${filename}, falling back to GH mirrors`);
  } catch (err: any) {
    console.error(`[resolver] ryuu.lol failed (${err?.message ?? err}), racing ${MIRRORS.length} GH mirrors`);
  }

  console.error(`[resolver] racing ${MIRRORS.length} mirrors for ${appId}/${filename}`);
  const controller = new AbortController();
  const attempts = MIRRORS.map(async (repo) => {
    const url = `https://raw.githubusercontent.com/${repo}/${appId}/${filename}`;
    const res = await fetch(url, { signal: controller.signal });
    if (!res.ok) throw new Error(`${repo}: HTTP ${res.status}`);
    const buf = validateManifest(repo, Buffer.from(await res.arrayBuffer()));
    return { name: repo, buf };
  });
  try {
    const { name, buf } = await Promise.any(attempts);
    controller.abort();
    console.error(`[resolver] ✓ ${name} (${buf.length} bytes)`);
    return { buffer: buf, source: name, bytes: buf.length };
  } catch (err: any) {
    const errors: string[] =
      err?.errors?.map((e: Error) => e.message) ?? [err?.message ?? String(err)];
    throw new Error(`All sources failed:\n  - ${errors.join("\n  - ")}`);
  }
}
