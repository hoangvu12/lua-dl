/**
 * Thin wrapper around steam-user for anonymous Steam network access.
 * Goal for Phase 1: fetch latest manifest IDs for a given appid's depots.
 */

import SteamUser from "steam-user";
import { vlog } from "./verbose";

export interface DepotInfo {
  depotId: number;
  name?: string;
  manifestId?: string;   // latest on "public" branch
  maxSize?: number;
}

export interface AppInfo {
  name: string;
  depots: DepotInfo[];
}

export async function anonymousLogin(): Promise<SteamUser> {
  const client = new SteamUser({
    dataDirectory: null,
    protocol: SteamUser.EConnectionProtocol.TCP,
  });
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      reject(new Error("login timed out after 20s"));
    }, 20000);

    client.on("debug", (msg: string) => vlog("[steam:debug]", msg));
    client.on("disconnected", (eresult: number, msg: string) =>
      vlog("[steam] disconnected", eresult, msg)
    );
    client.on("error", (err: Error) => {
      console.error("[steam] error:", err.message);
      clearTimeout(timer);
      reject(err);
    });
    client.once("loggedOn", () => {
      vlog("[steam] logged on anonymously");
      clearTimeout(timer);
      resolve(client);
    });

    vlog("[steam] calling logOn({anonymous:true})...");
    client.logOn({ anonymous: true });
  });
}

export async function getAppInfo(
  client: SteamUser,
  appId: number
): Promise<AppInfo> {
  const info: any = await client.getProductInfo([appId], [], true);
  const app = info.apps[appId];
  if (!app) throw new Error(`No product info returned for app ${appId}`);

  const name: string = app.appinfo?.common?.name ?? `app-${appId}`;
  const rawDepots = app.appinfo?.depots ?? {};
  const depots: DepotInfo[] = [];

  for (const [key, raw] of Object.entries<any>(rawDepots)) {
    if (!/^\d+$/.test(key)) continue;
    depots.push({
      depotId: Number(key),
      name: raw?.name,
      manifestId: raw?.manifests?.public?.gid,
      maxSize: raw?.maxsize ? Number(raw.maxsize) : undefined,
    });
  }

  return { name, depots };
}

export async function getAppDepots(
  client: SteamUser,
  appId: number
): Promise<DepotInfo[]> {
  return (await getAppInfo(client, appId)).depots;
}
