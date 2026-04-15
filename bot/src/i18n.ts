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

export function reply(lang: Lang, appids: number[]): string {
  const single = appids.length === 1;
  if (lang === "vi") {
    const target = single ? `app ${appids[0]}` : `${appids.length} app`;
    return `File .bat cho ${target}. Bỏ vào 1 folder trống rồi double-click.
Lần đầu sẽ tải ~24MB. Nếu Windows báo "protected your PC" thì bấm more info rồi run anyway.`;
  }
  const target = single ? `app ${appids[0]}` : `${appids.length} apps`;
  return `Your .bat for ${target}. Drop it in an empty folder and double-click.
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

export function childPickPrompt(lang: Lang): string {
  return lang === "vi"
    ? "Chọn thứ muốn tải..."
    : "Pick what to download...";
}

export function childHeader(lang: Lang, gameName: string): string {
  if (lang === "vi") {
    return `**${gameName}** có thêm nội dung đi kèm. Chọn những thứ bạn muốn tải (có thể chọn nhiều):`;
  }
  return `**${gameName}** has extra content. Pick everything you want to download (multi-select):`;
}

export function labelBaseGame(lang: Lang): string {
  return lang === "vi" ? "Game gốc" : "Base game";
}

export function labelType(lang: Lang, type: string): string {
  const t = type.toLowerCase();
  if (lang === "vi") {
    if (t === "music") return "Soundtrack";
    if (t === "dlc") return "DLC";
    if (t === "demo") return "Demo";
    return type.toUpperCase();
  }
  if (t === "music") return "Soundtrack";
  if (t === "dlc") return "DLC";
  if (t === "demo") return "Demo";
  return type.toUpperCase();
}

export function missingInputError(lang: Lang): string {
  if (lang === "vi") {
    return "Cần truyền 1 trong 2: `appid` (số) hoặc `query` (tên game).";
  }
  return "Provide either `appid` (number) or `query` (game name).";
}
