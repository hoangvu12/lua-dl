/**
 * Steam store search wrapper. Uses the public storesearch endpoint — no auth,
 * no key, returns JSON. Caches results for 5 minutes to stay polite to Steam
 * and keep Discord replies snappy.
 */

export interface SteamSearchResult {
  id: number;
  name: string;
  headerImage: string;
  priceText: string;
  platforms: string;
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

const cache = new Map<string, { at: number; items: SteamSearchResult[] }>();
const TTL_MS = 5 * 60 * 1000;

export async function searchSteamApps(
  query: string,
  limit = 10
): Promise<SteamSearchResult[]> {
  const key = query.trim().toLowerCase();
  if (!key) return [];
  const hit = cache.get(key);
  if (hit && Date.now() - hit.at < TTL_MS) return hit.items.slice(0, limit);

  const url = `https://store.steampowered.com/api/storesearch/?term=${encodeURIComponent(
    key
  )}&l=en&cc=us`;
  const res = await fetch(url, {
    signal: AbortSignal.timeout(5000),
  });
  if (!res.ok) throw new Error(`store search HTTP ${res.status}`);
  const raw = (await res.json()) as StoreSearchRaw;

  const items: SteamSearchResult[] = (raw.items ?? [])
    .filter((it) => it.type === "app" && it.id > 0)
    .slice(0, 25)
    .map((it) => ({
      id: it.id,
      name: it.name,
      // Filled in below via appdetails — the storesearch API only returns
      // a 231x87 capsule, too small for a nice picker embed.
      headerImage: "",
      priceText: formatPrice(it.price),
      platforms: formatPlatforms(it.platforms),
    }));

  // Fetch proper header images in parallel. appdetails returns header_image
  // which is 460x215 and renders nicely in a Discord embed.
  await Promise.all(
    items.map(async (it) => {
      it.headerImage = await fetchHeaderImage(it.id);
    })
  );

  cache.set(key, { at: Date.now(), items });
  return items.slice(0, limit);
}

const headerCache = new Map<number, string>();

async function fetchHeaderImage(appid: number): Promise<string> {
  const cached = headerCache.get(appid);
  if (cached !== undefined) return cached;
  try {
    const res = await fetch(
      `https://store.steampowered.com/api/appdetails?appids=${appid}&cc=us&l=en&filters=basic`,
      { signal: AbortSignal.timeout(4000) }
    );
    if (!res.ok) return "";
    const json = (await res.json()) as Record<
      string,
      { success?: boolean; data?: { header_image?: string } }
    >;
    const url = json[String(appid)]?.data?.header_image ?? "";
    headerCache.set(appid, url);
    return url;
  } catch {
    return "";
  }
}

function formatPrice(p?: { currency: string; final: number }): string {
  if (!p) return "Free";
  return `${(p.final / 100).toFixed(2)} ${p.currency}`;
}

function formatPlatforms(
  p?: { windows?: boolean; mac?: boolean; linux?: boolean }
): string {
  if (!p) return "";
  const parts: string[] = [];
  if (p.windows) parts.push("Win");
  if (p.mac) parts.push("Mac");
  if (p.linux) parts.push("Linux");
  return parts.join("/");
}
