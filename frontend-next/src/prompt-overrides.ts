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

/** 默认正文里有、当前正文里没有的占位符：运行时那项信息就不再进提示词了。 */
export function missingPromptVars(spec: PromptSpec, overrides: Overrides | undefined): string[] {
  const text = promptOverrideValue(spec, overrides) + (spec.contract ?? "");
  return (spec.vars ?? []).map((variable) => variable.name).filter((name) => !text.includes(`{${name}}`));
}

/** 只数登记表里认识的键：配置里可能还躺着旧版本删掉的提示词。 */
export function customizedPromptCount(specs: PromptSpec[], overrides: Overrides | undefined): number {
  return specs.filter((spec) => (overrides?.[spec.key] ?? "").trim() !== "").length;
}

/** 从覆盖表里去掉一批键，用于「恢复内置默认」这种只管一部分提示词的按钮。 */
export function withoutPromptOverrides(overrides: Overrides | undefined, keys: readonly string[]): Overrides | undefined {
  const next: Overrides = { ...(overrides ?? {}) };
  for (const key of keys) delete next[key];
  return Object.keys(next).length > 0 ? next : undefined;
}
