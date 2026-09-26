// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import type { PromptSpec } from "./api";

type Overrides = Record<string, string>;

/** 输入框里显示的正文：改过就是覆盖值，没改过就是内置默认值。 */
export function promptOverrideValue(spec: PromptSpec, overrides: Overrides | undefined): string {
  const custom = overrides?.[spec.key] ?? "";
  return custom.trim() !== "" ? custom : spec.default;
}

/**
 * 写回一段正文。和默认值相同（或清空）就把这个键删掉，不在配置里留一份默认值的副本；
 * 表空了返回 undefined，保存时整个字段不带。后端 normalizePromptOverrides 也会这样做，
 * 这里先做一遍是为了「已修改」标记当场就对。
 */
export function withPromptOverride(overrides: Overrides | undefined, spec: PromptSpec, value: string): Overrides | undefined {
  const next: Overrides = { ...(overrides ?? {}) };
  const normalized = value.replace(/\r\n/g, "\n");
  if (normalized.trim() === "" || normalized.trim() === spec.default.trim()) {
    delete next[spec.key];
  } else {
    next[spec.key] = normalized;
  }
  return Object.keys(next).length > 0 ? next : undefined;
}

/** 输出格式框里显示的内容：改过就是覆盖值，没改过就是默认格式（去掉前面的分隔符）。 */
export function promptFormatValue(spec: PromptSpec, overrides: Overrides | undefined): string {
  if (!spec.format_key) return "";
  const custom = overrides?.[spec.format_key] ?? "";
  return custom.trim() !== "" ? custom : (spec.contract ?? "").trim();
}

/** 写回输出格式。规则和正文一样：改回默认或清空就删掉这个键。 */
export function withPromptFormat(overrides: Overrides | undefined, spec: PromptSpec, value: string): Overrides | undefined {
  if (!spec.format_key) return overrides;
  const formatSpec: PromptSpec = { ...spec, key: spec.format_key, default: (spec.contract ?? "").trim() };
  return withPromptOverride(overrides, formatSpec, value);
}

/** 正文或输出格式改过任意一样，这段就算改过。 */
export function isPromptCustomized(spec: PromptSpec, overrides: Overrides | undefined): boolean {
  const changed = (key: string | undefined) => Boolean(key && (overrides?.[key] ?? "").trim() !== "");
  return changed(spec.key) || changed(spec.format_key);
}

/** 输出格式是否改过：界面上单独提醒，这是最容易改坏的地方。 */
export function isPromptFormatCustomized(spec: PromptSpec, overrides: Overrides | undefined): boolean {
  return Boolean(spec.format_key && (overrides?.[spec.format_key] ?? "").trim() !== "");
}

/** 把一段恢复成默认：正文和输出格式一起。 */
export function withoutPromptCustomization(overrides: Overrides | undefined, spec: PromptSpec): Overrides | undefined {
  return withoutPromptOverrides(overrides, spec.format_key ? [spec.key, spec.format_key] : [spec.key]);
}

/** 默认正文里有、当前正文里没有的占位符：运行时那项信息就不再进提示词了。 */
export function missingPromptVars(spec: PromptSpec, overrides: Overrides | undefined): string[] {
  const text = promptOverrideValue(spec, overrides) + promptFormatValue(spec, overrides);
  return (spec.vars ?? []).map((variable) => variable.name).filter((name) => !text.includes(`{${name}}`));
}

/** 只数登记表里认识的键：配置里可能还躺着旧版本删掉的提示词。 */
export function customizedPromptCount(specs: PromptSpec[], overrides: Overrides | undefined): number {
  return specs.filter((spec) => isPromptCustomized(spec, overrides)).length;
}

/** 从覆盖表里去掉一批键，用于「恢复内置默认」这种只管一部分提示词的按钮。 */
export function withoutPromptOverrides(overrides: Overrides | undefined, keys: readonly string[]): Overrides | undefined {
  const next: Overrides = { ...(overrides ?? {}) };
  for (const key of keys) delete next[key];
  return Object.keys(next).length > 0 ? next : undefined;
}

/** 合在一个框里编辑的一段：小标题加它对应的提示词。 */
export interface PromptSection {
  spec: PromptSpec;
  title: string;
}

export type ParsedPromptSections = { ok: true; values: Record<string, string> } | { ok: false; error: string };

function sectionHeading(title: string): string {
  return `【${title}】`;
}

/**
 * 几段提示词拼成一个框：每段前面一行【小标题】。存的时候仍按段拆开——后端会把它们
 * 塞进评分提示词的不同位置、判断模型的不同字段，合成一段存就分不回去了。
 */
export function composePromptSections(sections: readonly PromptSection[], overrides: Overrides | undefined): string {
  return sections.map((section) => `${sectionHeading(section.title)}\n${promptOverrideValue(section.spec, overrides).trim()}`).join("\n\n");
}

/**
 * 按【小标题】把框里的文字拆回各段。小标题必须一行一个、每段恰好一次，顺序不限；
 * 缺了、重了、开头多出正文或某段空着都不拆，免得把一段内容写进别的键里。
 */
export function parsePromptSections(text: string, sections: readonly PromptSection[], maxRunes = 0): ParsedPromptSections {
  const byHeading = new Map(sections.map((section) => [sectionHeading(section.title), section]));
  const bodies = new Map<PromptSection, string[]>();
  let current: PromptSection | null = null;
  for (const line of text.replace(/\r\n/g, "\n").split("\n")) {
    const section = byHeading.get(line.trim());
    if (section) {
      if (bodies.has(section)) return { ok: false, error: `小标题${sectionHeading(section.title)}出现了两次` };
      current = section;
      bodies.set(section, []);
      continue;
    }
    if (current) bodies.get(current)!.push(line);
    else if (line.trim()) return { ok: false, error: `第一个小标题前面不能有正文，每段要从${sectionHeading(sections[0]?.title ?? "")}这样的小标题开始` };
  }
  const values: Record<string, string> = {};
  for (const section of sections) {
    const lines = bodies.get(section);
    if (!lines) return { ok: false, error: `少了小标题${sectionHeading(section.title)}，小标题要单独占一行、原样保留` };
    const body = lines.join("\n").trim();
    if (!body) return { ok: false, error: `${sectionHeading(section.title)}是空的；想用默认内容请点「恢复默认」` };
    if (maxRunes && Array.from(body).length > maxRunes) return { ok: false, error: `${sectionHeading(section.title)}有 ${Array.from(body).length} 字，超过单段上限 ${maxRunes} 字` };
    values[section.spec.key] = body;
  }
  return { ok: true, values };
}

/** 把拆好的各段写回覆盖表，和默认一样的段照旧不存。 */
export function withPromptSections(overrides: Overrides | undefined, sections: readonly PromptSection[], values: Record<string, string>): Overrides | undefined {
  let next = overrides;
  for (const section of sections) {
    const value = values[section.spec.key];
    if (value !== undefined) next = withPromptOverride(next, section.spec, value);
  }
  return next;
}

/** 框里的文字拆出来和当前覆盖表一致：用来区分「自己刚输入的」和「别处改了覆盖表」。 */
export function promptSectionsMatch(values: Record<string, string>, sections: readonly PromptSection[], overrides: Overrides | undefined): boolean {
  return sections.every((section) => (values[section.spec.key] ?? "").trim() === promptOverrideValue(section.spec, overrides).trim());
}
