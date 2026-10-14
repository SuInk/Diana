// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import { ref } from "vue";
import { beginScopeTransition } from "./scope-transition";
import type { BotProfileConfig } from "../api";

// 控制台的「当前机器人」作用域。
//
// 多机器人部署里，每个页面各配一个筛选器会让人来回选同一件事；一个全局开关切一次，
// 各页跟着走。空串表示「全部机器人」。
//
// 状态只存在浏览器本地：作用域是「我这个窗口在看什么」，两个人同时开控制台本来就该
// 各看各的，服务端也没有「当前机器人」这种全局状态。
const STORAGE_KEY = "diana:bot-scope";

export const ALL_PROFILES = "";

function readStored(): string {
  try {
    return window.localStorage.getItem(STORAGE_KEY) ?? ALL_PROFILES;
  } catch {
    // 隐私模式或禁用了站点数据时读不到，按「全部」处理即可。
    return ALL_PROFILES;
  }
}

export const botScope = ref<string>(readStored());

// 侧栏选择器里的「添加机器人」：机器人页可能已经缓存着，路由不变时收不到信号，
// 所以用一个标志位传过去，机器人页看到就打开选平台的弹窗，处理完清掉。
export const botCreateRequested = ref(false);

export function setBotScope(profileID: string): void {
  const next = (profileID ?? "").trim();
  if (botScope.value === next) {
    return;
  }
  beginScopeTransition();
  botScope.value = next;
  try {
    if (next === ALL_PROFILES) {
      window.localStorage.removeItem(STORAGE_KEY);
    } else {
      window.localStorage.setItem(STORAGE_KEY, next);
    }
  } catch {
    // 存不下就只在本次会话里生效，不该因此中断切换。
  }
}

// 切换作用域会把各页的缓存整个换掉，正在编辑的机器人配置也随之丢弃。有未保存
// 修改的页面在这里登记一个守卫，用户切换前先问一句；返回 false 就不切。
let leaveGuard: (() => Promise<boolean>) | null = null;

export function setScopeLeaveGuard(guard: (() => Promise<boolean>) | null): void {
  leaveGuard = guard;
}

/** 用户主动切换作用域：先过守卫。删掉机器人后的自动纠正仍直接走 setBotScope。 */
export async function requestBotScope(profileID: string): Promise<void> {
  if ((profileID ?? "").trim() === botScope.value) return;
  if (leaveGuard && !(await leaveGuard())) return;
  setBotScope(profileID);
}

// reconcileBotScope 在配置档列表变化后校正作用域：选中的机器人被删掉时退回「全部」，
// 否则页面会一直按一个不存在的 ID 过滤，看起来像「什么都没有」。
export function reconcileBotScope(profiles: BotProfileConfig[]): void {
  if (botScope.value === ALL_PROFILES) {
    return;
  }
  if (!profiles.some((profile) => profile.id === botScope.value)) {
    setBotScope(ALL_PROFILES);
  }
}

// matchesBotScope 判断一条带 profile_id 的记录是否属于当前作用域。留空的记录
// （旧数据、或运行时没记下来源）在具体机器人视图里不显示，避免把来源不明的东西
// 算到某一台头上。
export function matchesBotScope(profileID?: string): boolean {
  if (botScope.value === ALL_PROFILES) {
    return true;
  }
  return (profileID ?? "").trim() === botScope.value;
}
