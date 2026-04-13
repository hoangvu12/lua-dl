/**
 * Worker thread that decompresses LZMA_ALONE payloads using pure-JS `lzma`.
 * Runs off the main event loop so decompression isn't a throughput bottleneck.
 *
 * Protocol:
 *   in:  { id, data: ArrayBuffer }   (Steam VZip-wrapped chunk)
 *   out: { id, ok: true, result: Uint8Array }  | { id, ok: false, error: string }
 */

import { parentPort } from "node:worker_threads";
// Static import so bun --compile bundles this worker correctly.
import pureLzma from "lzma";

if (!parentPort) throw new Error("lzma-worker must be spawned as a worker");

const HEADER_VZIP = "VZa";
const FOOTER_VZIP = "zv";

function decompressVzip(data: Buffer): Buffer {
  if (data.slice(0, 3).toString("utf8") !== HEADER_VZIP) {
    throw new Error("VZip: bad header");
  }
  const properties = data.slice(7, 12);
  const footerStart = data.length - 10;
  const decompressedSize = data.readUInt32LE(footerStart + 4);
  if (data.slice(data.length - 2).toString("utf8") !== FOOTER_VZIP) {
    throw new Error("VZip: bad footer");
  }

  const compressed = data.slice(12, footerStart);
  const sizeBuf = Buffer.alloc(8);
  sizeBuf.writeUInt32LE(decompressedSize, 0);
  sizeBuf.writeUInt32LE(0, 4);
  const lzmaAlone = Buffer.concat([properties, sizeBuf, compressed]);
  const result = Buffer.from(pureLzma.decompress(lzmaAlone));
  if (result.length !== decompressedSize) {
    throw new Error(
      `VZip: size mismatch (expected ${decompressedSize}, got ${result.length})`
    );
  }
  return result;
}

parentPort.on("message", (msg: { id: number; data: ArrayBuffer }) => {
  try {
    const buf = Buffer.from(msg.data);
    const out = decompressVzip(buf);
    const u8 = new Uint8Array(out);
    parentPort!.postMessage({ id: msg.id, ok: true, result: u8 }, [u8.buffer]);
  } catch (err: any) {
    parentPort!.postMessage({
      id: msg.id,
      ok: false,
      error: err?.message ?? String(err),
    });
  }
});
