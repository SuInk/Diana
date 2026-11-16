// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

/**
 * 实时推送里定位一条事件用的字段。bot_event（一轮处理的结果）和 event_progress
 * （调用链又走了一步）都带这几项。
 */
export interface LiveEventRef {
  message_id?: string;
  kind?: string;
  group_id?: string;
  user_id?: string;
  profile_id?: string;
}

/** 列表里的事件和推送说的是不是同一条：消息号、类型、会话都对上；机器人有一边没写就不比。 */
export function isSameLiveEvent(event: LiveEventRef, ref: LiveEventRef): boolean {
  const messageID = ref.message_id?.trim();
  if (!messageID || event.message_id !== messageID) return false;
  return (
    event.kind === ref.kind &&
    (event.group_id ?? "") === (ref.group_id ?? "") &&
    (event.user_id ?? "") === (ref.user_id ?? "") &&
    (!ref.profile_id || !event.profile_id || event.profile_id === ref.profile_id)
  );
}

/** 推送来源：result 是一轮处理出了结果（bot_event），progress 是调用链又走了一步（event_progress）。 */
export type LiveEventSource = "result" | "progress";

export interface LiveEventSyncOptions {
  /** 这条推送对应的事件当前列表里有没有。 */
  isListed(ref: LiveEventRef): boolean;
  /** 这条事件的调用链（或它某个子任务的调用链）是不是展开着。 */
  isExpanded(ref: LiveEventRef): boolean;
  /** 原地重拉列表里这一条，展开着的调用链一起重拉。 */
  refresh(ref: LiveEventRef): void;
  /** 只重拉这一条展开着的调用链。 */
  refreshTrace(ref: LiveEventRef): void;
  /** 结果推送说的事件列表里没有：去按当前筛选核实一下是不是真有新事件。 */
  checkForNew(): void;
  /** 合并同一条事件连续推送的等待时间。调用链一轮能写几十步，不能每步都重拉。 */
  delayMs: number;
  setTimer?: (callback: () => void, ms: number) => unknown;
  clearTimer?: (handle: unknown) => void;
}

/**
 * 把实时推送分开处理：
 *
 *   - 结果推送：列表里已有的事件原地刷新卡片，没有的才去核实新事件。
 *   - 调用链进度：只有展开着调用链的那条才重拉调用链，收起的卡片看不到步骤，不拉。
 *
 * 以前每来一条推送就亮「有新事件」，已显示事件的结果更新、别的机器人、根本进不了
 * 列表的记录都会把它点亮；而原地刷新只看最后一条推送、只在处理完那一刻刷，中途的
 * 步骤不跟着动。这里按消息号各自去抖，同一时刻来的几条也不会互相挤掉。
 */
export function createLiveEventSync(options: LiveEventSyncOptions): { push(ref: LiveEventRef, source: LiveEventSource): void; dispose(): void } {
  const setTimer = options.setTimer ?? ((callback, ms) => setTimeout(callback, ms));
  const clearTimer = options.clearTimer ?? ((handle) => clearTimeout(handle as ReturnType<typeof setTimeout>));
  const timers = new Map<string, unknown>();
  let checkTimer: unknown = null;

  function debounce(key: string, run: () => void): void {
    const previous = timers.get(key);
    if (previous !== undefined) clearTimer(previous);
    timers.set(key, setTimer(() => {
      timers.delete(key);
      run();
    }, options.delayMs));
  }

  function push(ref: LiveEventRef, source: LiveEventSource): void {
    const messageID = ref.message_id?.trim();
    // 没有消息号的记录（戳一戳、入群申请这类）不会写进事件列表。
    if (!messageID) return;
    const key = [messageID, ref.kind ?? "", ref.group_id ?? "", ref.user_id ?? "", ref.profile_id ?? ""].join("\u0000");
    if (source === "progress") {
      // 结果刷新已经在排队的话，它会连调用链一起拉。
      if (timers.has(`result\u0000${key}`) || !options.isExpanded(ref)) return;
      debounce(`progress\u0000${key}`, () => options.refreshTrace(ref));
      return;
    }
    if (options.isListed(ref)) {
      const pendingTrace = timers.get(`progress\u0000${key}`);
      if (pendingTrace !== undefined) {
        clearTimer(pendingTrace);
        timers.delete(`progress\u0000${key}`);
      }
      debounce(`result\u0000${key}`, () => options.refresh(ref));
      return;
    }
    if (checkTimer !== null) return;
    checkTimer = setTimer(() => {
      checkTimer = null;
      options.checkForNew();
    }, options.delayMs);
  }

  function dispose(): void {
    for (const handle of timers.values()) clearTimer(handle);
    timers.clear();
    if (checkTimer !== null) clearTimer(checkTimer);
    checkTimer = null;
  }

  return { push, dispose };
}

/**
 * 当前筛选下最新的一条不在列表里，才算真有新事件。列表按时间倒序，最新那条已经在
 * 列表里，就说明推送说的事件不归当前筛选管（别的群、别的机器人、被结果或搜索筛掉）。
 */
export function hasUnlistedNewest(newest: { id: string } | undefined, listedIDs: Iterable<string>): boolean {
  if (!newest) return false;
  for (const id of listedIDs) {
    if (id === newest.id) return false;
  }
  return true;
}
