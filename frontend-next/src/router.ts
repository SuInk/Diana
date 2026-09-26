// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import { ref } from "vue";

export type ViewID =
  | "dashboard"
  | "events"
  | "tasks"
  | "setup"
  | "provider"
  | "bot"
  | "groups"
  | "users"
  | "notebook"
  | "plugins"
  | "browser"
  | "workspace"
  | "logs"
  | "favorability"
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

// 顺序按「装机器人时的实际操作顺序」排：先看总览，再配模型、机器人、扩展和各个群，
// 然后才是记忆、任务这些日常项，事件（含日志）属于出问题时才翻的记录页，放后面。
export const navItems: NavItem[] = [
  { id: "dashboard", label: "总览", hint: "运行状态与实时事件" },
  { id: "provider", label: "提供商", hint: "提供商接入、凭据与模型分组", group: "setup" },
  { id: "bot", label: "机器人", hint: "OneBot v11 接入与行为", group: "setup" },
  { id: "plugins", label: "扩展", hint: "插件、Skills 与 MCP", group: "setup" },
  { id: "groups", label: "群管理", hint: "群管理员自助配置", group: "setup" },
  { id: "users", label: "记忆", hint: "机器人记住的人和事", group: "operate", covers: ["notebook"] },
  { id: "tasks", label: "任务", hint: "提醒、周期查询与仓库订阅", group: "operate" },
  { id: "browser", label: "浏览器", hint: "Diana 内置浏览器：看画面、自己上手", group: "operate" },
  { id: "workspace", label: "文件", hint: "Agent 工作区里的文件：按分区浏览、预览、下载与删除", group: "operate" },
  { id: "events", label: "记录", hint: "消息处理、回复决策、运行日志与好感与画像", group: "operate", covers: ["logs", "favorability"] },
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

const validViews = new Set<ViewID>(["dashboard", "events", "tasks", "setup", "provider", "bot", "groups", "users", "notebook", "plugins", "browser", "workspace", "logs", "favorability", "settings"]);

// 首页：地址栏里是根路径，也是所有认不出来的地址的落点。
const homeView: ViewID = "dashboard";

// 旧地址仍要认：这一页的路由标识从 llm 改成 provider，而书签、聊天里贴过的链接
// 和别人截过的图不会跟着改。认不出就掉回总览，用户会以为页面被删了。
const renamedViews: Record<string, ViewID> = { llm: "provider" };

// 控制台可以挂在子路径下（VITE_BASE_PATH），路由要在这个前缀之内工作。
// 单测直接 import 这个模块时没有 Vite 注入的 env，退回根路径。
const basePath = normalizeBase(import.meta.env?.BASE_URL);

/** 统一成 "/" 或 "/prefix/"：两头都带斜杠，拼地址和剥前缀都不用再判断。 */
function normalizeBase(raw: string | undefined): string {
  const trimmed = (raw ?? "/").trim();
  if (trimmed === "" || trimmed === "." || trimmed === "./") return "/";
  return `/${trimmed.replace(/^\/+|\/+$/g, "")}/`.replace(/^\/\/+/, "/");
}

/** 视图对应的浏览器地址。首页是根路径，其余是 /<view>。 */
export function viewPath(view: ViewID, query?: URLSearchParams): string {
  const search = query && query.size > 0 ? `?${query.toString()}` : "";
  return `${basePath}${view === homeView ? "" : view}${search}`;
}

interface Resolved {
  view: ViewID;
  /** 规范地址。与当前地址不同就用 replaceState 收敛过去。 */
  path: string;
}

/**
 * 解析一个地址，给出该显示哪个视图、以及它的规范写法。
 *
 * 三种要改写的情况：hash 路由时代的 #/xxx 老链接、改过名的视图、以及认不出来
 * 的地址。前两种保留 query（老链接的参数挂在 hash 里），最后一种连参数一起丢
 * 掉——既然路径本身就不认识，带过去的参数只会更让人困惑。
 */
function resolveLocation(pathname: string, search: string, hash: string): Resolved {
  let raw = pathname.startsWith(basePath) ? pathname.slice(basePath.length) : pathname.replace(/^\/+/, "");
  let query = new URLSearchParams(search);
  // #/groups?group=123 这类老链接：路径和参数都在 hash 里。
  const legacy = /^#\/?([^?]*)\??(.*)$/.exec(hash);
  if (raw === "" && legacy) {
    raw = legacy[1] ?? "";
    query = new URLSearchParams(legacy[2] ?? "");
  }
  raw = decodeURIComponent(raw.split("/")[0] ?? "").trim().toLowerCase();
  if (raw === "") {
    return { view: homeView, path: viewPath(homeView, query) };
  }
  const view = validViews.has(raw as ViewID) ? (raw as ViewID) : renamedViews[raw];
  if (!view) {
    return { view: homeView, path: viewPath(homeView) };
  }
  return { view, path: viewPath(view, query) };
}

/** 当前地址（含查询串），用于和规范地址比较，避免多推一条历史记录。 */
function currentLocation(): string {
  return `${window.location.pathname}${window.location.search}${window.location.hash}`;
}

function applyLocation(): ViewID {
  const resolved = resolveLocation(window.location.pathname, window.location.search, window.location.hash);
  if (currentLocation() !== resolved.path) {
    window.history.replaceState(null, "", resolved.path);
  }
  return resolved.view;
}

// 模块加载时只算视图，不动地址：真正的改写留到 setupRouter，那时页面已经准备
// 好渲染，地址栏不会先闪一下旧地址。SSR 或单测里没有 window，直接落到首页。
export const currentView = ref<ViewID>(
  typeof window === "undefined"
    ? homeView
    : resolveLocation(window.location.pathname, window.location.search, window.location.hash).view
);

export function navigate(view: ViewID, query?: Record<string, string>): void {
  const target = viewPath(view, new URLSearchParams(query));
  // pushState 不触发 popstate，视图要自己更新。
  if (currentLocation() !== target) {
    window.history.pushState(null, "", target);
  }
  currentView.value = view;
}

export function viewQuery(): URLSearchParams {
  return new URLSearchParams(window.location.search);
}

export function setupRouter(): void {
  window.addEventListener("popstate", () => {
    currentView.value = applyLocation();
  });
  // 进站时把 #/xxx 老链接、改名前的地址和非法路径一次性收敛到规范地址。
  currentView.value = applyLocation();
}
