import { reactive, readonly } from "vue";
import type { QQBotStatus, StatsSnapshot } from "./api";

export type BotEvent = NonNullable<QQBotStatus["recent_events"]>[number];

interface StreamState {
  connected: boolean;
  status: QQBotStatus | null;
  stats: StatsSnapshot | null;
  events: BotEvent[];
  lastEventAt: string | null;
}

const MAX_LIVE_EVENTS = 60;

const state = reactive<StreamState>({
  connected: false,
  status: null,
  stats: null,
  events: [],
  lastEventAt: null
});

let source: EventSource | null = null;
let started = false;

function handleStatus(raw: string): void {
  try {
    state.status = JSON.parse(raw) as QQBotStatus;
  } catch {
    /* 忽略坏帧，等待下一次快照 */
  }
}

function handleStats(raw: string): void {
  try {
    state.stats = JSON.parse(raw) as StatsSnapshot;
  } catch {
    /* 忽略坏帧 */
  }
}

function handleBotEvent(raw: string): void {
  try {
    const event = JSON.parse(raw) as BotEvent;
    state.events = [event, ...state.events].slice(0, MAX_LIVE_EVENTS);
    state.lastEventAt = event.at;
  } catch {
    /* 忽略坏帧 */
  }
}

function connect(): void {
  if (source) {
    source.close();
  }
  source = new EventSource("/api/events");
  source.addEventListener("open", () => {
    state.connected = true;
  });
  source.addEventListener("error", () => {
    // EventSource 自带重连；标记断开让 UI 显示重连状态。
    state.connected = false;
  });
  source.addEventListener("status", (event) => handleStatus((event as MessageEvent<string>).data));
  source.addEventListener("stats", (event) => handleStats((event as MessageEvent<string>).data));
  source.addEventListener("bot_event", (event) => handleBotEvent((event as MessageEvent<string>).data));
}

/** 启动全局 SSE 连接；重复调用是幂等的。 */
export function startEventStream(): void {
  if (started) {
    return;
  }
  started = true;
  if (import.meta.env.VITE_DEMO_MODE === "true") {
    void import("./demo").then(({ demoEvents, demoStats, demoStatus }) => {
      state.connected = true;
      state.status = demoStatus;
      state.stats = demoStats;
      state.events = demoEvents;
      state.lastEventAt = demoEvents[0]?.at ?? null;
    });
    return;
  }
  connect();
  document.addEventListener("visibilitychange", () => {
    // 后台标签页恢复后，若连接已断开则立即重建，避免等待浏览器重连间隔。
    if (document.visibilityState === "visible" && source && source.readyState === EventSource.CLOSED) {
      connect();
    }
  });
}

/** 只读的实时流状态，供各视图共享。 */
export const stream = readonly(state);

/** 手动把外部拉取的快照合并进流状态（如按钮触发的刷新）。 */
export function pushStatusSnapshot(status: QQBotStatus): void {
  state.status = status;
}

export function pushStatsSnapshot(stats: StatsSnapshot): void {
  state.stats = stats;
}
