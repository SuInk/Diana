import type { AssistantTask, RSSWatchSource } from "./api";

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
  return ({ "onebot-v11": "QQ / OneBot", telegram: "Telegram", "qq-official": "QQ 官方", dingtalk: "钉钉" } as Record<string, string>)[platform ?? ""] || platform || "平台未记录";
}
