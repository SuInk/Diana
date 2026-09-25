// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import type { StorageUsage, StorageUsageCategory } from "./api";

export interface StorageSegment {
  key: string;
  label: string;
  bytes: number;
  color: string;
}

/** 分类色固定，不跟随主题强调色：换个配色之后「蓝色是图片」不能变。 */
const categoryColors: Record<string, string> = {
  image: "var(--storage-image)",
  video: "var(--storage-video)",
  audio: "var(--storage-audio)",
  document: "var(--storage-document)",
  database: "var(--storage-database)",
  other: "var(--storage-other)"
};

/** 饼图的分母：有磁盘容量就按整块盘算，读不到就退回只画数据目录。 */
export function storageDiskTotal(usage: StorageUsage | null): number {
  if (!usage) return 0;
  return usage.disk_total_bytes || usage.diana_bytes || 0;
}

/**
 * storageDiskSegments 是饼图那一圈：Diana 自己、别的程序占掉的、还空着的，
 * 三段拼满一圈，所以环上任何一块都可以直接和整块盘比。
 *
 * Diana 在这里只占一段，不按类型拆——一台几百 GB 的机器上，图片视频加起来也
 * 常常不到 1%，拆开只会得到几条看不见的细线。类型拆分放到下面那条按数据目录
 * 自己为分母的条上，两处分母不同，但各自都是满的。
 *
 * 「其它程序与系统」夹到 0 以上：磁盘已用和数据目录体积来自不同的采样时刻，
 * 理论上能出现前者更小的瞬间，画成负数会把环扯断。
 */
export function storageDiskSegments(usage: StorageUsage | null): StorageSegment[] {
  if (!usage) return [];
  if (!usage.disk_total_bytes) {
    return storageCategorySegments(usage);
  }
  return [
    { key: "diana", label: "Diana 数据目录", bytes: usage.diana_bytes, color: "var(--accent)" },
    {
      key: "system",
      label: "其它程序与系统",
      bytes: Math.max(0, (usage.disk_used_bytes ?? 0) - usage.diana_bytes),
      color: "var(--storage-system)"
    },
    { key: "free", label: "可用空间", bytes: usage.disk_free_bytes ?? 0, color: "var(--storage-free)" }
  ];
}

/** 数据目录内部的拆分，分母是数据目录自己。 */
export function storageCategorySegments(usage: StorageUsage | null): StorageSegment[] {
  if (!usage) return [];
  return usage.categories.map((category) => ({
    key: category.key,
    label: category.label,
    bytes: category.bytes,
    color: categoryColors[category.key] ?? categoryColors.other!
  }));
}

/**
 * 按目录的拆分：回答「是哪块在涨」，和上面按文件类型的拆分互补——同样是图片，
 * 在 workspace/downloads 里会被自动清理，在 history-media 里归保留策略管。
 * 旧后端没有这个字段，空目录（0 字节也没有文件）不占一行。
 */
export function storageDirectories(usage: StorageUsage | null): StorageUsageCategory[] {
  if (!usage || !Array.isArray(usage.directories)) return [];
  return usage.directories.filter((directory) => directory.bytes > 0 || directory.files > 0);
}

/** 条形图的宽度只能是纯百分比：占比标签里的 `<0.1%` 拿来当 CSS 宽度是无效值。 */
export function storageWidth(bytes: number, total: number): string {
  if (total <= 0 || bytes <= 0) return "0%";
  return `${Math.min(100, (bytes / total) * 100)}%`;
}

/** 小于 10% 的多给一位小数，否则一堆分类全挤成 0%。 */
export function storageShareLabel(bytes: number, total: number): string {
  if (total <= 0 || bytes < 0) return "—";
  const percent = (bytes / total) * 100;
  if (percent > 0 && percent < 0.1) return "<0.1%";
  return `${percent >= 10 ? percent.toFixed(0) : percent.toFixed(1)}%`;
}
