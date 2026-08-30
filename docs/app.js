// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

const pages = [
  {
    title: "部署",
    file: "deploy.html",
    sections: [
      ["one-click", "一键安装", "部署 更新 SHA-256 校验 备份 健康检查"],
      ["installer-options", "安装参数", "环境变量 版本 端口 管理员"],
      ["manual-release", "手工 Release", "下载 完整包 离线"],
      ["docker", "Docker", "容器 compose NapCat"],
      ["source", "源码部署", "Go Node npm 构建"],
      ["first-run", "首次登录", "管理员 验证码 模型 通道"],
    ],
  },
  {
    title: "配置",
    file: "configuration.html",
    sections: [
      ["channels", "QQ 与 Telegram", "OneBot NapCat Bot API 多通道 隔离"],
      ["models", "模型与视觉", "LLM Provider 生图 OCR token 超时"],
      ["groups", "群聊策略", "群管理 回复时间 屏蔽 QQ 触发词"],
      ["agent", "Agent 与工具", "联网搜索 MCP Skills 提醒 订阅"],
      ["security", "安全边界", "HTTPS token 密钥 调试"],
    ],
  },
  {
    title: "实现",
    file: "implementation.html",
    sections: [
      ["architecture", "系统架构", "Go Gin Vue SQLite 模块"],
      ["message-flow", "消息调用链", "回复 不回复 原因 工具 上下文"],
      ["memory", "记忆与检索", "长期记忆 跨群 超长历史 压缩 摘要"],
      ["media", "图片与文件", "视觉 OCR 原图 视频 缓存"],
      ["storage", "数据存储", "SQLite 数据库 媒体 日志"],
    ],
  },
  {
    title: "运维",
    file: "operations.html",
    sections: [
      ["updates", "更新与回滚", "小黄点 Release 手动更新 备份 健康检查"],
      ["operations", "运行与备份", "日志 SSE SQLite systemd launchd"],
      ["troubleshooting", "故障排查", "不回复 视觉 群管理 队列 超时"],
      ["development", "开发与发布", "CI GitHub Pages 测试 Release"],
    ],
  },
];

const currentFile = window.location.pathname.split("/").pop() || "index.html";
const currentPage = pages.find((page) => page.file === currentFile) || pages[0];

const icons = {
  menu: '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M4 6h16M4 12h16M4 18h16"/></svg>',
  home: '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 10.5 12 3l9 7.5"/><path d="M5 9.5V21h14V9.5"/></svg>',
  play: '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M10 8.5 16 12l-6 3.5z"/></svg>',
  github: '<svg width="15" height="15" viewBox="0 0 16 16" fill="currentColor"><path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27s1.36.09 2 .27c1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8Z"/></svg>',
  sun: '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>',
  monitor: '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="12" rx="2"/><path d="M8 20h8M12 16v4"/></svg>',
  moon: '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 14.5A8.5 8.5 0 0 1 9.5 4a8.5 8.5 0 1 0 10.5 10.5z"/></svg>'
};

document.querySelector("[data-docs-header]").innerHTML = `
  <a class="skip-link" href="#main">跳到正文</a>
  <header class="topbar">
    <button class="icon-button menu-button" type="button" aria-label="打开目录" title="打开目录" data-menu-toggle>${icons.menu}</button>
    <a class="brand" href="./index.html" aria-label="Diana 首页">
      <span class="brand-mark" aria-hidden="true">D</span><span><strong>Diana</strong><small>文档</small></span>
    </a>
    <nav class="page-tabs" aria-label="文档页面">
      ${pages.map((page) => `<a href="./${page.file}"${page === currentPage ? ' aria-current="page"' : ""}>${page.title}</a>`).join("")}
    </nav>
    <nav class="top-links" aria-label="外部链接">
      <a href="./index.html" title="返回首页">${icons.home}首页</a>
      <a href="./demo/" title="在线演示">${icons.play}演示</a>
      <a class="icon-link" href="https://github.com/SuInk/Diana" aria-label="GitHub 仓库" title="GitHub 仓库">${icons.github}</a>
      <button class="icon-button" type="button" data-theme-toggle><span data-theme-icon aria-hidden="true"></span></button>
    </nav>
  </header>`;

const sidebarRoot = document.querySelector("[data-docs-sidebar]");
sidebarRoot.innerHTML = `
  <aside class="sidebar" data-sidebar>
    <div class="sidebar-search">
      <label for="docs-search">搜索全部文档</label>
      <input id="docs-search" type="search" placeholder="部署、模型、NapCat…" autocomplete="off" data-search />
      <p class="search-status" data-search-status aria-live="polite"></p>
    </div>
    <nav class="sidebar-nav" aria-label="文档目录" data-nav>
      ${pages.map((page) => `<p>${page.title}</p>${page.sections.map(([id, title, keywords]) => `<a href="./${page.file}#${id}" data-search-text="${page.title} ${title} ${keywords}">${title}</a>`).join("")}`).join("")}
    </nav>
  </aside>
  <div class="sidebar-backdrop" data-sidebar-backdrop></div>`;

