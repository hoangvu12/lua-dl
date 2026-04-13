/**
 * Monkey-patch steam-user's CdnCompression.unzip to offload LZMA decompression
 * to a pool of worker threads.
 *
 * Why: pure-JS LZMA decompresses at ~1.4 MB/s single-threaded. That's our
 * observed download ceiling on a fast network because every chunk funnels
 * through CdnCompression.unzip on the main event loop. Moving the work to
 * N worker threads gives near-linear speedup per core until the network
 * catches up.
 *
 * Zstd (VSZa) + zip (PK) stay on the main thread — zstd is already native
 * via zstddec and the zip path is rare.
 *
 * Must be imported BEFORE steam-user loads its cdn module.
 */

import { Worker } from "node:worker_threads";
import os from "node:os";
// Static import so bun's bundler inlines this module and we share the same
// instance with steam-user's internal cdn.js (which does require('./cdn_compression.js')).
// Using createRequire here breaks bun --compile: the runtime require resolves
// against the real filesystem and drags in an unbundled helpers.js.
import CdnCompression from "steam-user/components/cdn_compression.js";

const HEADER_VZIP = "VZa";

const WORKER_COUNT = Math.max(2, Math.min(16, os.cpus().length - 2));
// `new URL(path, import.meta.url)` lets bun's bundler statically detect
// and embed the worker file into the compiled exe.
const workerUrl = new URL("./lzma-worker.ts", import.meta.url);

interface Pending {
  resolve: (buf: Buffer) => void;
  reject: (err: Error) => void;
}

interface PoolWorker {
  worker: Worker;
  busy: boolean;
}

const pool: PoolWorker[] = [];
const pending = new Map<number, Pending>();
let nextId = 0;
const queue: Array<{ id: number; data: Buffer }> = [];

function initPool() {
  for (let i = 0; i < WORKER_COUNT; i++) {
    const w = new Worker(workerUrl);
    const pw: PoolWorker = { worker: w, busy: false };
    w.on("message", (msg: any) => {
      pw.busy = false;
      const p = pending.get(msg.id);
      if (!p) return;
      pending.delete(msg.id);
      if (msg.ok) {
        p.resolve(Buffer.from(msg.result.buffer, msg.result.byteOffset, msg.result.byteLength));
      } else {
        p.reject(new Error(msg.error));
      }
      pump();
    });
    w.on("error", (err) => {
      // Worker crashed — reject all pending and reinit is out of scope.
      console.error("[cdn-patch] worker error:", err);
    });
    pool.push(pw);
  }
}

function pump() {
  while (queue.length > 0) {
    const free = pool.find((p) => !p.busy);
    if (!free) return;
    const job = queue.shift()!;
    free.busy = true;
    // Transfer the ArrayBuffer slice — avoids a copy on the way in.
    const ab = job.data.buffer.slice(
      job.data.byteOffset,
      job.data.byteOffset + job.data.byteLength
    );
    free.worker.postMessage({ id: job.id, data: ab }, [ab]);
  }
}

function decompressVzipViaPool(data: Buffer): Promise<Buffer> {
  return new Promise((resolve, reject) => {
    const id = nextId++;
    pending.set(id, { resolve, reject });
    queue.push({ id, data });
    pump();
  });
}

initPool();

export function shutdownLzmaPool(): void {
  for (const pw of pool) pw.worker.terminate();
  pool.length = 0;
}

const origUnzip = CdnCompression.unzip;

CdnCompression.unzip = async function patchedUnzip(data: Buffer): Promise<Buffer> {
  const header = data.slice(0, 3).toString("utf8");
  if (header === HEADER_VZIP) {
    return decompressVzipViaPool(data);
  }
  return origUnzip(data);
};

console.error(`[cdn-patch] LZMA worker pool: ${WORKER_COUNT} threads`);
