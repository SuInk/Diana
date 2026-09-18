// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import { onScopeDispose, reactive, watch } from "vue";

export type ConfigurationKind = "bot" | "llm";
const revisions = reactive<Record<ConfigurationKind, number>>({ bot: 0, llm: 0 });

export function configurationKindForMutation(path: string): ConfigurationKind | undefined {
  if (path === "/api/assistant/config" || path.startsWith("/api/assistant/config/")) return "bot";
  if (path === "/api/llm/config" || path.startsWith("/api/llm/config/")) return "llm";
  if (["/api/llm/oauth/providers", "/api/llm/oauth/providers/delete", "/api/llm/oauth/login/complete", "/api/llm/oauth/logout"].includes(path)) return "llm";
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
