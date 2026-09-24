import type { BotProfileConfig, Persona } from "./api";

export function personaFromSettings(current: BotProfileConfig, name: string) {
  return {
    name,
    system_prompt: current.system_prompt ?? "",
    // 品格层跟着人设走，不在这里逐字段拆开：它只有人能改，前端只负责原样搬运。
    soul: current.soul,
    persona_mode: current.persona_mode ?? "fill",
    action_description_enabled: current.action_description_enabled ?? false,
    daypart_tone_enabled: current.daypart_tone_enabled ?? false,
    self_reference: current.self_reference ?? "",
    sentence_enders: current.sentence_enders ?? "",
    // 提示词跟着人设走：存进人设库、导出分享的都是这台机器人当前的整套覆盖。
    prompts: nonEmptyPrompts(current.prompt_overrides),
    // 判据也跟着人设走：一份人设文件就是全部提示词配置。
    extra_criteria: current.proactive_reply_extra_criteria ?? "",
    account_safety_rules: current.reply_account_safety_audit_prompt ?? "",
  };
}

function nonEmptyPrompts(prompts: Record<string, string> | undefined): Record<string, string> | undefined {
  if (!prompts) return undefined;
  const entries = Object.entries(prompts).filter(([, value]) => value.trim() !== "");
  return entries.length ? Object.fromEntries(entries) : undefined;
}

/** 两份覆盖表是否相同。顺序无关，空表和 undefined 算同一个。 */
export function samePrompts(a: Record<string, string> | undefined, b: Record<string, string> | undefined): boolean {
  const left = nonEmptyPrompts(a) ?? {};
  const right = nonEmptyPrompts(b) ?? {};
  const keys = Object.keys(left);
  return keys.length === Object.keys(right).length && keys.every((key) => left[key].trim() === (right[key] ?? "").trim());
}

export function applyPersonaSettings(current: BotProfileConfig, persona: Persona, replacePrompt = false): BotProfileConfig {
  const prompt = persona.system_prompt ?? "";
  return {
    ...current,
    system_prompt: prompt.trim() && (replacePrompt || !current.system_prompt?.trim()) ? prompt : current.system_prompt,
    soul: persona.soul ?? current.soul,
    // 档位跟着正文一起换：这份人设的正文带不带段头，只有它自己的档位说了算。
    persona_mode: persona.persona_mode ?? "fill",
    action_description_enabled: persona.action_description_enabled ?? false,
    daypart_tone_enabled: persona.daypart_tone_enabled ?? current.daypart_tone_enabled,
    self_reference: persona.self_reference ?? "",
    sentence_enders: persona.sentence_enders ?? "",
    // 整份替换：人设没带提示词就是全部用默认值，不和上一套人设的覆盖混在一起。
    prompt_overrides: nonEmptyPrompts(persona.prompts),
    // 老人设没有判据这两栏：没写就保留机器人现在的，写了（哪怕空串）才替换。
    proactive_reply_extra_criteria: persona.extra_criteria ?? current.proactive_reply_extra_criteria,
    reply_account_safety_audit_prompt: persona.account_safety_rules ?? current.reply_account_safety_audit_prompt,
  };
}

/**
 * 套用一份 YAML 里的人设。和从人设库套用不同，这是整份替换：YAML 就是全部配置，
 * 里面没写的品格、正文就是没有，不保留表单里原来的值。
 */
export function applyPersonaDocument(current: BotProfileConfig, persona: Persona): BotProfileConfig {
  return asCustomPersona({
    ...current,
    system_prompt: persona.system_prompt ?? "",
    soul: persona.soul,
    persona_mode: persona.persona_mode ?? "fill",
    action_description_enabled: persona.action_description_enabled ?? false,
    daypart_tone_enabled: persona.daypart_tone_enabled ?? false,
    self_reference: persona.self_reference ?? "",
    sentence_enders: persona.sentence_enders ?? "",
    prompt_overrides: nonEmptyPrompts(persona.prompts),
    proactive_reply_extra_criteria: persona.extra_criteria ?? "",
    reply_account_safety_audit_prompt: persona.account_safety_rules ?? "",
  });
}

export function unusedPersonaName(name: string, personas: Persona[]): string {
  const names = new Set(personas.map(p => p.name));
  const base = Array.from(name.trim()).slice(0, 40).join("");
  let result = base;
  for (let n = 1; names.has(result); n++) {
    const suffix = n === 1 ? "（副本）" : `（副本 ${n}）`;
    result = Array.from(base).slice(0, 40 - Array.from(suffix).length).join("") + suffix;
  }
  return result;
}

export function currentPersonaSelection(current: BotProfileConfig, personas: Persona[]): string {
  const preset = personas.find(p => p.id === current.persona_id);
  if (!preset) return "custom";
  const actual = personaFromSettings(current, "");
  const expected = personaFromSettings(preset as BotProfileConfig, "");
  for (const key of Object.keys(actual) as (keyof typeof actual)[]) {
    // soul 是对象，=== 比的是引用，逐字段比较会把每套预设都判成「自定义」。
    if (key === "soul") continue;
    if (key === "prompts") {
      if (!samePrompts(actual.prompts, preset.prompts)) return "custom";
      continue;
    }
    // 判据按人设里的字段比，personaFromSettings 读的是机器人配置的字段名。
    if (key === "extra_criteria" || key === "account_safety_rules") {
      const expectedCriteria = preset[key];
      if (expectedCriteria !== undefined && (actual[key] ?? "").trim() !== expectedCriteria.trim()) return "custom";
      continue;
    }
    if (key === "name" || (key === "system_prompt" && !preset.system_prompt?.trim()) || (key === "daypart_tone_enabled" && preset.daypart_tone_enabled === undefined)) continue;
    if (actual[key] !== expected[key]) return "custom";
  }
  return preset.id;
}

export function asCustomPersona(current: BotProfileConfig): BotProfileConfig {
  return { ...current, persona_id: "", custom_persona: { id: "custom", ...personaFromSettings(current, "自定义") } };
}

export function selectPersona(current: BotProfileConfig, preset?: Persona): BotProfileConfig {
  const custom = current.persona_id ? current.custom_persona : asCustomPersona(current).custom_persona;
  if (!preset) {
    if (!custom) return asCustomPersona(current);
    return asCustomPersona({ ...current, ...personaFromSettings(custom as BotProfileConfig, ""), name: current.name });
  }
  return { ...applyPersonaSettings(current, preset, true), persona_id: preset.id, custom_persona: custom };
}
