/**
 * Depot download orchestration.
 *
 * Key trick: SteamUser's entire CDN pipeline (getManifest, downloadChunk,
 * downloadFile) internally calls `client.getDepotDecryptionKey(appId, depotId)`
 * to get the AES key for decrypting chunks + filenames. We override that
 * single method to return keys from the lua file instead of asking Steam
 * (which would fail for apps the anonymous account doesn't own).
 */

import { mkdirSync, existsSync, rmSync } from "node:fs";
import { dirname, join } from "node:path";
import { createRequire } from "node:module";
import type SteamUser from "steam-user";
import type { DepotEntry } from "./lua";
import { resolveManifest } from "./manifest-resolver";

// steam-user doesn't re-export ContentManifest, but it lives in components/.
// We reach in directly — same module the getManifest helper uses internally.
const require = createRequire(import.meta.url);
const ContentManifest: {
  parse: (buffer: Buffer) => any;
  decryptFilenames: (manifest: any, key: Buffer) => void;
} = require("steam-user/components/content_manifest.js");

export function injectDepotKeys(
  client: SteamUser,
  depots: DepotEntry[]
): void {
  const keyMap = new Map<number, Buffer>();
  for (const d of depots) {
    if (d.key) keyMap.set(d.id, Buffer.from(d.key, "hex"));
  }

  const orig = (client as any).getDepotDecryptionKey.bind(client);
  (client as any).getDepotDecryptionKey = async (appID: number, depotID: number) => {
    const injected = keyMap.get(Number(depotID));
    if (injected) {
      return { key: injected };
    }
    console.error(
      `[inject] no lua key for depot ${depotID}, falling back to Steam API`
    );
    return orig(appID, depotID);
  };

  console.error(`[inject] depot keys loaded: ${[...keyMap.keys()].join(", ")}`);
}

interface ManifestFile {
  filename: string;
  size: string | number;
  flags: number;
  chunks: Array<{ sha: string; offset: string | number; cb_original: number }>;
  sha_content?: string;
}

interface ParsedManifest {
  files: ManifestFile[];
  filenames_encrypted: boolean;
  depot_id: number;
  manifest_id: string;
}

const FLAG_DIRECTORY = 64;

export async function downloadDepot(
  client: SteamUser,
  appId: number,
  depotId: number,
  manifestId: string,
  outputDir: string
): Promise<void> {
  console.error(
    `\n[download] app=${appId} depot=${depotId} manifest=${manifestId}`
  );

  // Fetch pre-cached manifest binary from a mirror (skips GetManifestRequestCode)
  const { buffer: manifestBuf, source } = await resolveManifest(
    appId,
    depotId,
    manifestId
  );
  console.error(`[download] parsing manifest (from ${source})...`);

  const manifest: ParsedManifest = ContentManifest.parse(manifestBuf);

  if (manifest.filenames_encrypted) {
    const keyHex = (() => {
      // Pull key out of the injected map by forcing a key lookup via the patched method
      return undefined;
    })();
    // Filenames are AES-encrypted with the depot key. Decrypt using our injected key.
    const injectedKey: Buffer = (
      await (client as any).getDepotDecryptionKey(appId, depotId)
    ).key;
    ContentManifest.decryptFilenames(manifest, injectedKey);
    console.error(`[download] filenames decrypted`);
  }

  const files = manifest.files.filter((f) => !(f.flags & FLAG_DIRECTORY));
  const totalSize = files.reduce((s, f) => s + Number(f.size), 0);
  console.error(
    `[download] manifest: ${manifest.files.length} entries (${files.length} files, ${(totalSize / 1e6).toFixed(1)} MB)`
  );

  // Pre-create directories
  mkdirSync(outputDir, { recursive: true });

  // Normalize path separators from Steam's backslashes
  const normalizePath = (p: string) => p.replace(/\\/g, "/");

  let done = 0;
  let bytes = 0;
  const start = Date.now();
  const CONCURRENCY = 16;

  // Pre-create all directories up front (avoids contention)
  const dirs = new Set<string>();
  for (const f of files) dirs.add(dirname(join(outputDir, normalizePath(f.filename))));
  for (const d of dirs) mkdirSync(d, { recursive: true });

  const queue = [...files];
  let lastLogged = 0;

  async function worker() {
    while (queue.length) {
      const file = queue.shift();
      if (!file) return;
      const rel = normalizePath(file.filename);
      const outPath = join(outputDir, rel);
      const size = Number(file.size);

      if (size === 0) {
        await Bun.write(outPath, new Uint8Array(0));
      } else {
        try {
          await (client as any).downloadFile(appId, depotId, file, outPath);
        } catch (err: any) {
          console.error(`[download] FAILED ${rel}: ${err?.message ?? err}`);
          throw err;
        }
      }

      done++;
      bytes += size;

      // Throttle progress logging (every ~1% or every 25 files)
      const now = Date.now();
      if (done === files.length || done - lastLogged >= 25 || now - start < 2000) {
        lastLogged = done;
        const pct = ((bytes / totalSize) * 100).toFixed(1);
        const mb = (bytes / 1e6).toFixed(1);
        const elapsed = (now - start) / 1000;
        const mbps = (bytes / 1e6 / elapsed).toFixed(1);
        console.error(
          `[download] [${done}/${files.length}] ${pct}% ${mb}MB @ ${mbps}MB/s  ${rel}`
        );
      }
    }
  }

  await Promise.all(Array.from({ length: CONCURRENCY }, () => worker()));

  console.error(
    `\n[download] ✅ depot ${depotId} complete — ${done} files, ${(bytes / 1e6).toFixed(1)} MB in ${((Date.now() - start) / 1000).toFixed(1)}s`
  );
}
