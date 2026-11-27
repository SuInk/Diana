// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 落地页脚本。所有语言版本共用，按 <html lang> 或路径识别当前语言。
const SUPPORTED_LANGS = ["zh", "en", "ja", "ko", "fr", "de", "nl"];
const detectLang = () => {
  const htmlLang = (document.documentElement.lang || "").toLowerCase().split("-")[0];
  if (SUPPORTED_LANGS.includes(htmlLang)) return htmlLang;
  const match = window.location.pathname.match(/\/(en|ja|ko|fr|de|nl)(\/|$)/);
  return match ? match[1] : "zh";
};
const lang = detectLang();
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
  ja: {
    themeLabels: { system: "システム", dark: "ダーク", light: "ライト" },
    themeTitle: (l) => `テーマ：${l}（クリックで切替）`,
    copy: "コピー",
    copied: "コピー済み ✓",
  },
  ko: {
    themeLabels: { system: "시스템", dark: "다크", light: "라이트" },
    themeTitle: (l) => `테마: ${l} (클릭하여 전환)`,
    copy: "복사",
    copied: "복사됨 ✓",
  },
  fr: {
    themeLabels: { system: "Système", dark: "Sombre", light: "Clair" },
    themeTitle: (l) => `Thème : ${l} (cliquer pour changer)`,
    copy: "Copier",
    copied: "Copié ✓",
  },
  de: {
    themeLabels: { system: "System", dark: "Dunkel", light: "Hell" },
    themeTitle: (l) => `Design: ${l} (zum Wechseln klicken)`,
    copy: "Kopieren",
    copied: "Kopiert ✓",
  },
  nl: {
    themeLabels: { system: "Systeem", dark: "Donker", light: "Licht" },
    themeTitle: (l) => `Thema: ${l} (klik om te wisselen)`,
    copy: "Kopiëren",
    copied: "Gekopieerd ✓",
  },
}[lang];

// 图片只有一份，放在 docs/assets/；非中文首页在 docs/<lang>/ 下，要往上一级取。
const assetBase = lang === "zh" ? "./" : "../";

  window.DianaIcons.render();

  // 语言切换下拉菜单
  (() => {
    const toggle = document.querySelector("[data-lang-toggle]");
    const menu = document.querySelector("[data-lang-dropdown]");
    if (!toggle || !menu) return;
    const setOpen = (open) => {
      menu.hidden = !open;
      toggle.setAttribute("aria-expanded", String(open));
    };
    toggle.addEventListener("click", (e) => {
      e.stopPropagation();
      setOpen(menu.hidden);
    });
    document.addEventListener("click", (e) => {
      if (!menu.hidden && !menu.contains(e.target) && !toggle.contains(e.target)) {
        setOpen(false);
      }
    });
    document.addEventListener("keydown", (e) => {
      if (e.key === "Escape" && !menu.hidden) {
        setOpen(false);
        toggle.focus();
      }
    });
  })();

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
