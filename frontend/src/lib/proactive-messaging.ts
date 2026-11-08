// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import type { ProactiveMessaging } from "../api";

// 主动开口（试验）的表单默认值，和后端 proactive_messaging.go 的默认值一致。
export const PROACTIVE_DEFAULTS = {
  group_idle_minutes: 120,
  owner_idle_hours: 8,
  daily_limit: 2,
  quiet_start: "23:00",
  quiet_end: "08:00"
} as const;

// proactiveFormValue 把接口里可能为空的设置补成表单能直接绑定的完整对象。
export function proactiveFormValue(value: ProactiveMessaging | null | undefined): Required<ProactiveMessaging> {
  return {
    enabled: value?.enabled ?? false,
    group_ids: [...(value?.group_ids ?? [])],
    group_idle_minutes: value?.group_idle_minutes || PROACTIVE_DEFAULTS.group_idle_minutes,
    owner_enabled: value?.owner_enabled ?? false,
    owner_idle_hours: value?.owner_idle_hours || PROACTIVE_DEFAULTS.owner_idle_hours,
    daily_limit: value?.daily_limit || PROACTIVE_DEFAULTS.daily_limit,
    quiet_start: value?.quiet_start || PROACTIVE_DEFAULTS.quiet_start,
    quiet_end: value?.quiet_end || PROACTIVE_DEFAULTS.quiet_end
  };
}

// parseProactiveGroupIDs 把「每行一个，也可用逗号、空格分隔」的输入拆成去重的群号。
export function parseProactiveGroupIDs(draft: string): string[] {
  const seen = new Set<string>();
  for (const part of draft.split(/[\s,，、]+/)) {
    const id = part.trim();
    if (id) seen.add(id);
  }
  return [...seen];
}

// proactivePayload 生成保存时提交的设置。清空的数字框是空串，按 0 交给后端补默认值。
export function proactivePayload(value: ProactiveMessaging | null | undefined, groupDraft: string): ProactiveMessaging {
  const current = proactiveFormValue(value);
  const int = (input: unknown) => Math.max(0, Math.trunc(Number(input) || 0));
  return {
    enabled: current.enabled,
    group_ids: parseProactiveGroupIDs(groupDraft),
    group_idle_minutes: int(current.group_idle_minutes),
    owner_enabled: current.owner_enabled,
    owner_idle_hours: int(current.owner_idle_hours),
    daily_limit: int(current.daily_limit),
    quiet_start: (current.quiet_start ?? "").trim(),
    quiet_end: (current.quiet_end ?? "").trim()
  };
}
