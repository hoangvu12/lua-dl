/**
 * Thin wrapper for api.steamcmd.net — a public mirror that runs `steamcmd`
 * against Steam and returns the same PICS data our Go CLI reads natively.
 * We use it for per-app install/download sizes, which neither `appdetails`
 * nor `storesearch` expose.
 *
 * No auth, no cookies, no Cloudflare. Data lags real Steam by a few seconds.
 *
 * We filter depots to the ones a Windows user would actually download:
 *   - depots with oslist containing "windows", OR
 *   - depots with no oslist (platform-agnostic base content)
 * and skip depots tagged with dlcappid (those belong to optional DLC apps
 * and are fetched via their own appid separately).
 */

export interface AppSizes {
  installBytes: number; // sum of manifests.public.size — shown to users
  downloadBytes: number; // sum of manifests.public.download — actual bandwidth
}

interface ScmdDepotManifest {
  size?: string;
  download?: string;
  gid?: string;
}

interface ScmdDepot {
  config?: { oslist?: string };
  dlcappid?: string;
  manifests?: { public?: ScmdDepotManifest };
}

interface ScmdResponse {
  data?: Record<
    string,
    {
      appid?: string;
      depots?: Record<string, ScmdDepot | string>;
    }
  >;
}

const cache = new Map<number, AppSizes | null>();

export async function fetchAppSizes(appid: number): Promise<AppSizes | null> {
  if (cache.has(appid)) return cache.get(appid) ?? null;
  try {
    const res = await fetch(`https://api.steamcmd.net/v1/info/${appid}`, {
      signal: AbortSignal.timeout(6000),
    });
    if (!res.ok) {
      cache.set(appid, null);
      return null;
    }
    const json = (await res.json()) as ScmdResponse;
    const app = json.data?.[String(appid)];
    const depots = app?.depots;
    if (!depots || typeof depots !== "object") {
      cache.set(appid, null);
      return null;
    }

    let installBytes = 0;
    let downloadBytes = 0;
    for (const [key, raw] of Object.entries(depots)) {
      // Skip non-numeric keys like "branches", "baselanguages", "overridescddb".
      if (!/^\d+$/.test(key)) continue;
      if (typeof raw !== "object" || raw === null) continue;
      const d = raw as ScmdDepot;
      // DLC-owned depots are fetched under the DLC's appid, not the base game.
      if (d.dlcappid) continue;
      const oslist = d.config?.oslist;
      // Accept platform-agnostic (empty oslist) and anything listing windows.
      if (oslist && !oslist.split(",").includes("windows")) continue;
      const pub = d.manifests?.public;
      if (!pub) continue;
      const size = Number(pub.size);
      const dl = Number(pub.download);
      if (Number.isFinite(size)) installBytes += size;
      if (Number.isFinite(dl)) downloadBytes += dl;
    }
    const out: AppSizes | null =
      installBytes > 0 || downloadBytes > 0
        ? { installBytes, downloadBytes }
        : null;
    cache.set(appid, out);
    return out;
  } catch {
    cache.set(appid, null);
    return null;
  }
}

// Human-readable size, SI-ish (1000 not 1024) to match what Steam itself
// displays in the store ("1.2 GB").
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let n = bytes;
  let i = 0;
  while (n >= 1000 && i < units.length - 1) {
    n /= 1000;
    i++;
  }
  const digits = n >= 100 ? 0 : n >= 10 ? 1 : 2;
  return `${n.toFixed(digits)} ${units[i]}`;
}
