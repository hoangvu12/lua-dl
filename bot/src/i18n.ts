/**
 * Tiny locale helper. Two languages. Auto-pick from the Discord interaction
 * locale (`i.locale`) with fallback to the bot's BOT_LANG env var, then "en".
 */

export type Lang = "en" | "vi";

export function pickLang(interactionLocale: string | undefined): Lang {
  if (interactionLocale?.toLowerCase().startsWith("vi")) return "vi";
  const envLang = process.env.BOT_LANG?.toLowerCase();
  if (envLang === "vi") return "vi";
  return "en";
}

export function reply(lang: Lang, appid: number): string {
  if (lang === "vi") {
    return `File .bat cho app ${appid}. Bỏ vào 1 folder trống rồi double-click.
Lần đầu sẽ tải ~24MB. Nếu Windows báo "protected your PC" thì bấm more info rồi run anyway.`;
  }
  return `Your .bat for app ${appid}. Drop it in an empty folder and double-click.
First run downloads ~24MB. If Windows warns "protected your PC", click more info then run anyway.`;
}
