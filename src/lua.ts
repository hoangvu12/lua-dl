/**
 * Parse openlua.cloud-style SteamTools lua files.
 *
 * Handles:
 *   - Zero-width watermark chars (openlua.cloud fingerprints leaked files)
 *   - addappid(APPID, 1, "DEPOT_KEY")
 *   - setManifestid(DEPOTID, "MANIFEST_ID", 0)
 */

export interface DepotEntry {
  id: number;
  key: string;          // hex
  manifestId?: string;  // present only if setManifestid was used
  label?: string;       // from trailing comment, best-effort
}

export interface LuaParseResult {
  appId: number;        // main app id (first addappid)
  depots: DepotEntry[]; // includes main app, content depots, DLCs
  raw: string;          // cleaned source
}

const ZERO_WIDTH = /[\u200B-\u200D\uFEFF\u2060]/g;

export function parseLua(source: string): LuaParseResult {
  const clean = source.replace(ZERO_WIDTH, "");

  const depots = new Map<number, DepotEntry>();

  // addappid(APPID, 1, "KEY")  — optional trailing comment
  const addRe =
    /addappid\s*\(\s*(\d+)\s*,\s*\d+\s*,\s*"([0-9a-fA-F]+)"\s*\)\s*(?:--\s*(.*))?/g;
  for (const m of clean.matchAll(addRe)) {
    const id = Number(m[1]);
    depots.set(id, {
      id,
      key: m[2].toLowerCase(),
      label: m[3]?.trim(),
    });
  }

  // setManifestid(DEPOTID, "MID", 0)
  const setRe = /setManifestid\s*\(\s*(\d+)\s*,\s*"(\d+)"/g;
  for (const m of clean.matchAll(setRe)) {
    const id = Number(m[1]);
    const existing = depots.get(id);
    if (existing) existing.manifestId = m[2];
    else depots.set(id, { id, key: "", manifestId: m[2] });
  }

  const first = [...depots.values()].find((d) => d.key);
  if (!first) throw new Error("No addappid(...) entries found");

  return {
    appId: first.id,
    depots: [...depots.values()],
    raw: clean,
  };
}
