import type { BotProfileConfig, Persona } from "./api";

export function personaFromSettings(current: BotProfileConfig, name: string) {
  return {
    name,
    system_prompt: current.system_prompt ?? "",
    reply_style: current.reply_style || "assistant",
    action_description_enabled: current.action_description_enabled ?? false,
    daypart_tone_enabled: current.daypart_tone_enabled ?? false,
    self_reference: current.self_reference ?? "",
    sentence_enders: current.sentence_enders ?? "",
  };
}

export function applyPersonaSettings(current: BotProfileConfig, persona: Persona, replacePrompt = false): BotProfileConfig {
  const prompt = persona.system_prompt ?? "";
  return {
    ...current,
    system_prompt: prompt.trim() && (replacePrompt || !current.system_prompt?.trim()) ? prompt : current.system_prompt,
    reply_style: persona.reply_style || "assistant",
    action_description_enabled: persona.action_description_enabled ?? false,
    daypart_tone_enabled: persona.daypart_tone_enabled ?? current.daypart_tone_enabled,
    self_reference: persona.self_reference ?? "",
    sentence_enders: persona.sentence_enders ?? "",
  };
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
