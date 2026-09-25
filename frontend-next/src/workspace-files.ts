// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import type { WorkspaceArea, WorkspaceFileEntry, WorkspaceFilesResponse } from "./api";

/** 下载走普通链接而不是 fetch：大文件交给浏览器自己的下载管理，不用先整个读进内存。 */
export function workspaceDownloadURL(path: string): string {
  return `/api/workspace/download?path=${encodeURIComponent(path)}`;
}

/**
 * 条目在分区里的相对路径：分区标题已经说了在哪个目录，每行再重复一遍
 * 「keep/bot-1/」只会把真正的文件名挤出屏幕。不在分区目录下的原样显示。
 */
export function workspaceEntryLabel(area: Pick<WorkspaceArea, "path">, entry: Pick<WorkspaceFileEntry, "path" | "name">): string {
  const prefix = area.path.replace(/\/+$/, "");
  if (prefix && entry.path.startsWith(`${prefix}/`)) return entry.path.slice(prefix.length + 1);
  return entry.path || entry.name;
}

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

/** 回收站里的东西再删就是真删了，只能走「清空回收站」，单条不给删除按钮。 */
export function workspaceCanDelete(area: Pick<WorkspaceArea, "key">): boolean {
  return area.key !== "trash";
}

/** 配额用量的百分比，夹在 0–100；没有配额时返回 null，界面上就不画条。 */
export function workspaceQuotaPercent(bytes: number, quota: number | undefined): number | null {
  if (!quota || quota <= 0) return null;
  return Math.min(100, Math.max(0, (bytes / quota) * 100));
}

/** 所有分区都没文件、也没有散落文件和闲置编码工作区时才算空，整页换成空状态，不列一排「0 个文件」。 */
export function workspaceIsEmpty(files: WorkspaceFilesResponse | null): boolean {
  if (!files) return true;
  const areasEmpty = (files.areas ?? []).every((area) => area.files === 0 && (area.entries ?? []).length === 0);
  return areasEmpty && (files.loose ?? []).length === 0 && (files.orphan_coding ?? []).length === 0;
}
