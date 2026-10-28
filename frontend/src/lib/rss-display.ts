import type { AssistantTask, RSSWatchSource } from "../api";

export function rssSources(task: AssistantTask): RSSWatchSource[] {
  if (task.feed_sources?.length) return task.feed_sources;
  return task.feed_url ? [{ feed_url: task.feed_url, source: task.feed_source, handle: task.feed_handle }] : [];
}

export function rssSourceLabel(source: RSSWatchSource): string {
  const name = source.name?.trim();
  if (source.source === "twitter" && source.handle) {
    const handle = `@${source.handle.replace(/^@/, "")}`;
    return name && name !== handle && name !== handle.slice(1) ? `X · ${name}（${handle}）` : `X · ${handle}`;
  }
  return name && name !== source.feed_url ? `RSS · ${name}（${source.feed_url}）` : `RSS · ${source.feed_url}`;
}

export function subscriptionPlatformLabel(platform?: string): string {
  return ({ "onebot-v11": "QQ / OneBot", telegram: "Telegram", "qq-official": "QQ 官方" } as Record<string, string>)[platform ?? ""] || platform || "平台未记录";
}

// An explicit empty prompt means original text, including previously generated watches.
export function rssReplyRequirements(task: AssistantTask): string {
  if (task.feed_reply_prompt != null) return task.feed_reply_prompt;
  if (task.feed_reply_mode === "translated") return "忠实翻译成简体中文，保留已有中文，不总结。";
  if (task.feed_reply_mode === "original") return "";
  if (task.feed_reply_mode === "generated" || task.feed_rule_mode === "llm" || (!task.feed_rule_mode && task.feed_judge_prompt?.trim())) return task.feed_judge_prompt || "根据原文生成通知。";
  return "";
}
