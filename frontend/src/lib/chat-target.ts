// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import { ref } from "vue";

// 聊天页里选中的是谁：机器人 ID、"room:<群 ID>" 或管理助手。顶栏的管理对话按钮靠它
// 判断亮不亮，也靠 openChatTarget 让聊天页切到管理助手。
export const CHAT_ADMIN = "admin";

/** 聊天页当前选中的对象，由聊天页写。 */
export const chatSelection = ref("");

/** 别处要求聊天页切到某个对象；聊天页打开或已开着时都会照办，办完清空。 */
export const chatOpenTarget = ref("");

export function openChatTarget(target: string): void {
  chatOpenTarget.value = target;
}
