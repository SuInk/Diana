// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 聊天页：主人在 WebUI 里和机器人私聊。和语音通话共用「主人私聊」这条回复流程，
// 人设、记忆、历史和主人在平台上的私聊是同一份。

export interface WebChatMessage {
  id: string;
  role: "user" | "assistant";
  /** "call" 是一条通话记录（「[语音通话] 通话 3 分 12 秒」），显示成居中的提示。 */
  kind?: "call";
  text: string;
  images?: string[];
  /** 这条回的那一条；原消息不在记录里时只有 message_id。 */
  quote?: WebChatQuote;
  /** 通话里说的话带着那通电话的 ID；kind 为 "call" 时是那通电话的记录。 */
  call_id?: string;
  /** Unix 秒。 */
  at: number;
  /** 本地刚发出、还没被服务端确认的那句。 */
  pending?: boolean;
  failed?: boolean;
}

export interface WebChatQuote {
  message_id?: string;
  user_id?: string;
  sender_name?: string;
  text?: string;
}

/** 还没挂断的那通电话。 */
export interface WebChatCall {
  id: string;
  /** Unix 秒。 */
  started_at: number;
}

export interface WebChatInfo {
  bot_id: string;
  bot_name: string;
  platform: string;
  problem?: string;
  owner_avatar_url?: string;
  active_call?: WebChatCall;
}

export type WebChatFrame =
  | { type: "message"; message: WebChatMessage }
  | { type: "heartbeat" }
  | { type: "done"; error?: string; replies?: number };

async function chatRequest(init?: RequestInit, query = ""): Promise<Response> {
  const response = await fetch(`/api/assistant/chat${query}`, {
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

export async function getWebChat(profile: string, signal?: AbortSignal): Promise<{ info: WebChatInfo; messages: WebChatMessage[] }> {
  return (await chatRequest({ signal }, `?profile=${encodeURIComponent(profile)}`)).json();
}

/** 发一句话，回复按条推给 receive；连接中途断了或服务端报错都抛出。 */
export async function sendWebChat(profile: string, text: string, signal: AbortSignal, receive: (message: WebChatMessage) => void): Promise<void> {
  const response = await chatRequest({ method: "POST", body: JSON.stringify({ profile, text }), signal });
  if (!response.body) throw new Error("浏览器不支持流式回复");
  await readWebChatFrames(response.body, (frame) => {
    if (frame.type === "message") receive(frame.message);
  });
}

// HTTP 分块可能把一行 JSON 或一个多字节字符切开；没收到 done 就结束算中断。
export async function readWebChatFrames(body: ReadableStream<Uint8Array>, receive: (frame: WebChatFrame) => void): Promise<void> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let pending = "";
  let done = false;
  let failure = "";
  const consume = (line: string) => {
    if (!line.trim()) return;
    const frame = JSON.parse(line) as WebChatFrame;
    if (frame.type === "done") {
      done = true;
      failure = frame.error || "";
    }
    receive(frame);
  };
  try {
    while (true) {
      const chunk = await reader.read();
      pending += decoder.decode(chunk.value, { stream: !chunk.done });
      let newline: number;
      while ((newline = pending.indexOf("\n")) >= 0) {
        consume(pending.slice(0, newline));
        pending = pending.slice(newline + 1);
      }
      if (chunk.done) break;
    }
    consume(pending);
  } finally {
    if (!done) await reader.cancel().catch(() => {});
    reader.releaseLock();
  }
  if (!done) throw new Error("连接中断了，刷新看看她回了没有");
  if (failure) throw new Error(failure);
}

/** 两条消息隔了多久要插一条时间分隔，和 QQ 一样五分钟。 */
export const TIME_DIVIDER_GAP = 5 * 60;

/** 聊天里的时间标签：今天只写时分，昨天写「昨天」，更早写月日。 */
export function chatTimeLabel(at: number, now = new Date()): string {
  const date = new Date(at * 1000);
  const hm = `${String(date.getHours()).padStart(2, "0")}:${String(date.getMinutes()).padStart(2, "0")}`;
  const day = (value: Date) => new Date(value.getFullYear(), value.getMonth(), value.getDate()).getTime();
  const diff = Math.round((day(now) - day(date)) / 86400000);
  if (diff === 0) return hm;
  if (diff === 1) return `昨天 ${hm}`;
  if (date.getFullYear() === now.getFullYear()) return `${date.getMonth() + 1}月${date.getDate()}日 ${hm}`;
  return `${date.getFullYear()}年${date.getMonth() + 1}月${date.getDate()}日 ${hm}`;
}

/** 哪些消息前面要插时间分隔：第一条和隔了五分钟以上的。 */
export function timeDividers(messages: Pick<WebChatMessage, "at">[]): boolean[] {
  return messages.map((message, i) => i === 0 || message.at - messages[i - 1].at >= TIME_DIVIDER_GAP);
}

/** 聊天里的一项：普通消息，或者一条通话记录连同那通电话里说的话。 */
export interface WebChatEntry {
  message: WebChatMessage;
  turns?: WebChatMessage[];
}

/**
 * 把通话里说的话从私聊里拿出来：有通话记录的归到记录下面，正在通话的那通单独放在
 * live 里。对不上记录的（旧版本记的、记录还没写进来）照常当普通消息显示，不丢。
 */
export function groupCallTurns(messages: WebChatMessage[], activeCallID = ""): { entries: WebChatEntry[]; live: WebChatMessage[] } {
  const recorded = new Set(messages.filter((m) => m.kind === "call" && m.call_id).map((m) => m.call_id));
  const turns = new Map<string, WebChatMessage[]>();
  const entries: WebChatEntry[] = [];
  const live: WebChatMessage[] = [];
  for (const message of messages) {
    const callID = message.kind === "call" ? "" : message.call_id || "";
    if (callID && recorded.has(callID)) {
      turns.set(callID, [...(turns.get(callID) ?? []), message]);
    } else if (callID && callID === activeCallID) {
      live.push(message);
    } else {
      entries.push({ message });
    }
  }
  for (const entry of entries) {
    if (entry.message.kind === "call" && entry.message.call_id) entry.turns = turns.get(entry.message.call_id) ?? [];
  }
  return { entries, live };
}

/** 通话时长「03:12」，过一小时写成「1:03:12」。 */
export function callClock(seconds: number): string {
  const total = Math.max(0, Math.floor(seconds));
  const pad = (value: number) => String(value).padStart(2, "0");
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const rest = `${pad(m)}:${pad(total % 60)}`;
  return h ? `${h}:${rest}` : rest;
}

/** 悬停时显示的完整时间。 */
export function fullTimeLabel(at: number): string {
  const date = new Date(at * 1000);
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}

/**
 * 后台刷新拿到的新记录合进页面：以服务端为准，本地还没确认或没发出去的那几句接在后面，
 * 免得刷新一下就把失败待重发的消息冲掉。
 */
export function mergeWebChatHistory(server: WebChatMessage[], current: WebChatMessage[]): WebChatMessage[] {
  const local = current.filter((m) => m.pending || m.failed);
  return local.length ? [...server, ...local] : server;
}
