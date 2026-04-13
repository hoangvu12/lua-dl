const RESERVED = /^(con|prn|aux|nul|com[1-9]|lpt[1-9])(\..*)?$/i;

export function sanitizeFolderName(name: string): string {
  let s = name
    .replace(/[<>:"|?*\\/\x00-\x1f]/g, "-")
    .replace(/\s+/g, " ")
    .replace(/[. ]+$/g, "")
    .trim();
  if (!s) s = "game";
  if (RESERVED.test(s)) s = `_${s}`;
  if (s.length > 120) s = s.slice(0, 120).trimEnd();
  return s;
}
