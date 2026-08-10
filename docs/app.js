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
  sidebar?.classList.toggle("open", open);
  sidebarBackdrop?.classList.toggle("open", open);
  menuToggle?.setAttribute("aria-expanded", String(open));
}

menuToggle?.addEventListener("click", () => setSidebarOpen(!sidebar?.classList.contains("open")));
sidebarBackdrop?.addEventListener("click", () => setSidebarOpen(false));
nav?.querySelectorAll("a").forEach((link) => link.addEventListener("click", () => setSidebarOpen(false)));

const savedTheme = window.localStorage.getItem("diana-docs-theme");
const preferredDark = window.matchMedia("(prefers-color-scheme: dark)").matches;
setTheme(savedTheme === "dark" || (!savedTheme && preferredDark) ? "dark" : "light");

function setTheme(theme) {
  root.dataset.theme = theme;
  if (themeIcon) themeIcon.textContent = theme === "dark" ? "☼" : "☾";
}

themeToggle?.addEventListener("click", () => {
  const next = root.dataset.theme === "dark" ? "light" : "dark";
  setTheme(next);
  window.localStorage.setItem("diana-docs-theme", next);
});

document.querySelectorAll("[data-tab]").forEach((button) => {
  button.addEventListener("click", () => {
    const target = button.dataset.tab;
    document.querySelectorAll("[data-tab]").forEach((item) => {
      item.setAttribute("aria-selected", String(item === button));
    });
    document.querySelectorAll("[data-panel]").forEach((panel) => {
      panel.hidden = panel.dataset.panel !== target;
    });
  });
});

document.querySelectorAll("pre").forEach((block) => {
  const button = document.createElement("button");
  button.type = "button";
  button.className = "copy-button";
  button.textContent = "复制";
  button.setAttribute("aria-label", "复制代码");
  button.addEventListener("click", async () => {
    const value = block.querySelector("code")?.textContent || "";
    await navigator.clipboard.writeText(value);
    button.textContent = "已复制";
    window.setTimeout(() => (button.textContent = "复制"), 1200);
  });
  block.append(button);
});

const navLinks = [...(nav?.querySelectorAll("a") || [])];
const navHeadings = [...(nav?.querySelectorAll("p") || [])];
const sections = [...document.querySelectorAll(".searchable")];

search?.addEventListener("input", () => {
  const query = search.value.trim().toLocaleLowerCase();
  let count = 0;
  navLinks.forEach((link) => {
    const target = document.querySelector(link.getAttribute("href"));
    const haystack = `${link.textContent || ""} ${target?.dataset.title || ""} ${target?.textContent || ""}`.toLocaleLowerCase();
    const matched = !query || haystack.includes(query);
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
  if (searchStatus) searchStatus.textContent = query ? `找到 ${count} 个相关章节` : "";
});

const sectionByID = new Map(sections.map((section) => [section.id, section]));
const observer = new IntersectionObserver(
  (entries) => {
    const visible = entries
      .filter((entry) => entry.isIntersecting)
      .sort((left, right) => left.boundingClientRect.top - right.boundingClientRect.top)[0];
    if (!visible) return;
    navLinks.forEach((link) => {
      link.classList.toggle("active", link.getAttribute("href") === `#${visible.target.id}`);
    });
  },
  { rootMargin: "-20% 0px -72% 0px" }
);

sectionByID.forEach((section) => observer.observe(section));
