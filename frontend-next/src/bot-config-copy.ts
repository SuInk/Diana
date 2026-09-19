import type { BotProfileConfig } from "./api";

// Copy behavior, not a live account or its transport credentials. JSON cloning
// also works for reactive Vue proxies and keeps nested model/persona drafts apart.
export function copyBotConfiguration(source: BotProfileConfig, defaults: BotProfileConfig): BotProfileConfig {
  const copied = JSON.parse(JSON.stringify(source)) as BotProfileConfig;
  const data = copied as unknown as Record<string, unknown>;
  for (const key of Object.keys(data)) {
    if ((/^(onebot_|nonebot_|qq_|dingtalk_|feishu_|wecom_)/.test(key) && key !== "qq_typing_enabled")
      || (key.startsWith("telegram_") && key !== "telegram_suppress_bot_messages")) {
      delete data[key];
    }
  }
  return {
    ...defaults,
    ...copied,
    id: undefined,
    profiles: undefined,
    active_profile_id: undefined,
    message_relays: undefined,
    connection_profile_id: "",
    bot_account: undefined,
    avatar_url: undefined,
    callback_path: defaults.callback_path,
    nonebot_bridge_enabled: false,
    name: `${source.name || "未命名机器人"} 副本`,
    platform: source.platform || defaults.platform,
    enabled: defaults.enabled
  };
}