document.querySelector("[data-docs-footer]").innerHTML = `
  <footer class="footer">
    <div><strong>Diana</strong><p>多平台 AI 助手服务</p></div>
    <div>
      <a href="./demo/">在线演示</a>
      <a href="https://github.com/SuInk/Diana">源代码</a>
      <a href="https://github.com/SuInk/Diana/releases/latest">最新版本</a>
      <a href="https://github.com/SuInk/Diana/issues">问题反馈</a>
      <a href="https://github.com/SuInk/Diana/blob/main/LICENSE">许可证</a>
    </div>
  </footer>`;

const sidebar = document.querySelector("[data-sidebar]");
const sidebarBackdrop = document.querySelector("[data-sidebar-backdrop]");
const menuToggle = document.querySelector("[data-menu-toggle]");
const themeToggle = document.querySelector("[data-theme-toggle]");
const themeIcon = document.querySelector("[data-theme-icon]");
const nav = document.querySelector("[data-nav]");
const search = document.querySelector("[data-search]");
const searchStatus = document.querySelector("[data-search-status]");

function setSidebarOpen(open) {
  sidebar.classList.toggle("open", open);
  sidebarBackdrop.classList.toggle("open", open);
  menuToggle.setAttribute("aria-expanded", String(open));
}

menuToggle.addEventListener("click", () => setSidebarOpen(!sidebar.classList.contains("open")));
sidebarBackdrop.addEventListener("click", () => setSidebarOpen(false));
nav.querySelectorAll("a").forEach((link) => link.addEventListener("click", () => setSidebarOpen(false)));

// 主题偏好由 theme.js 统一管理（它在 <head> 里已经把 data-theme 定好了，
// 这里只负责把按钮和当前偏好对上）。三态循环：跟随系统 → 深色 → 浅色。
const themeLabels = { system: "跟随系统", dark: "深色", light: "浅色" };
const themeIcons = { system: icons.monitor, dark: icons.moon, light: icons.sun };

window.DianaTheme.subscribe((preference) => {
  themeIcon.innerHTML = themeIcons[preference];
  const label = `主题：${themeLabels[preference]}（点击切换）`;
  themeToggle.setAttribute("title", label);
  themeToggle.setAttribute("aria-label", label);
});

themeToggle.addEventListener("click", () => window.DianaTheme.cycle());

document.querySelectorAll("[data-tab]").forEach((button) => {
  button.addEventListener("click", () => {
    const group = button.closest("[data-tabs-root]");
    group.querySelectorAll("[data-tab]").forEach((item) => item.setAttribute("aria-selected", String(item === button)));
    group.querySelectorAll("[data-panel]").forEach((panel) => { panel.hidden = panel.dataset.panel !== button.dataset.tab; });
  });
});

document.querySelectorAll("pre").forEach((block) => {
  const button = document.createElement("button");
  button.type = "button";
  button.className = "copy-button";
  button.textContent = "复制";
  button.setAttribute("aria-label", "复制代码");
  button.addEventListener("click", async () => {
    await navigator.clipboard.writeText(block.querySelector("code")?.textContent || "");
    button.textContent = "已复制";
    window.setTimeout(() => { button.textContent = "复制"; }, 1200);
  });
  block.append(button);
});

const navLinks = [...nav.querySelectorAll("a")];
const navHeadings = [...nav.querySelectorAll("p")];
search.addEventListener("input", () => {
  const query = search.value.trim().toLocaleLowerCase();
  let count = 0;
  navLinks.forEach((link) => {
    const matched = !query || `${link.dataset.searchText} ${link.textContent}`.toLocaleLowerCase().includes(query);
    link.hidden = !matched;
    if (matched && query) count += 1;
  });
  navHeadings.forEach((heading) => {
    let next = heading.nextElementSibling;
    let hasVisibleLink = false;
    while (next && next.tagName === "A") {
      hasVisibleLink ||= !next.hidden;
      next = next.nextElementSibling;
    }
    heading.hidden = !hasVisibleLink;
  });
  searchStatus.textContent = query ? `找到 ${count} 个相关章节` : "";
});

const localSections = [...document.querySelectorAll(".searchable[id]")];
function updateActiveLink(id) {
  navLinks.forEach((link) => {
    const url = new URL(link.href);
    link.classList.toggle("active", url.pathname.endsWith(currentPage.file) && url.hash === `#${id}`);
  });
}

const observer = new IntersectionObserver((entries) => {
  const visible = entries.filter((entry) => entry.isIntersecting).sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top)[0];
  if (visible) updateActiveLink(visible.target.id);
}, { rootMargin: "-20% 0px -72% 0px" });
localSections.forEach((section) => observer.observe(section));
updateActiveLink(window.location.hash.slice(1) || localSections[0]?.id || "");
