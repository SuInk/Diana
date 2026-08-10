const pages = [
  {
    title: "概览",
    file: "index.html",
    sections: [
      ["overview", "产品介绍", "Diana AI 助手 QQ Telegram"],
      ["capabilities", "核心能力", "联网 搜索 多通道 模型 事件 token"],
      ["quickstart", "快速开始", "一键 安装 部署 curl PowerShell"],
    ],
  },
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
      ["updates", "更新与回滚", "小黄点 Release 自动更新 备份 健康检查"],
      ["operations", "运行与备份", "日志 SSE SQLite systemd launchd"],
      ["troubleshooting", "故障排查", "不回复 视觉 群管理 队列 超时"],
      ["development", "开发与发布", "CI GitHub Pages 测试 Release"],
    ],
  },
];

const currentFile = window.location.pathname.split("/").pop() || "index.html";
const currentPage = pages.find((page) => page.file === currentFile) || pages[0];

document.querySelector("[data-docs-header]").innerHTML = `
  <a class="skip-link" href="#main">跳到正文</a>
  <header class="topbar">
    <button class="icon-button menu-button" type="button" aria-label="打开目录" title="打开目录" data-menu-toggle><span aria-hidden="true">☰</span></button>
    <a class="brand" href="./index.html" aria-label="Diana 文档首页">
      <span class="brand-mark" aria-hidden="true">D</span><span><strong>Diana</strong><small>文档</small></span>
    </a>
    <nav class="page-tabs" aria-label="文档页面">
      ${pages.map((page) => `<a href="./${page.file}"${page === currentPage ? ' aria-current="page"' : ""}>${page.title}</a>`).join("")}
    </nav>
    <nav class="top-links" aria-label="外部链接">
      <a href="https://github.com/SuInk/Diana/releases/latest">下载</a>
      <a href="https://github.com/SuInk/Diana">GitHub</a>
      <button class="icon-button" type="button" aria-label="切换明暗主题" title="切换明暗主题" data-theme-toggle><span data-theme-icon aria-hidden="true">☾</span></button>
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
      <a href="https://github.com/SuInk/Diana">源代码</a>
      <a href="https://github.com/SuInk/Diana/releases/latest">最新版本</a>
      <a href="https://github.com/SuInk/Diana/issues">问题反馈</a>
      <a href="https://github.com/SuInk/Diana/blob/main/LICENSE">许可证</a>
    </div>
  </footer>`;

const root = document.documentElement;
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

const savedTheme = window.localStorage.getItem("diana-docs-theme");
const preferredDark = window.matchMedia("(prefers-color-scheme: dark)").matches;

function setTheme(theme) {
  root.dataset.theme = theme;
  themeIcon.textContent = theme === "dark" ? "☼" : "☾";
}

setTheme(savedTheme === "dark" || (!savedTheme && preferredDark) ? "dark" : "light");
themeToggle.addEventListener("click", () => {
  const next = root.dataset.theme === "dark" ? "light" : "dark";
  setTheme(next);
  window.localStorage.setItem("diana-docs-theme", next);
});

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
