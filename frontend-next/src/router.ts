// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import { ref } from "vue";

export type ViewID =
  | "dashboard"
  | "events"
  | "tasks"
  | "setup"
  | "llm"
  | "bot"
  | "groups"
  | "plugins"
  | "logs"
  | "settings";

export interface NavItem {
  id: ViewID;
  label: string;
  hint: string;
}

export const navItems: NavItem[] = [
  { id: "dashboard", label: "总览", hint: "运行状态与实时事件" },
  { id: "events", label: "事件", hint: "消息处理与回复决策" },
  { id: "tasks", label: "任务", hint: "提醒、周期查询与仓库订阅" },
  { id: "llm", label: "LLM 配置", hint: "Provider 与模型管理" },
  { id: "bot", label: "机器人", hint: "OneBot v11 接入与行为" },
  { id: "plugins", label: "插件", hint: "插件安装与设置" },
  { id: "groups", label: "群管理", hint: "群管理员自助配置" },
  { id: "logs", label: "日志", hint: "操作与错误日志" },
  { id: "settings", label: "设置", hint: "主题与系统更新" }
];

const validViews = new Set<ViewID>(["dashboard", "events", "tasks", "setup", "llm", "bot", "groups", "plugins", "logs", "settings"]);

function parseHash(): ViewID {
  const raw = window.location.hash.replace(/^#\/?/, "").split("?")[0] ?? "";
  if (validViews.has(raw as ViewID)) {
    return raw as ViewID;
  }
  return "dashboard";
}

export const currentView = ref<ViewID>(parseHash());

export function navigate(view: ViewID, query?: Record<string, string>): void {
  const params = new URLSearchParams(query);
  const nextHash = `#/${view}${params.size > 0 ? `?${params.toString()}` : ""}`;
  if (window.location.hash !== nextHash) {
    window.location.hash = nextHash;
  } else {
    currentView.value = view;
  }
}

export function viewQuery(): URLSearchParams {
  const query = window.location.hash.split("?", 2)[1] ?? "";
  return new URLSearchParams(query);
}

export function setupRouter(): void {
  window.addEventListener("hashchange", () => {
    currentView.value = parseHash();
  });
}
