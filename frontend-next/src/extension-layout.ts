// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import { ref } from "vue";

// 插件、Skills、MCP 是同一个页面里的三个标签，排列方式当然该是同一个设置：
// 在插件那边切成横排，翻到 MCP 又变回方块，会让人以为自己点错了。
export type ExtensionLayout = "tiles" | "rows";

// 沿用插件页原来的键，老用户的选择不丢。
const LAYOUT_KEY = "dqb-next:plugin-layout";

function stored(): ExtensionLayout {
  try {
    return window.localStorage.getItem(LAYOUT_KEY) === "rows" ? "rows" : "tiles";
  } catch {
    // 隐私模式下读不到 localStorage，用默认值继续，不影响页面渲染。
    return "tiles";
  }
}

export const extensionLayout = ref<ExtensionLayout>(stored());

export function setExtensionLayout(next: ExtensionLayout): void {
  extensionLayout.value = next;
  try {
    window.localStorage.setItem(LAYOUT_KEY, next);
  } catch {
    // 存不住就只在这次会话里生效。
  }
}
