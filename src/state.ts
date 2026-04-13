import { existsSync, readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { dirname } from "node:path";

interface Entry {
  size: number;
  sha1: string;
  mtime: number;
}

type StateShape = Record<string, Entry>;

export class StateCache {
  private path: string;
  private data: StateShape;
  private dirty = false;

  constructor(path: string) {
    this.path = path;
    if (existsSync(path)) {
      try {
        this.data = JSON.parse(readFileSync(path, "utf8"));
      } catch {
        this.data = {};
      }
    } else {
      this.data = {};
    }
  }

  private key(depotId: number, manifestId: string, filepath: string): string {
    return `${depotId}_${manifestId}/${filepath}`;
  }

  get(depotId: number, manifestId: string, filepath: string): Entry | undefined {
    return this.data[this.key(depotId, manifestId, filepath)];
  }

  set(
    depotId: number,
    manifestId: string,
    filepath: string,
    entry: Entry
  ): void {
    this.data[this.key(depotId, manifestId, filepath)] = entry;
    this.dirty = true;
  }

  flush(): void {
    if (!this.dirty) return;
    mkdirSync(dirname(this.path), { recursive: true });
    writeFileSync(this.path, JSON.stringify(this.data));
    this.dirty = false;
  }
}

export function toHex(v: unknown): string {
  if (typeof v === "string") return v.toLowerCase();
  if (v && typeof v === "object" && "length" in (v as any)) {
    return Buffer.from(v as Uint8Array).toString("hex");
  }
  return "";
}
