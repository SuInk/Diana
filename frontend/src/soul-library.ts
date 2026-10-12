import type { Persona } from "./api";

// 人设库在界面上的几件小事：认名字、认「当前正文是库里哪一份」、起不撞车的名字。
// 单独放一个文件是为了能在 node 里直接测，不用起 Vue。

/** SOUL.md 的上限和提醒线，和后端 personaPromptMaxRunes 对齐。 */
export const SOUL_MAX_CHARS = 4000;
/** 超过这条线模型越来越抓不住重点：Anthropic 那份写了两万多字，但它是训练用的，不是每轮都塞进上下文。 */
export const SOUL_WARN_CHARS = 2500;

/** 按码点数字数：中文和 emoji 都算一个，和后端 []rune 的数法一致。 */
export function soulLength(text: string): number {
  return Array.from(text ?? "").length;
}

/** 取第一行一级标题当名字；第一行不是 `# ` 标题就返回空串。 */
export function soulTitle(text: string): string {
  for (const raw of (text ?? "").split("\n")) {
    const line = raw.trim();
    if (!line) continue;
    return line.startsWith("# ") ? line.slice(2).trim() : "";
  }
  return "";
}

/** 当前正文和库里哪一份一字不差。首尾空白不算差别。 */
export function matchingPersona(text: string, personas: Persona[]): Persona | undefined {
  const current = (text ?? "").trim();
  if (!current) return undefined;
  return personas.find((persona) => (persona.system_prompt ?? "").trim() === current);
}

/** 同名时加「（副本）」「（副本 2）」，名字上限 40 字。 */
export function unusedPersonaName(name: string, personas: Persona[]): string {
  const names = new Set(personas.map((p) => p.name));
  const base = Array.from(name.trim()).slice(0, 40).join("");
  let result = base;
  for (let n = 1; names.has(result); n++) {
    const suffix = n === 1 ? "（副本）" : `（副本 ${n}）`;
    result = Array.from(base).slice(0, 40 - Array.from(suffix).length).join("") + suffix;
  }
  return result;
}

/** 导出文件名。人设名可能带 / \ : 这类非法字符，只挑掉这几个，中文保留。 */
export function soulFileName(name: string): string {
  const slug = (name ?? "").replace(/[\\/:*?"<>|\u0000-\u001f]/g, "").trim();
  return `${slug || "SOUL"}.md`;
}
