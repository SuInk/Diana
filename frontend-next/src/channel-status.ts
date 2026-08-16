import type { QQBotChannelStatus } from "./api";

export function channelAccountUnhealthy(channel: QQBotChannelStatus): boolean {
  return Boolean(channel.account_status_known && (!channel.account_online || !channel.account_good));
}

export function channelOperational(channel: QQBotChannelStatus): boolean {
  return channel.connected && !channelAccountUnhealthy(channel);
}

export function channelStatusLabel(channel: QQBotChannelStatus): string {
  if (!channel.connected) return channel.last_error ? "连接失败" : "等待连接";
  if (channel.account_status_known && !channel.account_online) return "QQ 账号离线";
  if (channel.account_status_known && !channel.account_good) return "QQ 状态异常";
  return `已连接 ${channel.self_id || ""}`.trim();
}

export function channelStatusHint(channel: QQBotChannelStatus): string {
  if (channel.account_status_message) return `${channel.account_status_message}；NapCat WebSocket 仍保持连接。`;
  if (channel.last_error) return channel.last_error;
  if (!channel.connected) return "请确认 NapCat 已启动，并检查反向 WebSocket 地址与 Token。";
  return "NapCat WebSocket 与 QQ 账号状态正常。";
}
