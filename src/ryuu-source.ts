/**
 * Ryuu.lol generator source.
 *
 * Two endpoints:
 *   /resellerlua?appid=N&auth_code=...   → {appid}.lua (text)
 *   /secure_download?appid=N&auth_code=... → {appid}.zip (STORED) containing
 *     {appid}.lua + all {depotId}_{manifestId}.manifest files
 *
 * The zip is fetched once per appid, parsed in-memory, and cached for the
 * duration of the process. A single zip gives us every manifest for the app
 * (including DLCs), so it's strictly more useful than the per-file GH mirror
 * for downloads that need multiple depots.
 *
 * All manifest binaries inside the zip are STORED, so we don't need a real
 * zip library — a ~40-line EOCD/central-directory walker is enough.
 */

const AUTH_CODE = "RYUUMANIFEST-setapikeyforsteamtoolsversion9700";
const BASE = "https://generator.ryuu.lol";

export interface RyuuBundle {
  files: Map<string, Buffer>; // filename → raw bytes
}

const bundleCache = new Map<number, Promise<RyuuBundle>>();

export async function fetchRyuuLua(appId: number, signal?: AbortSignal): Promise<string> {
  const url = `${BASE}/resellerlua?appid=${appId}&auth_code=${AUTH_CODE}`;
  const res = await fetch(url, { signal });
  if (!res.ok) throw new Error(`ryuu resellerlua: HTTP ${res.status}`);
  const text = await res.text();
  if (!/addappid\s*\(/i.test(text)) throw new Error(`ryuu resellerlua: not a lua script`);
  return text;
}

export function fetchRyuuBundle(appId: number, signal?: AbortSignal): Promise<RyuuBundle> {
  const cached = bundleCache.get(appId);
  if (cached) return cached;
  const p = (async () => {
    const url = `${BASE}/secure_download?appid=${appId}&auth_code=${AUTH_CODE}`;
    const res = await fetch(url, { signal });
    if (!res.ok) throw new Error(`ryuu secure_download: HTTP ${res.status}`);
    const buf = Buffer.from(await res.arrayBuffer());
    return parseStoredZip(buf);
  })();
  bundleCache.set(appId, p);
  p.catch(() => bundleCache.delete(appId));
  return p;
}

// Minimal ZIP reader that assumes STORED (method 0) entries, which is what
// ryuu.lol emits. Scans the End-Of-Central-Directory record to find the
// central directory, then walks it to locate each file's local header + data.
function parseStoredZip(buf: Buffer): RyuuBundle {
  const EOCD_SIG = 0x06054b50;
  const CD_SIG = 0x02014b50;
  const LFH_SIG = 0x04034b50;

  // EOCD is within the last 65557 bytes (max comment 65535 + 22 header)
  let eocdOffset = -1;
  const searchStart = Math.max(0, buf.length - 65557);
  for (let i = buf.length - 22; i >= searchStart; i--) {
    if (buf.readUInt32LE(i) === EOCD_SIG) {
      eocdOffset = i;
      break;
    }
  }
  if (eocdOffset < 0) throw new Error("ryuu zip: EOCD not found");

  const entryCount = buf.readUInt16LE(eocdOffset + 10);
  const cdOffset = buf.readUInt32LE(eocdOffset + 16);

  const files = new Map<string, Buffer>();
  let p = cdOffset;
  for (let i = 0; i < entryCount; i++) {
    if (buf.readUInt32LE(p) !== CD_SIG) throw new Error(`ryuu zip: bad CD sig at ${p}`);
    const method = buf.readUInt16LE(p + 10);
    const compSize = buf.readUInt32LE(p + 20);
    const uncompSize = buf.readUInt32LE(p + 24);
    const nameLen = buf.readUInt16LE(p + 28);
    const extraLen = buf.readUInt16LE(p + 30);
    const commentLen = buf.readUInt16LE(p + 32);
    const localOffset = buf.readUInt32LE(p + 42);
    const name = buf.slice(p + 46, p + 46 + nameLen).toString("utf8");
    p += 46 + nameLen + extraLen + commentLen;

    if (method !== 0) throw new Error(`ryuu zip: ${name} uses method ${method}, only STORED supported`);
    if (compSize !== uncompSize) throw new Error(`ryuu zip: ${name} size mismatch`);

    if (buf.readUInt32LE(localOffset) !== LFH_SIG) {
      throw new Error(`ryuu zip: bad local header for ${name}`);
    }
    const lfhNameLen = buf.readUInt16LE(localOffset + 26);
    const lfhExtraLen = buf.readUInt16LE(localOffset + 28);
    const dataStart = localOffset + 30 + lfhNameLen + lfhExtraLen;
    files.set(name, buf.slice(dataStart, dataStart + compSize));
  }
  return { files };
}
