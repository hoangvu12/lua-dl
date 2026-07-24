/**
 * Online-Fix catalog search, backed by the prebuilt of-catalog manifest.
 *
 * Instead of scraping online-fix.me for every /of query, we page/search a
 * GitHub-hosted manifest in memory (the same source the ina desktop app uses).
 * It carries the article id, title, Steam app id, cover art, and version for
 * each game — everything the picker embeds need. The manifest is cached for
 * 6 hours and served stale on a fetch error so a GitHub blip can't break search.
 *
 * The article `id` is what the generated .bat passes to `lua-dl of <id>`.
 */

const BROWSE_URL =
  "https://raw.githubusercontent.com/hoangvu12/of-catalog/main/data/browse.json";
const SITE_URL = "https://online-fix.me";
const CACHE_TTL_MS = 6 * 60 * 60 * 1000;

export interface OfCatalogGame {
  id: string; // online-fix article id — passed to `lua-dl of <id>`
  title: string;
  originUrl: string; // full online-fix.me article URL
  version?: string;
  steamAppId?: number;
  coverUrl?: string;
  updatedAt?: string;
}

interface RemoteGame {
  id: string;
  title: string;
  originPath: string;
  version?: string;
  steamAppId?: number;
  coverUrl?: string;
  updatedAt?: string;
}

let cache: { at: number; games: OfCatalogGame[] } | null = null;

function toGame(g: RemoteGame): OfCatalogGame {
  const originUrl = /^https?:\/\//.test(g.originPath)
    ? g.originPath
    : g.originPath.startsWith("/")
      ? `${SITE_URL}${g.originPath}`
      : `${SITE_URL}/${g.originPath}`;
  return {
    id: g.id,
    title: g.title,
    originUrl,
    version: g.version,
    steamAppId: g.steamAppId,
    coverUrl: g.coverUrl,
    updatedAt: g.updatedAt,
  };
}

async function loadCatalog(): Promise<OfCatalogGame[]> {
  if (cache && Date.now() - cache.at < CACHE_TTL_MS) return cache.games;
  try {
    const res = await fetch(BROWSE_URL, { signal: AbortSignal.timeout(8000) });
    if (!res.ok) throw new Error(`browse.json HTTP ${res.status}`);
    const data = (await res.json()) as { games?: RemoteGame[] };
    const games = (data.games ?? [])
      .filter((g) => g && g.id && g.title && g.originPath)
      .map(toGame);
    cache = { at: Date.now(), games };
    return games;
  } catch (err) {
    if (cache) return cache.games; // stale-on-error
    throw err;
  }
}

// scoreMatch ranks a game against the query. Higher wins; 0 means no match.
function scoreMatch(g: OfCatalogGame, q: string, appid: number | null): number {
  const title = g.title.toLowerCase();
  if (appid !== null) {
    if (g.steamAppId === appid) return 1000;
    if (g.id === String(appid)) return 999;
  }
  if (title === q) return 500;
  if (title.startsWith(q)) return 300;
  if (title.includes(q)) return 100;
  const tokens = q.split(/\s+/).filter(Boolean);
  if (tokens.length > 1 && tokens.every((t) => title.includes(t))) return 80;
  return 0;
}

/**
 * Searches the catalog for `query` (game name, Steam app id, or article id).
 * Returns up to `limit` matches ordered best-first. Throws only if the manifest
 * can't be loaded at all (no cache to fall back on).
 */
export async function searchOfCatalog(
  query: string,
  limit = 5
): Promise<OfCatalogGame[]> {
  const q = query.trim().toLowerCase();
  if (!q) return [];
  const games = await loadCatalog();
  const appid = /^\d+$/.test(q) ? Number(q) : null;
  return games
    .map((g) => ({ g, score: scoreMatch(g, q, appid) }))
    .filter((s) => s.score > 0)
    .sort((a, b) => b.score - a.score)
    .slice(0, limit)
    .map((s) => s.g);
}

/** Looks up a single catalog game by its online-fix article id. */
export async function getOfGameById(id: string): Promise<OfCatalogGame | null> {
  const games = await loadCatalog();
  return games.find((g) => g.id === id) ?? null;
}
