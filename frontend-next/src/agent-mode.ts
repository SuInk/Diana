// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

/**
 * Agent 模式（标准 / 安全）的界面逻辑。
 *
 * 安全模式关掉什么由后端 AgentSafeModeRules 那张表说了算，这里只负责把它排成人话；
 * 不在前端另抄一份清单，抄一份迟早和后端对不上。
 */

export type AgentMode = "standard" | "safe";

export interface AgentSafeModeRule {
  category: string;
  tool: string;
  field?: string;
  operations?: string[];
  reason: string;
}

export interface AgentSafeModeCategory {
  id: string;
  label: string;
  impact: string;
  rules: AgentSafeModeRule[];
}

/** 切换前能便宜查到的现场情况；查不到的项为 null，确认框里就不提。 */
export interface AgentModeImpact {
  runningCodingJobs: number | null;
  enabledMCPServers: number | null;
}

export interface AgentModeConfirmRequest {
  title: string;
  message: string;
  confirmLabel: string;
  danger: boolean;
}

export const agentModeOptions: { value: AgentMode; label: string }[] = [
  { value: "standard", label: "标准模式（全部能力）" },
  { value: "safe", label: "安全模式（关掉高风险能力）" }
];

/** 后端迁移后一定带 agent_mode；万一没有（旧后端），按旧开关换算，规则和后端一致。 */
export function normalizeAgentMode(mode: string | undefined, legacyEnabled?: boolean): AgentMode {
  const value = (mode ?? "").trim().toLowerCase();
  if (value === "standard") return "standard";
  if (value === "") return legacyEnabled ? "standard" : "safe";
  // 认不出的值按安全模式：配置写错不该变成权限放开。
  return "safe";
}

export function agentModeLabel(mode: AgentMode): string {
  return mode === "standard" ? "标准模式" : "安全模式";
}

/** 只有「切进」安全模式要二次确认；切回标准模式是主人明确要放开，不拦。 */
export function needsSafeModeConfirm(from: AgentMode, to: AgentMode): boolean {
  return from !== "safe" && to === "safe";
}

/** 规则在界面上的一行写法：整个工具只写名字，部分操作带上操作名。 */
export function safeModeRuleLabel(rule: AgentSafeModeRule): string {
  const operations = rule.operations ?? [];
  if (operations.length === 0) return rule.tool;
  return `${rule.tool}（${operations.join("/")}）`;
}

// 规则表拿不到（接口失败、演示站离线）时的兜底：宁可说得粗一点，也不能让确认框什么
// 都不说。
const fallbackImpacts = [
  "本机命令和编码代理停用",
  "已启用的 MCP 工具不可用，不能安装 Skill 和 MCP",
  "内置浏览器（主人登录态）不能再操作",
  "GitHub 写操作、跨会话/跨群发送停用",
  "不能再写入或整理工作区文件",
  "不能改机器人配置和群管操作"
];

export function safeModeImpactLines(categories: AgentSafeModeCategory[]): string[] {
  const lines = categories.map((category) => category.impact.trim()).filter(Boolean);
  return lines.length > 0 ? lines : fallbackImpacts;
}

/** 现场情况的那几句；都查不到就是空数组。 */
export function safeModeLiveImpactLines(impact: AgentModeImpact | null): string[] {
  if (!impact) return [];
  const lines: string[] = [];
  if (impact.runningCodingJobs !== null && impact.runningCodingJobs > 0) {
    lines.push(`有 ${impact.runningCodingJobs} 个编码任务正在运行：不会被中断，但之后不能再派新任务。`);
  }
  if (impact.enabledMCPServers !== null && impact.enabledMCPServers > 0) {
    lines.push(`这台机器人已启用的 ${impact.enabledMCPServers} 个 MCP 服务的工具将不可用。`);
  }
  return lines;
}

export function safeModeSwitchConfirm(categories: AgentSafeModeCategory[], impact: AgentModeImpact | null): AgentModeConfirmRequest {
  const sections = [
    "安全模式对所有人生效，包括主人本人。切换后下面这些会停止工作：",
    safeModeImpactLines(categories).map((line) => `· ${line}`).join("\n")
  ];
  const live = safeModeLiveImpactLines(impact);
  if (live.length > 0) sections.push("当前情况：\n" + live.map((line) => `· ${line}`).join("\n"));
  sections.push("查资料、记忆、提醒订阅、画图和读取工作区文件照常。确认后还要点「保存配置」才生效，随时可以切回标准模式。");
  return {
    title: "切换到安全模式？",
    message: sections.join("\n\n"),
    confirmLabel: "确认切换到安全模式",
    danger: true
  };
}
