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

export function searchHeader(lang: Lang, query: string, count: number): string {
  if (lang === "vi") {
    return `Tìm thấy ${count} kết quả cho **${query}** — chọn 1 game bên dưới:`;
  }
  return `Found ${count} result(s) for **${query}** — pick one below:`;
}

export function searchNoResults(lang: Lang, query: string): string {
  if (lang === "vi") return `Không tìm thấy game nào cho **${query}**.`;
  return `No games found for **${query}**.`;
}

export function searchPickPrompt(lang: Lang): string {
  return lang === "vi" ? "Chọn 1 game..." : "Pick a game...";
}

export function missingInputError(lang: Lang): string {
  if (lang === "vi") {
    return "Cần truyền 1 trong 2: `appid` (số) hoặc `query` (tên game).";
  }
  return "Provide either `appid` (number) or `query` (game name).";
}
