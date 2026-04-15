/**
 * Steam store search wrapper.
 *
 * Two-layer model: storesearch gives us a fuzzy-match shortlist, then
 * appdetails resolves each hit to its real app graph (type, parent, children).
 * We use that to pivot soundtrack / DLC hits back to their parent game and
 * dedupe, so searching "yapyap" returns one row (the game) with the OST
 * offered as a child checkbox, not two sibling rows.
 *
 * Steam app types we care about (from appdetails.type):
 *   - "game"      → normal root
 *   - "dlc"       → child; has fullgame.appid pointing at parent
 *   - "music"     → soundtrack; usually has fullgame, sometimes standalone
 *   - "demo"      → child of a game
 *   - "application" / "video" / others → passed through as-is
 *
 * Children of a game are listed in parent.dlc[] (a flat array mixing real
 * DLC, soundtracks, demos — you have to look up each one to classify it).
 *
 * Results are cached for 5 minutes to stay polite to Steam and keep replies
 * snappy.
 */

import { fetchAppSizes } from "./steamcmd-net";

export type AppType = "game" | "dlc" | "music" | "demo" | string;

export interface AppChild {
  id: number;
  name: string;
  type: AppType;
  installBytes?: number;
}

export interface SteamSearchResult {
  id: number; // root appid after pivot
  name: string;
  headerImage: string;
  priceText: string;
  platforms: string;
  type: AppType;
  children: AppChild[];
  installBytes?: number;
}

interface StoreSearchRaw {
  items?: Array<{
    type: string;
    name: string;
    id: number;
    tiny_image?: string;
    price?: { currency: string; final: number };
    platforms?: { windows?: boolean; mac?: boolean; linux?: boolean };
  }>;
}

interface AppDetails {
  name: string;
  type: AppType;
  headerImage: string;
  fullgameAppid?: number;
  dlc: number[];
}

const resultCache = new Map<
  string,
  { at: number; items: SteamSearchResult[] }
>();
const RESULT_TTL_MS = 5 * 60 * 1000;

const detailsCache = new Map<number, AppDetails | null>();

export async function searchSteamApps(
  query: string,
  limit = 10
): Promise<SteamSearchResult[]> {
  const key = query.trim().toLowerCase();
  if (!key) return [];
  const hit = resultCache.get(key);
  if (hit && Date.now() - hit.at < RESULT_TTL_MS)
    return hit.items.slice(0, limit);

  const url = `https://store.steampowered.com/api/storesearch/?term=${encodeURIComponent(
    key
  )}&l=en&cc=us`;
  const res = await fetch(url, { signal: AbortSignal.timeout(5000) });
  if (!res.ok) throw new Error(`store search HTTP ${res.status}`);
  const raw = (await res.json()) as StoreSearchRaw;

  // Storesearch `type` is unreliable for filtering (soundtracks come back as
  // "app"), so we take everything app-shaped and let appdetails classify.
  const candidates = (raw.items ?? [])
    .filter((it) => it.type === "app" && it.id > 0)
    .slice(0, 20)
    .map((it) => ({
      id: it.id,
      name: it.name,
      priceText: formatPrice(it.price),
      platforms: formatPlatforms(it.platforms),
    }));

  // Pivot each candidate to its root (fullgame if it's a child) and dedupe.
  // Fetch details in parallel.
  const rootIds: number[] = [];
  const candidateMeta = new Map<
    number,
    { priceText: string; platforms: string }
  >();
  await Promise.all(
    candidates.map(async (c) => {
      const det = await fetchAppDetails(c.id);
      if (!det) return;
      const rootId = det.fullgameAppid ?? c.id;
      if (!candidateMeta.has(rootId)) {
        rootIds.push(rootId);
        candidateMeta.set(rootId, {
          priceText: c.priceText,
          platforms: c.platforms,
        });
      }
    })
  );

  // Fetch root details (usually already cached from the pivot pass) + their
  // children in parallel. One batch of appdetails calls per unique appid.
  // Sizes come from steamcmd.net in parallel — best-effort, missing size
  // just means we don't show it.
  const results: SteamSearchResult[] = [];
  await Promise.all(
    rootIds.map(async (id) => {
      const [det, sizes] = await Promise.all([
        fetchAppDetails(id),
        fetchAppSizes(id),
      ]);
      if (!det) return;
      const meta = candidateMeta.get(id)!;
      const children = await fetchChildren(det.dlc);
      results.push({
        id,
        name: det.name,
        headerImage: det.headerImage,
        priceText: meta.priceText,
        platforms: meta.platforms,
        type: det.type,
        children,
        installBytes: sizes?.installBytes,
      });
    })
  );

  // Preserve storesearch ordering as best we can (Map preserved insertion).
  const ordered = rootIds
    .map((id) => results.find((r) => r.id === id))
    .filter((r): r is SteamSearchResult => !!r);

  resultCache.set(key, { at: Date.now(), items: ordered });
  return ordered.slice(0, limit);
}

