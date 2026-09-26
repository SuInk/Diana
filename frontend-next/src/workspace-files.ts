// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import type { WorkspaceArea, WorkspaceEntry } from "./api";

// 分区按「多久会被清掉」排：长期保存区最该看，回收站和兜底的「其它」垫底。
// 后端返回的顺序不作数，新加的区没登记时排在最后，不会因为不认识就被藏起来。
const areaOrder = ["keep", "downloads", "outputs", "tmp", "browser", "trash", "other"];

export function sortWorkspaceAreas(areas: WorkspaceArea[] | null | undefined): WorkspaceArea[] {
  const rank = (key: string) => {
    const index = areaOrder.indexOf(key);
    return index < 0 ? areaOrder.length : index;
  };
  return [...(areas ?? [])].sort((a, b) => {
    const byArea = rank(a.key) - rank(b.key);
    if (byArea !== 0) return byArea;
    // 多台机器人的长期保存区按名字排，名字一样再按 ID，刷新前后顺序不跳。
    const byName = (a.bot_name || a.bot_id || "").localeCompare(b.bot_name || b.bot_id || "", "zh-CN");
    return byName !== 0 ? byName : (a.bot_id ?? "").localeCompare(b.bot_id ?? "");
  });
}

/** 配额用量的百分比，夹在 0–100；没有配额时返回 null，界面上就不画条。 */
export function workspaceQuotaPercent(bytes: number, quota: number | undefined): number | null {
  if (!quota || quota <= 0) return null;
  return Math.min(100, Math.max(0, (bytes / quota) * 100));
}

/** 分区卡片点下去跳到的目录：「其他目录」的 path 是 "."，对应浏览器里的根目录。 */
export function workspaceAreaTarget(area: Pick<WorkspaceArea, "path">): string {
  const path = area.path.replace(/^(\.\/)+|^\/+|\/+$/g, "");
  return path === "." ? "" : path;
}

const TRASH_DIR = ".trash";

/** 当前目录在回收站里：这时不给单条删除，只给「清空回收站」。 */
export function workspaceInTrash(path: string): boolean {
  return path === TRASH_DIR || path.startsWith(`${TRASH_DIR}/`);
}

// 工作区根下有固定用途的目录：分区本身、运行时状态、编码仓库、Skills。整个挪进回收站
// 只会让 Agent 下次重建，还可能连带扩展开关或编码代理没推上去的改动，界面上不给这个
// 按钮；里面的东西照常能删。长期区每台机器人的目录同理。
const RESERVED_TOP_LEVEL = new Set(["keep", "downloads", "outputs", "tmp", ".agent-browser", TRASH_DIR, ".diana", ".agents", "skills", "coding", "coding-runtime"]);

/**
 * 这一项能不能从页面上删（挪进回收站）。回收站里的东西再删就是真删了，只能整个清空。
 * 凭据和运行时文件能删，但要多确认一次（见 workspaceProtectedLabel）；删链接挪走的是
 * 链接本身，指向的东西不动。当前目录经链接到了工作区外面（insideExternal）时一律不删：
 * 后端删除经 os.Root 走不出工作区，按钮点了也只会报错。
 */
export function workspaceCanDelete(entry: Pick<WorkspaceEntry, "path" | "kind">, insideExternal = false): boolean {
  const path = entry.path.replace(/^\/+|\/+$/g, "");
  if (!path || insideExternal || workspaceInTrash(path)) return false;
  const parts = path.split("/");
  if (parts.length === 1 && RESERVED_TOP_LEVEL.has(path)) return false;
  if (parts.length === 2 && parts[0] === "keep" && entry.kind === "dir") return false;
  return true;
}

// 老版本放在工作区根下、还没搬进 .diana/ 的运行时状态文件。
const LEGACY_RUNTIME_FILES = new Set([".extension-overrides.json", ".extension-audience.json", ".extension-paths.json", ".mcp-presets-hidden.json"]);

/**
 * 受保护条目在页面上的标记：.diana/ 和老位置的扩展开关是「运行时文件」，其余（MCP 配置、
 * 编码代理登录目录、数据库这类）是「凭据」。不受保护的返回空串。
 */
export function workspaceProtectedLabel(entry: Pick<WorkspaceEntry, "path" | "protected">): "" | "凭据" | "运行时文件" {
  if (!entry.protected) return "";
  const path = entry.path.replace(/^\/+|\/+$/g, "");
  if (path === ".diana" || path.startsWith(".diana/") || LEGACY_RUNTIME_FILES.has(path)) return "运行时文件";
  return "凭据";
}

export type WorkspacePreviewKind = "image" | "video" | "audio" | "pdf" | "text";

// 和后端 workspaceMediaTypes 同一份名单：这些直接用对应的标签打开。其余的一律先当
// 文本读一段，后端按内容判断不是文本时再改成只给下载，所以文本扩展名不用列全。
const MEDIA_EXTENSIONS: Partial<Record<string, Exclude<WorkspacePreviewKind, "text">>> = {
  png: "image", jpg: "image", jpeg: "image", gif: "image", webp: "image", bmp: "image", avif: "image", ico: "image", svg: "image",
  mp4: "video", m4v: "video", webm: "video", mov: "video",
  mp3: "audio", wav: "audio", ogg: "audio", oga: "audio", opus: "audio", m4a: "audio", aac: "audio", flac: "audio",
  pdf: "pdf"
};

export function workspaceExtension(name: string): string {
  const dot = name.lastIndexOf(".");
  return dot > 0 ? name.slice(dot + 1).toLowerCase() : "";
}

/** 按扩展名决定预览方式，认不出的先当文本试读。 */
export function workspacePreviewKind(name: string): WorkspacePreviewKind {
  return MEDIA_EXTENSIONS[workspaceExtension(name)] ?? "text";
}
