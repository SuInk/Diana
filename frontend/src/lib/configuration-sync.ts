// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import { onScopeDispose, reactive, watch } from "vue";

// Go time.Duration stores nanoseconds in an int64; the API uses whole milliseconds.
export const maximumModelRequestTimeoutMS = 9_223_372_036_854;
export const maximumModelRequestTimeoutSeconds = maximumModelRequestTimeoutMS / 1000;

export function modelRequestTimeoutSeconds(milliseconds?: number): number {
  return (milliseconds ?? 0) / 1000;
}

export function modelRequestTimeoutMilliseconds(seconds: number | string): number {
  const value = typeof seconds === "string" && seconds.trim() === "" ? 0 : Number(seconds);
  const milliseconds = Math.round(value * 1000);
  if (!Number.isFinite(value) || value < 0 || value > maximumModelRequestTimeoutSeconds ||
      !Number.isSafeInteger(milliseconds) || Math.abs(value * 1000 - milliseconds) > Number.EPSILON * Math.max(1, Math.abs(value * 1000)) * 2 ||
      milliseconds > maximumModelRequestTimeoutMS) {
    throw new Error(`单次模型请求超时请输入 0 到 ${maximumModelRequestTimeoutSeconds} 秒，最多三位小数`);
  }
  return milliseconds;
}

export type ConfigurationKind = "bot" | "llm" | "search";
const revisions = reactive<Record<ConfigurationKind, number>>({ bot: 0, llm: 0, search: 0 });

export function configurationKindForMutation(path: string): ConfigurationKind | undefined {
  if (path === "/api/assistant/search-providers/test") return undefined;
  if (path === "/api/assistant/search-assignment" || path === "/api/assistant/search-providers" || path.startsWith("/api/assistant/search-providers/")) return "search";
  if (path === "/api/assistant/agent-browser" || path === "/api/assistant/config" || path.startsWith("/api/assistant/config/")) return "bot";
  if (path === "/api/llm/config" || path.startsWith("/api/llm/config/")) return "llm";
  return undefined;
}

export function notifyConfigurationChanged(kind: ConfigurationKind): void {
  revisions[kind]++;
}

// Kept-alive views retain this subscription, so their reference lists are fresh
// when revisited. Callers refresh server data, not an open editor's draft.
// Serializing refreshes avoids overlapping responses overwriting newer state.
export function useConfigurationRefresh(kinds: ConfigurationKind[], refresh: () => Promise<unknown>): void {
  let running = false;
  let queued = false;
  let disposed = false;
  onScopeDispose(() => { disposed = true; });
  watch(() => kinds.map(kind => revisions[kind]), async () => {
    queued = true;
    if (running) return;
    running = true;
    try {
      while (queued && !disposed) {
        queued = false;
        try {
          await refresh();
        } catch (error) {
          console.warn("Configuration refresh failed", error);
        }
      }
    } finally {
      running = false;
    }
  }, { flush: "post" });
}
