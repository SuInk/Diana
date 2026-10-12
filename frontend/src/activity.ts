// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import type { ActivityDay } from "./api";

/**
 * 四档按占峰值的比例分，而不是按绝对数：群大群小差几个数量级，固定阈值总有一头全白或全深。
 * 0 档只给真的为 0 的格子。
 */
export function activityLevel(count: number, peak: number): number {
  if (count <= 0 || peak <= 0) return 0;
  return Math.min(4, Math.ceil((count / peak) * 4));
}

/** 颜色从主题强调色混出来，换配色时跟着走。 */
export function activityLevelColor(level: number): string {
  if (level <= 0) return "var(--surface-2)";
  return `color-mix(in srgb, var(--accent) ${[0, 22, 45, 70, 100][Math.min(4, level)]}%, var(--surface))`;
}

/**
 * 统计按服务器时区算。浏览器和服务器在同一个时区时不必多说，不在时要点明，
 * 不然「晚上 9 点最热闹」会被读成浏览器所在地的 9 点。
 */
export function activityTimezoneNote(serverOffset: string | undefined, subject: string, browserOffsetMinutes = -new Date().getTimezoneOffset()): string {
  if (!serverOffset) return "";
  const sign = browserOffsetMinutes >= 0 ? "+" : "-";
  const abs = Math.abs(browserOffsetMinutes);
  const browser = `${sign}${String(Math.floor(abs / 60)).padStart(2, "0")}:${String(abs % 60).padStart(2, "0")}`;
  return browser === serverOffset ? "" : `${subject}按服务器时区 UTC${serverOffset}，和这台电脑的时区（UTC${browser}）不同。`;
}

export interface CalendarCell {
  date: string;
  /** null 表示这一天还没到，日历上不画格子；过去没有消息的日子是 0，画成灰色空格。 */
  count: number | null;
}

export interface CalendarWeek {
  /** 这一列是某个月的第一列时给出月份（1–12），用来在顶上标「N月」。 */
  month?: number;
  days: CalendarCell[];
}

// 日期一律按 UTC 解析、按 UTC 加减：这些是服务器本地的日历日，和浏览器所在时区无关，
// 用本地时间算会在夏令时切换那天多出或少掉一天。
function parseDate(value: string): Date {
  const [year, month, day] = value.split("-").map(Number);
  return new Date(Date.UTC(year, month - 1, day));
}

function formatDate(date: Date): string {
  return date.toISOString().slice(0, 10);
}

/**
 * 排出 GitHub 式的日历：每列一周（周一在上），最后一列是今天所在的那周，往回共 weeks 列。
 * 和 GitHub 一样，过去的每一天都有格子，没有消息就是 0（灰色空格）；只有今天之后的格子
 * count 为 null，不画。
 */
export function buildActivityCalendar(today: string, days: readonly ActivityDay[], weeks: number): CalendarWeek[] {
  const counts = new Map(days.map((day) => [day.date, day.count]));
  const end = parseDate(today);
  const mondayOffset = (end.getUTCDay() + 6) % 7;
  const start = new Date(end);
  start.setUTCDate(end.getUTCDate() - mondayOffset - (weeks - 1) * 7);
  const columns: CalendarWeek[] = [];
  let lastMonth = -1;
  for (let week = 0; week < weeks; week++) {
    const column: CalendarWeek = { days: [] };
    for (let weekday = 0; weekday < 7; weekday++) {
      const date = new Date(start);
      date.setUTCDate(start.getUTCDate() + week * 7 + weekday);
      const key = formatDate(date);
      column.days.push({ date: key, count: key <= today ? (counts.get(key) ?? 0) : null });
    }
    const month = parseDate(column.days[0].date).getUTCMonth() + 1;
    if (month !== lastMonth) {
      // 第一列多半只露出月底几天，标上月份会和下一列的新月份挤在一起，跳过。
      if (week > 0 || parseDate(column.days[0].date).getUTCDate() <= 7) column.month = month;
      lastMonth = month;
    }
    columns.push(column);
  }
  return columns;
}
