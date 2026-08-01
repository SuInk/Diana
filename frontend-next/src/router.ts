import { ref } from "vue";

export type ViewID =
  | "dashboard"
  | "setup"
  | "llm"
  | "test"
  | "bot"
  | "groups"
  | "plugins"
  | "logs"
  | "settings";

export interface NavItem {
  id: ViewID;
  label: string;
  hint: string;
}

export const navItems: NavItem[] = [
  { id: "dashboard", label: "总览", hint: "运行状态与实时事件" },
  { id: "llm", label: "LLM 配置", hint: "Provider 与模型管理" },
  { id: "test", label: "测试台", hint: "连通与对话测试" },
  { id: "bot", label: "机器人", hint: "NapCat 接入与行为" },
  { id: "plugins", label: "插件", hint: "内置插件启停" },
  { id: "groups", label: "群管理", hint: "群管理员自助配置" },
  { id: "logs", label: "日志", hint: "操作与错误日志" },
  { id: "settings", label: "设置", hint: "主题与系统更新" }
];

const validViews = new Set<ViewID>(["dashboard", "setup", "llm", "test", "bot", "groups", "plugins", "logs", "settings"]);

function parseHash(): ViewID {
  const raw = window.location.hash.replace(/^#\/?/, "").split("?")[0] ?? "";
  if (validViews.has(raw as ViewID)) {
    return raw as ViewID;
  }
  return "dashboard";
}

export const currentView = ref<ViewID>(parseHash());

export function navigate(view: ViewID): void {
  if (window.location.hash !== `#/${view}`) {
    window.location.hash = `#/${view}`;
  } else {
    currentView.value = view;
  }
}

export function setupRouter(): void {
  window.addEventListener("hashchange", () => {
    currentView.value = parseHash();
  });
}
