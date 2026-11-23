// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

/** 两条消息隔了五分钟以上才插一条时间分隔，照 QQ。 */
export const timeGapSeconds = 5 * 60

export function needsTimeSeparator(previousAt: number | undefined, at: number): boolean {
  return previousAt === undefined || at - previousAt >= timeGapSeconds
}

/** 今天只写时分，昨天写「昨天」，今年写月日，更早带年份。 */
export function formatChatTime(at: number, now: Date = new Date()): string {
  const date = new Date(at * 1000)
  const hm = `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`
  const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime()
  const dayMs = 24 * 60 * 60 * 1000
  if (date.getTime() >= startOfToday) return hm
  if (date.getTime() >= startOfToday - dayMs) return `昨天 ${hm}`
  if (date.getFullYear() === now.getFullYear()) return `${date.getMonth() + 1}月${date.getDate()}日 ${hm}`
  return `${date.getFullYear()}年${date.getMonth() + 1}月${date.getDate()}日 ${hm}`
}
