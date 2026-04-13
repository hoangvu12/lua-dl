/**
 * Depot download orchestration.
 *
 * Key trick: SteamUser's entire CDN pipeline (getManifest, downloadChunk,
 * downloadFile) internally calls `client.getDepotDecryptionKey(appId, depotId)`
 * to get the AES key for decrypting chunks + filenames. We override that
 * single method to return keys from the lua file instead of asking Steam
 * (which would fail for apps the anonymous account doesn't own).
 */

import { mkdirSync, existsSync, statSync, renameSync, rmSync } from "node:fs";
import { dirname, join } from "node:path";
import { createHash } from "node:crypto";
import type SteamUser from "steam-user";
import type { DepotEntry } from "./lua";
import { resolveManifest } from "./manifest-resolver";
import { VERBOSE, vlog, statusLine, statusDone } from "./verbose";
import { StateCache, toHex } from "./state";
// Static import so bun --compile bundles this. createRequire escapes the
// bundler and blows up at runtime.
import ContentManifestModule from "steam-user/components/content_manifest.js";

const ContentManifest: {
  parse: (buffer: Buffer) => any;
  decryptFilenames: (manifest: any, key: Buffer) => void;
} = ContentManifestModule as any;

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
    vlog(
      `[inject] no lua key for depot ${depotID}, falling back to Steam API`
    );
    return orig(appID, depotID);
  };

  vlog(`[inject] depot keys loaded: ${[...keyMap.keys()].join(", ")}`);
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

async function sha1File(path: string): Promise<string> {
  const hash = createHash("sha1");
  const file = Bun.file(path);
  const stream = file.stream();
  // @ts-ignore — Bun ReadableStream async iterable
  for await (const chunk of stream) hash.update(chunk);
  return hash.digest("hex");
}

export async function downloadDepot(
  client: SteamUser,
  appId: number,
  depotId: number,
  manifestId: string,
  outputDir: string,
  state: StateCache
): Promise<void> {
  vlog(
    `\n[download] app=${appId} depot=${depotId} manifest=${manifestId}`
  );

  // Fetch pre-cached manifest binary from a mirror (skips GetManifestRequestCode)
  const { buffer: manifestBuf, source } = await resolveManifest(
    appId,
    depotId,
    manifestId
  );
  vlog(`[download] parsing manifest (from ${source})...`);

  const manifest: ParsedManifest = ContentManifest.parse(manifestBuf);

  if (manifest.filenames_encrypted) {
    const injectedKey: Buffer = (
      await (client as any).getDepotDecryptionKey(appId, depotId)
    ).key;
    ContentManifest.decryptFilenames(manifest, injectedKey);
    if (VERBOSE) console.error(`[download] filenames decrypted`);
  }

  const files = manifest.files.filter((f) => !(f.flags & FLAG_DIRECTORY));
  const totalSize = files.reduce((s, f) => s + Number(f.size), 0);
  console.error(
    `[download] depot ${depotId}: ${files.length} files, ${(totalSize / 1e6).toFixed(1)} MB`
  );

  // Pre-create directories
  mkdirSync(outputDir, { recursive: true });

  // Normalize path separators from Steam's backslashes
  const normalizePath = (p: string) => p.replace(/\\/g, "/");

  let done = 0;
  let bytes = 0;
  let skipped = 0;
  let skippedBytes = 0;
  const start = Date.now();
  const CONCURRENCY = 24;

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
      const partPath = outPath + ".partial";
      const size = Number(file.size);
      const manifestSha = toHex(file.sha_content);

      // Resume check: existing file, size match, sha match → skip
      if (size > 0 && manifestSha && existsSync(outPath)) {
        const st = statSync(outPath);
        if (st.size === size) {
          const cached = state.get(depotId, manifestId, rel);
          let sha: string | undefined;
          if (
            cached &&
            cached.size === size &&
            cached.mtime === st.mtimeMs &&
            cached.sha1 === manifestSha
          ) {
            sha = cached.sha1;
          } else {
            sha = await sha1File(outPath);
            if (sha === manifestSha) {
              state.set(depotId, manifestId, rel, {
                size,
                sha1: sha,
                mtime: st.mtimeMs,
              });
            }
          }
          if (sha === manifestSha) {
            done++;
            skipped++;
            bytes += size;
            skippedBytes += size;
            continue;
          }
        }
      }

      if (size === 0) {
        await Bun.write(outPath, new Uint8Array(0));
      } else {
        const MAX_ATTEMPTS = 4;
        let lastErr: any;
        for (let attempt = 1; attempt <= MAX_ATTEMPTS; attempt++) {
          try {
            if (existsSync(partPath)) rmSync(partPath);
            await (client as any).downloadFile(appId, depotId, file, partPath);
            renameSync(partPath, outPath);
            if (manifestSha) {
              const st = statSync(outPath);
              state.set(depotId, manifestId, rel, {
                size,
                sha1: manifestSha,
                mtime: st.mtimeMs,
              });
            }
            lastErr = undefined;
            break;
          } catch (err: any) {
            lastErr = err;
            const msg = err?.message ?? String(err);
            if (attempt < MAX_ATTEMPTS) {
              const delay = 500 * 2 ** (attempt - 1);
              vlog(`[retry ${attempt}/${MAX_ATTEMPTS - 1}] ${rel}: ${msg} — waiting ${delay}ms`);
              await new Promise((r) => setTimeout(r, delay));
            }
          }
        }
        if (lastErr) {
          console.error(`\n[download] FAILED ${rel} after ${MAX_ATTEMPTS} attempts: ${lastErr?.message ?? lastErr}`);
          throw lastErr;
        }
      }

      done++;
      bytes += size;

      const now = Date.now();
      if (done === files.length || done - lastLogged >= 10 || now - start < 2000) {
        lastLogged = done;
        const pct = ((bytes / totalSize) * 100).toFixed(1);
        const mb = (bytes / 1e6).toFixed(1);
        const elapsed = (now - start) / 1000;
        const mbps = (bytes / 1e6 / elapsed).toFixed(1);
        statusLine(
          `[${done}/${files.length}] ${pct}% ${mb}MB @ ${mbps}MB/s  ${rel.slice(-60)}`
        );
      }
    }
  }

  await Promise.all(Array.from({ length: CONCURRENCY }, () => worker()));
  statusDone();

  const skipInfo = skipped > 0 ? ` (${skipped} skipped, ${(skippedBytes / 1e6).toFixed(1)} MB)` : "";
  console.error(
    `[download] depot ${depotId} done — ${done} files, ${(bytes / 1e6).toFixed(1)} MB in ${((Date.now() - start) / 1000).toFixed(1)}s${skipInfo}`
  );
  state.flush();
}
