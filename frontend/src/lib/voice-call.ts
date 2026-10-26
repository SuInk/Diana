// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 实时语音通话的前端工具。协议见 docs/voice-call.md：上行 16 位 PCM 和 JSON 控制
// 消息，下行 JSON 事件和逐句的合成音频。这里只放和页面无关、能单测的部分。

/** 上行采样率。识别接口都按 16kHz 单声道处理，再高只是多传数据。 */
export const CALL_SAMPLE_RATE = 16000;

export type CallState = "listening" | "thinking" | "speaking";

export interface CallServerMessage {
  type: string;
  turn?: number;
  seq?: number;
  text?: string;
  state?: CallState;
  event?: "start" | "end" | "discarded";
  mime?: string;
  reason?: string;
  code?: string;
  fatal?: boolean;
  ready?: { call_id: string; bot_id: string; bot_name?: string; format: string; sample_rate?: number; segmentation: string; barge_in: boolean };
}

export interface CallInfo {
  bot_id: string;
  bot_name: string;
  platform: string;
  stt: boolean;
  tts: boolean;
  problem?: string;
}

/** 通话 WebSocket 地址。鉴权靠同源的登录 Cookie。 */
export function callSocketURL(profileID: string, location: Pick<Location, "protocol" | "host"> = window.location): string {
  const protocol = location.protocol === "https:" ? "wss:" : "ws:";
  const query = profileID ? `?profile=${encodeURIComponent(profileID)}` : "";
  return `${protocol}//${location.host}/api/assistant/call/ws${query}`;
}

/**
 * 把麦克风的采样率降到 16kHz。按区间取平均，顺带滤掉一点高频；手机上
 * AudioContext 多半是 48kHz，正好三取一。
 */
export function downsample(input: Float32Array, fromRate: number, toRate = CALL_SAMPLE_RATE): Float32Array {
  if (fromRate === toRate) return input;
  if (fromRate < toRate) throw new Error("不支持升采样");
  const ratio = fromRate / toRate;
  const length = Math.floor(input.length / ratio);
  const out = new Float32Array(length);
  for (let i = 0; i < length; i++) {
    const start = Math.floor(i * ratio);
    const end = Math.min(input.length, Math.floor((i + 1) * ratio));
    let sum = 0;
    for (let j = start; j < end; j++) sum += input[j];
    out[i] = end > start ? sum / (end - start) : 0;
  }
  return out;
}

/** Float32 [-1, 1] 转 16 位小端 PCM。 */
export function floatToPCM16(input: Float32Array): ArrayBuffer {
  const buffer = new ArrayBuffer(input.length * 2);
  const view = new DataView(buffer);
  for (let i = 0; i < input.length; i++) {
    const sample = Math.max(-1, Math.min(1, input[i]));
    view.setInt16(i * 2, sample < 0 ? sample * 0x8000 : sample * 0x7fff, true);
  }
  return buffer;
}

/** 一轮对话在页面上的样子：用户说的一句，机器人回的几句。 */
export interface CallTurnView {
  turn: number;
  user: string;
  bot: string[];
  status: "active" | "done" | "interrupted" | "merged" | "failed" | "empty";
}

const endStatus: Record<string, CallTurnView["status"]> = {
  done: "done",
  interrupted: "interrupted",
  merged: "merged",
  failed: "failed",
  no_speech: "empty",
  no_reply: "empty"
};

/**
 * 把下行事件折叠进对话记录。被并进下一句的那一轮（merged）不单独显示：它的文字
 * 会出现在下一轮的转写里。
 */
export function applyCallMessage(turns: CallTurnView[], msg: CallServerMessage): CallTurnView[] {
  if (!msg.turn) return turns;
  const index = turns.findIndex((item) => item.turn === msg.turn);
  const current: CallTurnView = index >= 0 ? turns[index] : { turn: msg.turn, user: "", bot: [], status: "active" };
  let next = current;
  switch (msg.type) {
    case "transcript":
      next = { ...current, user: msg.text ?? "" };
      break;
    case "reply":
      next = { ...current, bot: [...current.bot, msg.text ?? ""] };
      break;
    case "turn_end":
      next = { ...current, status: endStatus[msg.reason ?? ""] ?? "done" };
      break;
    default:
      return turns;
  }
  const out = index >= 0 ? turns.map((item, i) => (i === index ? next : item)) : [...turns, next];
  return out.filter((item) => item.status !== "merged" && !(item.status === "empty" && !item.user && !item.bot.length));
}

/**
 * 逐句的音频收集：audio_start 开始收，audio_end 交出整句。浏览器的
 * decodeAudioData 要完整的文件，所以一句收齐了再解码播放。
 */
export class SentenceAudioCollector {
  private parts: ArrayBuffer[] = [];
  private mime = "";
  private key = "";

  start(turn: number, seq: number, mime: string): void {
    this.parts = [];
    this.mime = mime;
    this.key = `${turn}.${seq}`;
  }

  push(chunk: ArrayBuffer): void {
    if (this.key) this.parts.push(chunk);
  }

  /** 交出收齐的一句；对不上号（被打断后的残片）返回 null。 */
  end(turn: number, seq: number): { mime: string; data: ArrayBuffer } | null {
    if (this.key !== `${turn}.${seq}`) return null;
    const total = this.parts.reduce((sum, part) => sum + part.byteLength, 0);
    const data = new Uint8Array(total);
    let offset = 0;
    for (const part of this.parts) {
      data.set(new Uint8Array(part), offset);
      offset += part.byteLength;
    }
    this.reset();
    return { mime: this.mime, data: data.buffer };
  }

  reset(): void {
    this.parts = [];
    this.key = "";
  }
}
