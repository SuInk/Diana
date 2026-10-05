// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import type { LLMUsageBreakdown } from "./api";
import { formatNumber } from "./format.ts";

export type UsageDimension = "purpose" | "model";

const purposeLabels: Record<string, string> = {
  reply: "聊天回复",
  subagent: "后台子任务",
  subtask: "子任务",
  proactive_reply_router: "主动回复判断",
  reply_intent_router: "回复意图判断",
  reply_rule_router: "回复规则判断",
  proactive_reply_quality: "回复质量审核",
  reply_send_audit: "发送前审核",
  reply_account_safety: "账号安全审核",
  bot_reply_loop_detection: "机器人循环检测",
  reply_semantic_dedup: "回复语义去重",
  reply_compression: "回复压缩",
  memory_extract: "记忆抽取",
  memory_summary: "记忆归纳",
  context_summary_compaction: "上下文摘要",
  relationship_evaluate: "好感度评估",
  media_parse: "媒体解析",
  image_description: "图片描述",
  image_describe: "图片描述",
  image_description_cache: "图片描述缓存",
  emoji_description: "表情包描述",
  emoji_description_cache: "表情包描述缓存",
  sticker_description: "表情包描述",
  sticker_search: "表情包搜索向量化",
  ocr: "文字识别（OCR）",
  image_ocr: "文字识别（OCR）",
  image_generate: "图片生成",
  image_edit: "图片编辑",
  semantic_index: "语义索引向量化",
  semantic_search: "语义搜索向量化",
  cross_group_semantic: "跨群语义检索向量化",
  semantic_reference: "语义指代解析",
  semantic_text_reference: "文本指代解析",
  inbound_media_reference: "媒体指代解析",
  forward_content_safety: "转发内容审核",
  rss_watch_judge: "RSS 更新判断",
  direct_reply_topic: "回复话题判断",
  welcome_generator: "入群欢迎",
  poke_reply: "戳一戳回复",
  romance_greeting: "主动问候",
  reply_suppression_notice: "回复拦截提示",
  upstream_rejection_notice: "上游拒绝提示",
  account_safety_notice: "账号安全提示",
  error_notice: "错误提示",
  provider_test: "模型测试",
  persona_generate: "AI 生成人设",
  webui_provider_test: "控制台提供商测试",
  webui_llm_test: "控制台模型测试",
  webui_image_test: "控制台生图测试",
  webui_persona_generate: "AI 生成人设",
  webui_persona_lint: "人设检查",
  model_switch_probe: "模型切换探测",
  llm_capability_probe: "模型能力探测",
  repository_watch_follow_up: "仓库订阅跟评",
  plugin_follow_up: "插件跟评",
  unlabeled: "未标注用途"
};

export function usagePurposeLabel(purpose: string): string {
  const key = purpose.trim() || "unlabeled";
  const label = purposeLabels[key];
  return typeof label === "string" ? label : key;
}

export function usageModelKey(entry: Pick<LLMUsageBreakdown, "provider" | "model">): string {
  return JSON.stringify([entry.provider.trim(), entry.model.trim() || "unknown"]);
}

export interface UsageGroup {
  key: string;
  label: string;
  detail: string;
  recorded_calls: number;
  input_tokens: number;
  output_tokens: number;
  cached_input_tokens: number;
  total_tokens: number;
  missing_usage_calls: number;
}

// 筛选的是另一维度：按用途查看时可选模型，按模型查看时可选用途。
// 提供商也在模型键里，同名模型不会把不同提供商的调用混到一行。
export function groupLLMUsage(entries: readonly LLMUsageBreakdown[], dimension: UsageDimension, filter = ""): UsageGroup[] {
  const groups = new Map<string, UsageGroup>();
  for (const entry of entries) {
    const purpose = entry.purpose.trim() || "unlabeled";
    const modelKey = usageModelKey(entry);
    if (filter && filter !== (dimension === "purpose" ? modelKey : purpose)) continue;
    const key = dimension === "purpose" ? purpose : modelKey;
    let group = groups.get(key);
    if (!group) {
      const model = entry.model.trim() || "unknown";
      group = {
        key,
        label: dimension === "purpose" ? usagePurposeLabel(purpose) : (model === "unknown" ? "未知模型" : model),
        detail: dimension === "model" ? entry.provider.trim() : "",
        recorded_calls: 0, input_tokens: 0, output_tokens: 0,
        cached_input_tokens: 0, total_tokens: 0, missing_usage_calls: 0
      };
      groups.set(key, group);
    }
    group.recorded_calls += entry.recorded_calls;
    group.input_tokens += entry.input_tokens;
    group.output_tokens += entry.output_tokens;
    group.cached_input_tokens += entry.cached_input_tokens;
    group.total_tokens += entry.total_tokens;
    group.missing_usage_calls += entry.missing_usage_calls ?? 0;
  }
  return [...groups.values()].sort((a, b) => b.total_tokens - a.total_tokens || b.recorded_calls - a.recorded_calls || a.key.localeCompare(b.key));
}

export function usageShare(tokens: number, total: number): string {
  if (total <= 0) return "—";
  if (tokens > 0 && tokens / total < 0.001) return "<0.1%";
  return `${((tokens / total) * 100).toFixed(1)}%`;
}

/** 缓存利用率 = 命中缓存的输入 Token / 全部输入 Token。缓存命中已包含在输入中。 */
export function cacheUtilization(cachedInputTokens: number, inputTokens: number): string {
  if (inputTokens <= 0) return "—";
  const ratio = cachedInputTokens / inputTokens;
  if (ratio > 0 && ratio < 0.001) return "<0.1%";
  return `${(ratio * 100).toFixed(1)}%`;
}

export function cacheUsageDisplay(cachedInputTokens: number, inputTokens: number): string {
  return `${formatNumber(cachedInputTokens)} Token（${cacheUtilization(cachedInputTokens, inputTokens)}）`;
}
