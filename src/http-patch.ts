/**
 * Enable HTTP keep-alive + connection pooling for steam-user's CDN fetches.
 *
 * steam-user/components/cdn.js calls `http.request(options)` / `https.request(options)`
 * without passing an `agent`, which falls back to the respective globalAgent.
 * By default Node's globalAgent has keepAlive=false — every chunk opens a new
 * TCP+TLS connection. On a ~60ms RTT link that's seconds of handshake tax per
 * batch and caps throughput hard.
 *
 * This module mutates the global agents before steam-user runs. Import it
 * first in the entry file.
 */

import http from "node:http";
import https from "node:https";
import type { Socket } from "node:net";

function tune(agent: http.Agent) {
  (agent as any).keepAlive = true;
  (agent as any).keepAliveMsecs = 30_000;
  // Plenty of headroom for 16–32 file workers × 4 chunk workers each
  agent.maxSockets = 256;
  agent.maxFreeSockets = 64;

  // Disable Nagle's algorithm on every new connection this agent creates.
  // Without this the kernel batches small writes for ~40ms hoping to coalesce
  // — which adds latency on chunked HTTP traffic. DepotDownloader sets the
  // same flag on its sockets.
  const origCreate = (agent as any).createConnection?.bind(agent);
  if (origCreate) {
    (agent as any).createConnection = (opts: any, cb: any) => {
      const sock = origCreate(opts, cb) as Socket;
      if (sock && typeof (sock as any).setNoDelay === "function") {
        (sock as any).setNoDelay(true);
      }
      return sock;
    };
  }
}

tune(http.globalAgent);
tune(https.globalAgent);
