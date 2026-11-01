// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import type { RoomState } from "../api";
export type RoomPose = RoomState["pose"];

interface RoomChannel { profile_id?: string; connected: boolean }
export interface RoomActivityInput {
  profile: string;
  enabled: boolean;
  connected: boolean;
  initial?: RoomState;
  status: {
    running: boolean;
    channel: RoomChannel;
    channels?: readonly RoomChannel[];
    rooms?: readonly RoomState[];
  } | null;
  now: number;
}
export interface RoomActivity {
  pose: RoomPose;
  label: string;
  detail: string;
  source: "runtime" | "agent" | "idle" | "unavailable";
}

export function selectRoomState(profile: string, initial?: RoomState, live?: RoomState): RoomState | undefined {
  if (initial?.profile_id !== profile) initial = undefined;
  if (live?.profile_id !== profile) live = undefined;
  if (!live) return initial;
  if (!initial || initial.epoch !== live.epoch) return live;
  return initial.revision > live.revision ? initial : live;
}

const phaseLabels: Record<RoomState["phase"], string> = {
  idle: "房间待机", thinking: "正在生成回复", tool: "正在调用工具", completed: "回复生成已结束",
  cancelled: "回复任务已取消", error: "回复生成遇到问题", expired: "任务状态已过期", disabled: "小窝未开启"
};
const poseLabels: Record<RoomPose, string> = { rest: "床边休息", phone: "看手机", desk: "去书桌前" };

/** Only authoritative room snapshots drive automatic mode; never infer work from chat text or message age. */
export function roomActivity(input: RoomActivityInput): RoomActivity {
  const unavailable = (label: string, detail: string): RoomActivity => ({ pose: "rest", label, detail, source: "unavailable" });
  if (!input.profile) return unavailable("选一位房间主人", "在侧栏选择一个机器人，即可查看它的小窝。");
  if (!input.enabled) return unavailable("小窝未开启", "房间暂未启用，已有账本不受影响。");
  if (!input.connected || !input.status) return unavailable("等待状态同步", "连接断开或尚未收到快照，不推测真实任务进度。");
  if (!input.status.running) return unavailable("机器人服务未运行", "停止展示运行中的任务，等待服务恢复。");
  const channel = (input.status.channels ?? [input.status.channel]).find(item => item.profile_id === input.profile);
  if (!channel) return unavailable("暂无该机器人的状态", "不会使用其他机器人的活动代替。");
  const state = selectRoomState(input.profile, input.initial, input.status.rooms?.find(item => item.profile_id === input.profile));
  if (!state) return unavailable("等待房间快照", "服务端尚未提供房间协议；不会回退到根据消息猜姿势。");
  if (!state.enabled) return unavailable("小窝未开启", "服务端已关闭小窝，已有账本不受影响。");
  if (!Object.prototype.hasOwnProperty.call(poseLabels, state.pose) || !Object.prototype.hasOwnProperty.call(phaseLabels, state.phase)) return unavailable("房间状态不兼容", "请确认前后端版本一致。");
  if (state.active_tasks > 0 && (!state.task_id || !state.expires_at || !Number.isFinite(Date.parse(state.expires_at)) || Date.parse(state.expires_at) <= input.now)) {
    return unavailable("任务状态已过期", "等待服务端重新同步；过期任务不会继续显示为执行中。");
  }
  const activity = state.source === "agent" ? `Agent 选择：${poseLabels[state.pose]}` : phaseLabels[state.phase];
  const execution = state.active_tasks > 0 ? `当前 ${state.active_tasks} 个回复任务；${phaseLabels[state.phase]}。` : `${phaseLabels[state.phase]}。`;
  return {
    pose: state.pose, label: activity, source: state.source,
    detail: `${execution}${channel.connected ? "" : "消息通道未连接。"}房间动作不执行真实电脑操作；回复生成结束不等于消息已送达。`
  };
}
