// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

/** 常用展示格式化工具。 */

export function formatTime(iso: string | undefined | null): string {
  if (!iso) {
    return "—";
  }
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) {
    return "—";
  }
  return date.toLocaleString("zh-CN", { hour12: false });
}

export function formatClock(iso: string | undefined | null): string {
  if (!iso) {
    return "—";
  }
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) {
    return "—";
  }
  return date.toLocaleTimeString("zh-CN", { hour12: false });
}

export function formatRelative(iso: string | undefined | null): string {
  if (!iso) {
    return "—";
  }
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) {
    return "—";
  }
  const diff = Date.now() - date.getTime();
  if (diff < 0) {
    return formatTime(iso);
  }
  const seconds = Math.floor(diff / 1000);
  if (seconds < 60) {
    return `${seconds} 秒前`;
  }
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) {
    return `${minutes} 分钟前`;
  }
  const hours = Math.floor(minutes / 60);
  if (hours < 24) {
    return `${hours} 小时前`;
  }
  const days = Math.floor(hours / 24);
  if (days < 30) {
    return `${days} 天前`;
  }
  return formatTime(iso);
}

export function formatUptime(totalSeconds: number): string {
  if (!Number.isFinite(totalSeconds) || totalSeconds < 0) {
    return "—";
  }
  const days = Math.floor(totalSeconds / 86400);
  const hours = Math.floor((totalSeconds % 86400) / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  if (days > 0) {
    return `${days} 天 ${hours} 小时`;
  }
  if (hours > 0) {
    return `${hours} 小时 ${minutes} 分`;
  }
  if (minutes > 0) {
    return `${minutes} 分钟`;
  }
  return `${Math.floor(totalSeconds)} 秒`;
}

export function formatNumber(value: number | undefined | null): string {
  if (value === undefined || value === null || Number.isNaN(value)) {
    return "0";
  }
  return value.toLocaleString("zh-CN");
}

export function formatBytes(value: number | undefined | null): string {
  if (value === undefined || value === null || !Number.isFinite(value) || value < 0) {
    return "—";
  }
  if (value === 0) {
    return "0 B";
  }
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  const unitIndex = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1);
  const amount = value / 1024 ** unitIndex;
  const digits = amount >= 100 || unitIndex === 0 ? 0 : amount >= 10 ? 1 : 2;
  return `${amount.toFixed(digits)} ${units[unitIndex]}`;
}

export function formatHourLabel(hourUnix: number): string {
  const date = new Date(hourUnix * 1000);
  return `${String(date.getHours()).padStart(2, "0")}:00`;
}

export function truncate(text: string | undefined | null, max = 80): string {
  if (!text) {
    return "";
  }
  const chars = Array.from(text);
  if (chars.length <= max) {
    return text;
  }
  return chars.slice(0, max).join("") + "…";
}
