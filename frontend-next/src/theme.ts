import { reactive, watchEffect } from "vue";

export type ThemeMode = "auto" | "light" | "dark";

export interface AccentOption {
  id: string;
  label: string;
  color: string;
}

export const accentOptions: AccentOption[] = [
  { id: "diana", label: "嘉然粉", color: "#e0578f" },
  { id: "violet", label: "星夜紫", color: "#7c5cff" },
  { id: "ocean", label: "深海蓝", color: "#2f7df6" },
  { id: "forest", label: "松间绿", color: "#159f6c" }
];

interface ThemeState {
  mode: ThemeMode;
  accent: string;
}

const STORAGE_KEY = "dqb-next:theme";

function load(): ThemeState {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (raw) {
      const parsed = JSON.parse(raw) as Partial<ThemeState>;
      const mode: ThemeMode = parsed.mode === "light" || parsed.mode === "dark" ? parsed.mode : "auto";
      const accent = accentOptions.some((option) => option.id === parsed.accent) ? (parsed.accent as string) : "diana";
      return { mode, accent };
    }
  } catch {
    /* 存储不可用时使用默认主题 */
  }
  return { mode: "auto", accent: "diana" };
}

export const theme = reactive<ThemeState>(load());

const darkQuery = window.matchMedia("(prefers-color-scheme: dark)");

function resolvedMode(): "light" | "dark" {
  if (theme.mode === "auto") {
    return darkQuery.matches ? "dark" : "light";
  }
  return theme.mode;
}

function apply(): void {
  const root = document.documentElement;
  root.dataset.theme = resolvedMode();
  root.dataset.accent = theme.accent;
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) {
    meta.setAttribute("content", resolvedMode() === "dark" ? "#17151a" : "#faf7f8");
  }
}

/** 初始化主题：应用当前配置并监听系统与用户变更。 */
export function setupTheme(): void {
  apply();
  darkQuery.addEventListener("change", apply);
  watchEffect(() => {
    // 读取以建立依赖
    void theme.mode;
    void theme.accent;
    apply();
    try {
      window.localStorage.setItem(STORAGE_KEY, JSON.stringify({ mode: theme.mode, accent: theme.accent }));
    } catch {
      /* 忽略隐私模式下的写入失败 */
    }
  });
}
