// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import { ref } from "vue";
import { getOwnSpace, listFeed, listSelfNotes } from "./api";

// 侧栏「小窝」的小红点：她自己动过的东西（发了动态、改了自述、花了钱）比主人上次
// 打开小窝更新时亮起。主人在控制台发的零花钱是自己做的，不算新动静。
// 已读时间只存在这个浏览器里，换设备会再亮一次，无伤大雅。

const SEEN_KEY = "diana.space.seen";

export const spaceUnread = ref(false);

function seenAt(profile: string): number {
  try {
    const raw = localStorage.getItem(`${SEEN_KEY}:${profile}`);
    return raw ? Number(raw) || 0 : 0;
  } catch {
    return 0;
  }
}

export function markSpaceSeen(profile: string): void {
  spaceUnread.value = false;
  try {
    localStorage.setItem(`${SEEN_KEY}:${profile}`, String(Date.now()));
  } catch {
    // 存不下就下次再亮，不影响使用。
  }
}

function time(value?: string): number {
  const parsed = value ? Date.parse(value) : NaN;
  return Number.isFinite(parsed) ? parsed : 0;
}

export async function refreshSpaceUnread(profile: string): Promise<void> {
  const [space, feed, notes] = await Promise.allSettled([getOwnSpace(profile), listFeed(profile), listSelfNotes(profile)]);
  let latest = 0;
  if (space.status === "fulfilled") {
    for (const entry of space.value.wallet.recent) {
      if (entry.kind === "spend" || entry.kind === "refund") latest = Math.max(latest, time(entry.created_at));
    }
  }
  if (feed.status === "fulfilled") {
    for (const post of feed.value.posts) latest = Math.max(latest, time(post.created_at));
  }
  if (notes.status === "fulfilled") {
    for (const note of notes.value.notes) latest = Math.max(latest, time(note.updated_at));
  }
  const seen = seenAt(profile);
  // 第一次打开控制台没有已读记录：把现状记成已读，不让老内容一上来就亮红点。
  if (seen === 0) {
    markSpaceSeen(profile);
    return;
  }
  spaceUnread.value = latest > seen;
}
