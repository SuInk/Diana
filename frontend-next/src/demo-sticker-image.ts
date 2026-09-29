// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

/**
 * 演示模式的表情包示意图。演示模式没有真实图片，<img> 又绕过了 fetch 拦截，
 * 所以按哈希直接生成一张小图，颜色和嘴型随哈希变，看得出是不同的几张。
 */
export function demoStickerImage(hash: string): string {
  const hue = (parseInt(hash.slice(0, 6), 16) || 0) % 360;
  const mouth = parseInt(hash.slice(6, 8), 16) % 2 ? "M44 78 Q64 94 84 78" : "M46 84 L82 84";
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="128" height="128" viewBox="0 0 128 128"><rect width="128" height="128" rx="28" fill="hsl(${hue} 70% 82%)"/><circle cx="64" cy="66" r="44" fill="hsl(${hue} 65% 62%)"/><circle cx="48" cy="58" r="7" fill="#fff"/><circle cx="80" cy="58" r="7" fill="#fff"/><circle cx="49" cy="59" r="3.5" fill="#222"/><circle cx="81" cy="59" r="3.5" fill="#222"/><path d="${mouth}" fill="none" stroke="#222" stroke-width="5" stroke-linecap="round"/></svg>`;
  return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`;
}
