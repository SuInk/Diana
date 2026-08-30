// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 主题控制器，落地页和文档页共用。
//
// 偏好有三种：system / dark / light。data-theme 上永远只写解析后的 dark 或
// light，样式表因此只需要处理两种取值；「跟随系统」是偏好层的概念，不往 CSS 里漏。
//
// 这个文件必须在 <head> 里同步加载：主题要在首次绘制前定下来，否则深色偏好的用户
// 会先闪一下浅色。
(() => {
  const STORAGE_KEY = "diana-theme";
  const MODES = ["system", "dark", "light"];
  const media = window.matchMedia("(prefers-color-scheme: dark)");
  const listeners = new Set();

  function readPreference() {
    let saved = null;
    try {
      saved = window.localStorage.getItem(STORAGE_KEY);
    } catch {
      // 隐私模式下 localStorage 会抛错。读不到就按跟随系统处理，不影响使用。
    }
    return MODES.includes(saved) ? saved : "system";
  }

  let preference = readPreference();

  function resolve(mode) {
    if (mode === "system") return media.matches ? "dark" : "light";
    return mode;
  }

  function apply() {
    const resolved = resolve(preference);
    document.documentElement.dataset.theme = resolved;
    // 移动端浏览器用它给地址栏上色，跟着主题走才不会突兀。
    const meta = document.querySelector('meta[name="theme-color"]');
    if (meta) meta.setAttribute("content", resolved === "dark" ? "#100d13" : "#ffffff");
    listeners.forEach((fn) => fn(preference, resolved));
  }

  // 跟随系统时，系统切换要立刻反映出来，不必刷新页面。
  const onSystemChange = () => {
    if (preference === "system") apply();
  };
  if (media.addEventListener) media.addEventListener("change", onSystemChange);
  else media.addListener(onSystemChange);

  window.DianaTheme = {
    MODES,
    get preference() {
      return preference;
    },
    get resolved() {
      return resolve(preference);
    },
    set(mode) {
      if (!MODES.includes(mode)) return;
      preference = mode;
      try {
        // 显式选了「跟随系统」也要存下来：不存的话下次读取会退回默认，
        // 用户就没法把已经选过的深色改回跟随系统。
        window.localStorage.setItem(STORAGE_KEY, mode);
      } catch {
        // 存不下就只在本次会话内生效。
      }
      apply();
    },
    cycle() {
      this.set(MODES[(MODES.indexOf(preference) + 1) % MODES.length]);
    },
    /** 注册回调并立即触发一次，供按钮同步图标和文案。 */
    subscribe(fn) {
      listeners.add(fn);
      fn(preference, resolve(preference));
      return () => listeners.delete(fn);
    }
  };

  apply();
})();
