// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 本地群：主人在聊天页里建的群，成员是几台机器人，只存在 Diana 里。主人一发言，
// 成员都回；成员之间还会接话，连续到刹车条数就停下等主人。

import { readWebChatFrames } from "./web-chat";
import type { CallInfo } from "./voice-call";

export interface LocalRoom {
  id: string;
  name: string;
  members: string[];
  /** 主人没插话时机器人最多连续发言几条，0 按默认 8。 */
  max_bot_turns: number;
  created_at: number;
}

export interface LocalRoomMessage {
  id: string;
  /** "owner" 或成员机器人 ID。 */
  sender_id: string;
  sender_name: string;
  text: string;
  images?: string[];
  /** Unix 秒。 */
  at: number;
  /** "call" 是群通话记录（打了多久），显示成居中的提示。 */
  kind?: string;
  /** 本地刚发出、还没被服务端确认的那句。 */
  pending?: boolean;
  failed?: boolean;
}

export interface LocalRoomStatus {
  /** 正在想回复的成员数。 */
  thinking: number;
  bot_streak: number;
  /** 机器人之间不再接话，等主人说话：到了刹车条数或被停止。 */
  paused: boolean;
}

export const LOCAL_ROOM_OWNER = "owner";
export const LOCAL_ROOM_DEFAULT_MAX_BOT_TURNS = 8;

async function roomRequest(path: string, init?: RequestInit): Promise<Response> {
  const response = await fetch(`/api/assistant/rooms${path}`, {
    ...init,
    headers: { "Content-Type": "application/json", ...init?.headers },
    credentials: "same-origin",
    cache: "no-store"
  });
  if (!response.ok) {
    const body = await response.json().catch(() => ({}));
    throw new Error(body.error || `请求失败：${response.status}`);
  }
  return response;
}

export async function listLocalRooms(): Promise<LocalRoom[]> {
  return (await (await roomRequest("")).json()).rooms ?? [];
}

export async function createLocalRoom(room: Pick<LocalRoom, "name" | "members" | "max_bot_turns">): Promise<LocalRoom> {
  return (await (await roomRequest("", { method: "POST", body: JSON.stringify(room) })).json()).room;
}

export async function updateLocalRoom(room: Pick<LocalRoom, "id" | "name" | "members" | "max_bot_turns">): Promise<LocalRoom> {
  return (await (await roomRequest(`/${encodeURIComponent(room.id)}`, { method: "PUT", body: JSON.stringify(room) })).json()).room;
}

export async function deleteLocalRoom(id: string): Promise<void> {
  await roomRequest(`/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export async function getLocalRoom(id: string, signal?: AbortSignal): Promise<{ room: LocalRoom; messages: LocalRoomMessage[]; status: LocalRoomStatus }> {
  return (await roomRequest(`/${encodeURIComponent(id)}`, { signal })).json();
}

export async function stopLocalRoom(id: string): Promise<LocalRoomStatus> {
  return (await (await roomRequest(`/${encodeURIComponent(id)}/stop`, { method: "POST" })).json()).status;
}

/** 一句话建群：模型拆出群名和几台带人设的内置机器人，建好机器人再建群。 */
export async function planLocalRoom(text: string, template = ""): Promise<LocalRoom> {
  return (await (await roomRequest("/plan", { method: "POST", body: JSON.stringify({ text, template }) })).json()).room;
}

/** 主人说一句，这一轮讨论里成员的每条发言推给 receive；讨论停下时返回状态。 */
export async function sendLocalRoom(id: string, text: string, signal: AbortSignal, receive: (message: LocalRoomMessage) => void): Promise<LocalRoomStatus | undefined> {
  const response = await roomRequest(`/${encodeURIComponent(id)}/messages`, { method: "POST", body: JSON.stringify({ text }), signal });
  if (!response.body) throw new Error("浏览器不支持流式回复");
  let status: LocalRoomStatus | undefined;
  await readWebChatFrames(response.body, (frame) => {
    const value = frame as unknown as { type: string; message?: LocalRoomMessage; status?: LocalRoomStatus };
    if (value.type === "message" && value.message) receive(value.message);
    if (value.type === "done") status = value.status;
  });
  return status;
}

/** 这个群现在能不能打群电话；bot_name 是群名。 */
export async function getRoomCallInfo(id: string): Promise<CallInfo> {
  return (await roomRequest(`/${encodeURIComponent(id)}/call`)).json();
}
