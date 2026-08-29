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
  | "users"
  | "notebook"
  | "plugins"
  | "logs"
  | "settings";

// NavGroupID 是侧边栏的分组。分组本来就存在，只是以前只写在下面这段注释里：
// 「装机时先配什么、平时看什么、出问题翻什么」。11 个条目平铺时全都一样重，
// 读的人得自己把它们分回三堆，所以现在把这条信息画出来。
export type NavGroupID = "setup" | "operate";

export interface NavGroup {
  id: NavGroupID;
  label: string;
}

// 总览和设置不属于任何一组：一个是入口，一个是兜底，给它们套个标题反而多余。
export const navGroups: NavGroup[] = [
  { id: "setup", label: "配置" },
  { id: "operate", label: "日常" }
];

export interface NavItem {
  id: ViewID;
  label: string;
  hint: string;
  group?: NavGroupID;
  // covers 列出这一项还代表哪些视图。合并成一页多档之后，档位仍是各自的路由
  // 地址，侧边栏得知道停在哪个地址时该亮哪一项，否则用户会以为自己掉出了导航。
  covers?: ViewID[];
}

// 顺序按「装机器人时的实际操作顺序」排：先看总览，再配模型和机器人，然后才是
// 插件、群、人这些日常项，事件（含日志）属于出问题时才翻的记录页，放后面。
export const navItems: NavItem[] = [
  { id: "dashboard", label: "总览", hint: "运行状态与实时事件" },
  { id: "llm", label: "LLM 配置", hint: "Provider 与模型管理", group: "setup" },
  { id: "bot", label: "机器人", hint: "OneBot v11 接入与行为", group: "setup" },
  { id: "plugins", label: "插件", hint: "插件安装与设置", group: "setup" },
  { id: "groups", label: "群管理", hint: "群管理员自助配置", group: "operate" },
  { id: "users", label: "记忆", hint: "机器人记住的人和事", group: "operate", covers: ["notebook"] },
  { id: "tasks", label: "任务", hint: "提醒、周期查询与仓库订阅", group: "operate" },
  { id: "events", label: "记录", hint: "消息处理、回复决策与运行日志", group: "operate", covers: ["logs"] },
  { id: "settings", label: "设置", hint: "主题与系统更新" }
];

// navItemCovers 判断某个视图该由哪一个侧边栏条目代表。
export function navItemForView(view: ViewID): ViewID | undefined {
  for (const item of navItems) {
    if (item.id === view || item.covers?.includes(view)) {
      return item.id;
    }
  }
  return undefined;
}

// navSections 把条目按组切成连续的段，模板据此插入组标题。条目顺序仍以
// navItems 为准，这里不重排，只是把相邻的同组条目收在一起。
export function navSections(): { group?: NavGroup; items: NavItem[] }[] {
  const sections: { group?: NavGroup; items: NavItem[] }[] = [];
  for (const item of navItems) {
    const last = sections[sections.length - 1];
    if (last && last.group?.id === item.group) {
      last.items.push(item);
      continue;
    }
    sections.push({ group: navGroups.find((group) => group.id === item.group), items: [item] });
  }
  return sections;
}

const validViews = new Set<ViewID>(["dashboard", "events", "tasks", "setup", "llm", "bot", "groups", "users", "notebook", "plugins", "logs", "settings"]);

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
