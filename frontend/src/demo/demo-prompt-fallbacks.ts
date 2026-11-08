// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 演示站里插件设置的 fallback：和后端一样，留空的文本项直接显示运行时实际用的内容。
import demoPromptCatalogData from "./demo-prompt-catalog.json" with { type: "json" };

const catalog = demoPromptCatalogData as { prompts: Array<{ key: string; default: string }> };

export const demoFollowUpPromptFallback = catalog.prompts.find((prompt) => prompt.key === "tasks.follow_up")?.default ?? "";
export const demoRepositoryWatchTemplateFallback = "GitHub 动态：{repository}\n{body}";
export const demoJoinRequirementFallback = "正常想加入群聊的人通过；广告、引流、诈骗、明显机器人或乱填的拒绝；拿不准的留给群主人。";
