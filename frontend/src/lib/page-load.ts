// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import { autoReloadAllowed, markAutoReloaded } from "./backendState";

// 升级后，旧标签页仍会请求上个构建的页面资源。重新读取入口才能拿到新文件名。
// 与后端重启共用冷却标记，网络故障或入口缓存异常时不能进入刷新循环。
export function setupPageLoadRecovery(): void {
  window.addEventListener("vite:preloadError", () => {
    if (!autoReloadAllowed()) return;
    markAutoReloaded();
    window.location.reload();
    // 不 preventDefault：重载未完成或被浏览器拦下时，异步组件仍能展示错误提示。
  });
}
