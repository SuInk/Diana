// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 落地页脚本。中英两版共用，文案按路径切换——/en/ 下是英文。
const lang = window.location.pathname.includes("/en/") ? "en" : "zh";
const t = {
  zh: {
    themeLabels: { system: "跟随系统", dark: "深色", light: "浅色" },
    themeTitle: (l) => `主题：${l}（点击切换）`,
    copy: "复制",
    copied: "已复制 ✓",
  },
  en: {
    themeLabels: { system: "System", dark: "Dark", light: "Light" },
    themeTitle: (l) => `Theme: ${l} (click to change)`,
    copy: "Copy",
    copied: "Copied ✓",
  },
}[lang];

// 图片只有一份，放在 docs/assets/；英文页在 docs/en/ 下，要往上一级取。
const assetBase = lang === "en" ? "../" : "./";

  window.DianaIcons.render();

  // 首屏安装命令按访客平台切换
  (() => {
    const tabs = document.querySelectorAll(".install-tab");
    const cmds = document.querySelectorAll(".install-line code");
    const copy = document.getElementById("install-copy");
    let active = /Win/i.test(navigator.userAgentData?.platform || navigator.platform || navigator.userAgent) ? "ps" : "sh";
    const render = () => {
      tabs.forEach((t) => t.classList.toggle("active", t.dataset.os === active));
      cmds.forEach((c) => (c.hidden = c.dataset.os !== active));
    };
    tabs.forEach((t) => t.addEventListener("click", () => { active = t.dataset.os; render(); }));
    copy.addEventListener("click", async () => {
      try {
        await navigator.clipboard.writeText(document.getElementById("install-cmd-" + active).innerText.trim());
        copy.textContent = t.copied;
        setTimeout(() => (copy.textContent = t.copy), 1600);
      } catch { /* 剪贴板不可用时静默 */ }
    });
    render();
  })();
  // 三态主题按钮：跟随系统 → 深色 → 浅色，与文档页共用 theme.js 的偏好。
  (() => {
    const button = document.querySelector("[data-theme-toggle]");
    const icon = document.querySelector("[data-theme-icon]");
    const glyphs = {
      system: window.DianaIcons.svg("sun-moon", 18),
      dark: window.DianaIcons.svg("moon", 18),
      light: window.DianaIcons.svg("sun", 18),
    };
    const labels = t.themeLabels;
    const shot = document.getElementById("console-shot");
    const textNode = document.querySelector("[data-theme-text]");
    window.DianaTheme.subscribe((preference, resolved) => {
      icon.innerHTML = glyphs[preference];
      // 图标之外再写出当前模式：只有图标的话，看不出这个按钮是干什么的。
      if (textNode) textNode.textContent = labels[preference];
      const text = t.themeTitle(labels[preference]);
      button.setAttribute("title", text);
      button.setAttribute("aria-label", text);
      shot.src =
        resolved === "light"
          ? `${assetBase}assets/diana-webui-overview-light.png`
          : `${assetBase}assets/diana-webui-overview.png`;
    });
    button.addEventListener("click", () => window.DianaTheme.cycle());
  })();

  document.querySelectorAll(".copy-btn").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const el = document.getElementById(btn.dataset.copy);
      if (!el) return;
      try {
        await navigator.clipboard.writeText(el.innerText.replace(/^#.*\n/gm, "").trim());
        btn.textContent = t.copied;
        setTimeout(() => (btn.textContent = t.copy), 1600);
      } catch { /* 剪贴板不可用时静默 */ }
    });
  });
