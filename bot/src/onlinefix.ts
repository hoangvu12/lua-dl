/**
 * Checks whether a game has an online-fix (community multiplayer patch)
 * available on online-fix.me.
 *
 * Mirrors the Go CLI's search logic: hit the site's search endpoint with
 * baked subscriber cookies and look for game-page links in the HTML.
 *
 * The site uses windows-1251 encoding — we decode it with TextDecoder.
 * Results are cached for 10 minutes to stay polite.
 */

// Baked subscriber session — same as the Go CLI.
// Rotate when errors start appearing.
const SESSION_COOKIES =
  "SITE_TOTAL_ID=6855e127e2eb867851ee3c2b764f0137; dle_user_id=4600624; dle_password=0465b7c359b0776c82bf6b9a3e02387a; PHPSESSID=psjpb0qj2tfc3kjsj64hdb1n62; cf_clearance=hryVqoCYBF29gbQ6an6Wu6_9dgS3f3Y1vx.E3aucDBE-1776138584-1.2.1.1-4.TwpNjkPm.1v1ye06wAit3vEQquBZ7Yde5lxh6uJ2uKogaJlQxsYyZSdAy1bKfXskm5gjK70i2o0LSpC.ilmaWYT7te4j41IJ4.k_qSoznTyPo9jmqUcQsCFZL8MwohytihnZqeTa5nJ56pWM6G3V8ZpLPyVwu5G_AusASDKXEJdKK3mtQHEJW6SWshuRzjzbK16fFPOP932vdWylM7Osto3GHVGSyNYsBA2WJinR1vnCW4U4nKunI65CKk.JjZmsElDxKTiH.DnK9VnqfqSiF3CE7zUD3Dc9oXjfaXF0LjFV9mrRzamI9NowPV9URMSTSW51MHkf08PV4PPVSwDw";
const USER_AGENT =
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36";
const SITE_URL = "https://online-fix.me";

// Matches game-page links inside <h2 class="title"> — same regex as the Go CLI.
const SEARCH_LINK_RE =
  /<a[^>]+href="(https?:\/\/online-fix\.me\/games\/[^"]+?\.html)"[^>]*>\s*<h2[^>]*class="title"[^>]*>\s*([^<]+?)\s*<\/h2>/gs;

export interface OnlineFixMatch {
  title: string;
  url: string;
}

const cache = new Map<string, { at: number; matches: OnlineFixMatch[] }>();
const CACHE_TTL_MS = 10 * 60 * 1000;
const MAX_MATCHES = 10; // same cap as the Go CLI

/**
 * Searches online-fix.me for fixes matching `gameName`. Returns up to 10
 * unique matches (site search is fuzzy — "Portal" returns Portal, Portal 2,
 * Portal Stories: Mel, etc.). Never throws — returns [] on any error so a
 * broken lookup can't block the picker.
 */
export async function searchOnlineFix(
  gameName: string
): Promise<OnlineFixMatch[]> {
  const key = gameName.trim().toLowerCase();
  if (!key) return [];

  const hit = cache.get(key);
  if (hit && Date.now() - hit.at < CACHE_TTL_MS) return hit.matches;

  try {
    const url = `${SITE_URL}/index.php?do=search&subaction=search&story=${encodeURIComponent(key)}`;
    const res = await fetch(url, {
      headers: {
        "User-Agent": USER_AGENT,
        Cookie: SESSION_COOKIES,
        Referer: `${SITE_URL}/`,
      },
      signal: AbortSignal.timeout(6000),
    });
    if (!res.ok) {
      cache.set(key, { at: Date.now(), matches: [] });
      return [];
    }

    // The site serves windows-1251 but the search-result URLs and our
    // regex anchors are all ASCII, so a raw text read is fine for counting
    // matches. Titles may render slightly mangled for Cyrillic content,
    // but that's cosmetic — we're primarily showing the count.
    const html = await res.text();
    const seen = new Set<string>();
    const matches: OnlineFixMatch[] = [];
    for (const m of html.matchAll(SEARCH_LINK_RE)) {
      const u = m[1];
      if (seen.has(u)) continue;
      seen.add(u);
      matches.push({ title: m[2].trim(), url: u });
      if (matches.length >= MAX_MATCHES) break;
    }
    cache.set(key, { at: Date.now(), matches });
    return matches;
  } catch {
    cache.set(key, { at: Date.now(), matches: [] });
    return [];
  }
}
