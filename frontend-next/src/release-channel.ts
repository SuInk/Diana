// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

export type UpdateChannel = "release" | "beta" | "canary";

// 与 webui/system_update_channel.go 的 channelTagPattern 保持一致：只有文档里写明的
// 标签格式能进更新通道，其他预发布（含源码构建）一律不算候选。
const channelTagPattern = /^v?[0-9]+\.[0-9]+\.[0-9]+(?:-(canary|beta|rc)\.(0|[1-9][0-9]*))?(?:\+[0-9A-Za-z.-]+)?$/;

/** 与后端 releaseAllowed 相同：这个 Release 能不能出现在所选通道的更新候选里。 */
export function releaseAllowedOnChannel(release: { tag: string; prerelease?: boolean }, channel: string): boolean {
  const match = channelTagPattern.exec(release.tag);
  if (!match) return false;
  switch (match[1]) {
    case undefined:
      return !release.prerelease;
    case "beta":
    case "rc":
      return channel === "beta" || channel === "canary";
    case "canary":
      return channel === "canary";
  }
  return false;
}

export interface ChannelSwitchConfirm {
  title: string;
  message: string;
  confirmLabel: string;
  danger: boolean;
}

/** 切换更新通道前的确认文案。autoInstall 开着时切到预发布通道要额外提醒。 */
export function channelSwitchConfirm(target: UpdateChannel, autoInstall: boolean): ChannelSwitchConfirm {
  const autoInstallNote = autoInstall ? "「自动重启并安装」已开启，符合条件的新版本下载校验后会自动安装并重启。" : "";
  switch (target) {
    case "canary":
      return {
        title: "切换到 Canary 通道？",
        message: "Canary 是每次合并到 main 自动构建的版本，未经人工验证，可能包含尚未发现的问题。之后会收到 Canary、Beta、RC 和正式版中最新的一个。" +
          autoInstallNote + "切回其他通道不会自动降级。",
        confirmLabel: "切换到 Canary",
        danger: true
      };
    case "beta":
      return {
        title: "切换到 Beta 通道？",
        message: "Beta 会收到测试版、RC 候选版和正式版中最新的一个。测试版功能可能还在调整，稳定性低于正式版。" +
          autoInstallNote + "切回 Release 不会自动降级。",
        confirmLabel: "切换到 Beta",
        danger: true
      };
    default:
      return {
        title: "切回 Release 正式版通道？",
        message: "之后只接收正式版。切换不会自动降级：当前如果运行的是 Beta 或 Canary，会一直等到比它更高的正式版发布；需要立刻退回时请使用版本历史里的回退。",
        confirmLabel: "切回 Release",
        danger: false
      };
  }
}
