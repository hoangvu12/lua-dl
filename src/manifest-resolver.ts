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

const MIRRORS = [
  "Auiowu/ManifestAutoUpdate",
  "tymolu233/ManifestAutoUpdate-fix",
  "tymolu233/ManifestAutoUpdate",
  "luomojim/ManifestAutoUpdate",
  "BlankTMing/ManifestAutoUpdate",
  "hulovewang/ManifestAutoUpdate",
  "xhcom/ManifestAutoUpdate-R",
];

export interface ResolvedManifest {
  buffer: Buffer;
  source: string;       // which mirror provided it
  bytes: number;
}

export async function resolveManifest(
  appId: number,
  depotId: number,
  manifestId: string
): Promise<ResolvedManifest> {
  const filename = `${depotId}_${manifestId}.manifest`;
  const urls = MIRRORS.map((repo) => ({
    repo,
    url: `https://raw.githubusercontent.com/${repo}/${appId}/${filename}`,
  }));

  console.error(
    `[resolver] racing ${urls.length} mirrors for ${appId}/${filename}`
  );

  const controller = new AbortController();

  const attempts = urls.map(async ({ repo, url }) => {
    const res = await fetch(url, { signal: controller.signal });
    if (!res.ok) throw new Error(`${repo}: HTTP ${res.status}`);
    const buf = Buffer.from(await res.arrayBuffer());
    // Sanity: Steam protobuf manifest magic is 0x71F617D0 (LE)
    if (buf.length < 4 || buf.readUInt32LE(0) !== 0x71f617d0) {
      throw new Error(`${repo}: bad magic 0x${buf.readUInt32LE(0).toString(16)}`);
    }
    return { repo, buf };
  });

  try {
    const { repo, buf } = await Promise.any(attempts);
    controller.abort();
    console.error(
      `[resolver] ✓ ${repo} (${buf.length} bytes)`
    );
    return { buffer: buf, source: repo, bytes: buf.length };
  } catch (err: any) {
    const errors: string[] =
      err?.errors?.map((e: Error) => e.message) ?? [err?.message ?? String(err)];
    throw new Error(
      `All ${urls.length} mirrors failed:\n  - ${errors.join("\n  - ")}`
    );
  }
}
