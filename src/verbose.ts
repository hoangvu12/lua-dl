export let VERBOSE = false;
export function setVerbose(v: boolean) {
  VERBOSE = v;
}
export function vlog(...args: unknown[]) {
  if (VERBOSE) console.error(...args);
}

export function statusLine(msg: string) {
  if (VERBOSE) {
    console.error(msg);
    return;
  }
  if (process.stderr.isTTY) {
    process.stderr.write(`\r\x1b[2K${msg}`);
  } else {
    process.stderr.write(msg + "\n");
  }
}

export function statusDone() {
  if (!VERBOSE && process.stderr.isTTY) process.stderr.write("\n");
}