export async function fetchAppDetails(
  appid: number
): Promise<AppDetails | null> {
  if (detailsCache.has(appid)) return detailsCache.get(appid) ?? null;
  try {
    const res = await fetch(
      `https://store.steampowered.com/api/appdetails?appids=${appid}&cc=us&l=en&filters=basic`,
      { signal: AbortSignal.timeout(5000) }
    );
    if (!res.ok) {
      detailsCache.set(appid, null);
      return null;
    }
    const json = (await res.json()) as Record<
      string,
      {
        success?: boolean;
        data?: {
          name?: string;
          type?: string;
          header_image?: string;
          fullgame?: { appid?: string | number };
          dlc?: number[];
        };
      }
    >;
    const entry = json[String(appid)];
    if (!entry?.success || !entry.data) {
      detailsCache.set(appid, null);
      return null;
    }
    const d = entry.data;
    const parentRaw = d.fullgame?.appid;
    const parent =
      typeof parentRaw === "string"
        ? Number(parentRaw)
        : typeof parentRaw === "number"
          ? parentRaw
          : undefined;
    const details: AppDetails = {
      name: d.name ?? `App ${appid}`,
      type: (d.type ?? "game") as AppType,
      headerImage: d.header_image ?? "",
      fullgameAppid: Number.isFinite(parent) && parent! > 0 ? parent : undefined,
      dlc: Array.isArray(d.dlc) ? d.dlc.filter((x) => Number.isFinite(x)) : [],
    };
    detailsCache.set(appid, details);
    return details;
  } catch {
    detailsCache.set(appid, null);
    return null;
  }
}

async function fetchChildren(dlcIds: number[]): Promise<AppChild[]> {
  if (dlcIds.length === 0) return [];
  // Cap at a reasonable number — some AAA games list 50+ DLCs and we only
  // have a StringSelectMenu with 25 options (including the base game row).
  const capped = dlcIds.slice(0, 24);
  const children = await Promise.all(
    capped.map(async (id): Promise<AppChild | null> => {
      const [det, sizes] = await Promise.all([
        fetchAppDetails(id),
        fetchAppSizes(id),
      ]);
      if (!det) return null;
      return {
        id,
        name: det.name,
        type: det.type,
        installBytes: sizes?.installBytes,
      };
    })
  );
  return children.filter((c): c is AppChild => !!c);
}

function formatPrice(p?: { currency: string; final: number }): string {
  if (!p) return "Free";
  return `${(p.final / 100).toFixed(2)} ${p.currency}`;
}

function formatPlatforms(p?: {
  windows?: boolean;
  mac?: boolean;
  linux?: boolean;
}): string {
  if (!p) return "";
  const parts: string[] = [];
  if (p.windows) parts.push("Win");
  if (p.mac) parts.push("Mac");
  if (p.linux) parts.push("Linux");
  return parts.join("/");
}
