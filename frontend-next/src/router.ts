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

function parseHash(): { view: ViewID; param: string } {
  const raw = window.location.hash.replace(/^#\/?/, "").split("?")[0] ?? "";
  // 机器人实例作为子菜单项，用 #/bot/<id> 携带选中的实例，刷新后不丢。
  const [head, ...rest] = raw.split("/");
  if (validViews.has(head as ViewID)) {
    return { view: head as ViewID, param: decodeURIComponent(rest.join("/")) };
  }
  return { view: "dashboard", param: "" };
}

const initial = parseHash();
export const currentView = ref<ViewID>(initial.view);
/** 当前视图的子项标识；目前只有机器人视图用来记住选中的实例。 */
export const currentParam = ref<string>(initial.param);

export function navigate(view: ViewID, param = ""): void {
  const target = param ? `#/${view}/${encodeURIComponent(param)}` : `#/${view}`;
  if (window.location.hash !== target) {
    window.location.hash = target;
  } else {
    currentView.value = view;
    currentParam.value = param;
  }
}

export function setupRouter(): void {
  window.addEventListener("hashchange", () => {
    const next = parseHash();
    currentView.value = next.view;
    currentParam.value = next.param;
  });
}
