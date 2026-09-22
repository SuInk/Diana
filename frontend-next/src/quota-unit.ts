// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// token 额度按 K 记：五位数以上的 token 数一个个数零太费眼，而写额度的人心里
// 想的本来就是「五十万」。裸数字按 K 算，带单位时以单位为准，换算结果实时写在
// 提示里——默认单位最怕的就是「我到底填的是五十万还是五亿」，那就把它显出来。
export function parseTokenQuota(text: string): number | undefined {
  const raw = (text ?? "").trim().toLowerCase().replace(/[,，_\s]/g, "");
  if (raw === "") return undefined;
  const matched = /^(\d+(?:\.\d+)?)(k|m|w|万|token|t)?$/.exec(raw);
  if (!matched) return undefined;
  const amount = Number(matched[1]);
  if (!Number.isFinite(amount)) return undefined;
  const unit = matched[2] ?? "k";
  const scale = unit === "m" ? 1_000_000 : unit === "w" || unit === "万" ? 10_000 : unit === "token" || unit === "t" ? 1 : 1_000;
  return Math.round(amount * scale);
}

export function formatTokenQuota(value: number | undefined): string {
  if (!value || value <= 0) return "";
  if (value % 1000 === 0) return String(value / 1000);
  return `${value}token`;
}

// 输入框下面那行实时换算。空值和 0 的说法两处不一样：群里是「跟随机器人」，
// 机器人这一档才是「不限」，所以由调用方把这句话传进来。
export function tokenQuotaReadout(draft: string, fallbackHint: string): string {
  const parsed = parseTokenQuota(draft);
  if (draft.trim() === "") return `${fallbackHint}可写 500（＝500K）、1.5m、50万、8000token。`;
  if (parsed === undefined) return "看不懂这个写法，可写 500、500k、1.5m、50万、8000token。";
  if (parsed <= 0) return `0 等同留空。${fallbackHint}`;
  return `= ${parsed.toLocaleString("en-US")} token`;
}
