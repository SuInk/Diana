// 人设正文可以用段头声明「这一类规则我自己写」，运行时看到段头就不再注入对应的
// 那一段（后端 model/assistant/behavior_preset.go 里的 personaOwnedSections）。
//
// 其中三项背后有界面控件：自称、句尾语气词、动作描写。正文一旦接管，这几个控件
// 填什么都不再进提示词——如果界面不说，用户就会对着一个填了值却毫无反应的输入框
// 反复试，而且没有任何线索指向原因。所以这份表存在的唯一目的是让界面说出来。
//
// 段头必须和后端那张表逐字一致：对不上的话界面说「已接管」而运行时照旧注入，
// 或者反过来，两种都比没有提示更糟。persona-owned.test.mjs 钉住这份清单。
export const personaOwnedHeaders = {
  voice: "自称与语气词：",
  action: "动作描写：",
} as const;

export type PersonaOwnedField = keyof typeof personaOwnedHeaders;

/** 和后端 PersonaMode 同一套取值；空值按 fill 处理。 */
export type PersonaMode = "fill" | "own";

/**
 * personaOwnsField 判断这段人设正文是不是自己接管了某个带控件的项。
 *
 * 档位在前，段头在后，和后端 personaOwnsSection 同一个顺序：填空题档一律不认段头，
 * 哪怕正文里真写了。顺序反过来的话，界面会对着一个其实仍然生效的控件说「已接管」。
 *
 * 纯函数。判据是段头出现与否——和后端同一个判据（strings.Contains），不做归一化：
 * 后端认的是原样子串，这里多做一步就会两边给出不同答案。
 */
export function personaOwnsField(mode: PersonaMode | undefined, systemPrompt: string, field: PersonaOwnedField): boolean {
  if (mode !== "own") return false;
  return (systemPrompt ?? "").includes(personaOwnedHeaders[field]);
}
