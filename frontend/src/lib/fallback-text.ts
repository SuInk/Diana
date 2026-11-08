// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 「留空用默认 / 留空跟随机器人」的文本框统一这样处理：框里直接显示运行时实际用的
// 内容，不让用户对着空框猜；保存时内容和它一样仍存空，继续跟着默认或机器人走。

export function normalizeFallbackText(value: string | undefined | null): string {
  return (value ?? "").replace(/\r\n/g, "\n").trim();
}

/** 框里显示的内容：有自己的值就显示自己的，否则显示实际生效的那份。 */
export function shownFallbackText(value: string | undefined | null, fallback: string | undefined | null): string {
  return normalizeFallbackText(value) === "" ? (fallback ?? "") : (value ?? "");
}

/** 要存的内容：和实际生效的那份一样就存空串。 */
export function storedFallbackText(draft: string, fallback: string | undefined | null): string {
  const normalizedFallback = normalizeFallbackText(fallback);
  return normalizedFallback !== "" && normalizeFallbackText(draft) === normalizedFallback ? "" : draft;
}

/** 有可显示的默认内容，且当前没有自己的值。 */
export function usingFallbackText(value: string | undefined | null, fallback: string | undefined | null): boolean {
  return normalizeFallbackText(fallback) !== "" && normalizeFallbackText(value) === "";
}
