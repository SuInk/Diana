// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

/**
 * 发送失败后的两层慢重试参数：群退避闸门（同一份内容反复重发）和入站队列重跑
 * （整条消息重新生成回复再发）。机器人级和分群级是同一组键；分群留空跟随机器人。
 * 默认值和范围与后端 model/assistant/send_retry_policy.go 保持一致。
 */
export type SendRetryKey =
  | "send_backoff_initial_seconds"
  | "send_backoff_max_seconds"
  | "send_failure_window_minutes"
  | "send_drop_cooldown_minutes"
  | "inbound_retry_max_attempts";

export type SendRetrySettings = Partial<Record<SendRetryKey, number>>;

export interface SendRetryField {
  key: SendRetryKey;
  label: string;
  min: number;
  max: number;
  fallback: number;
  hint: string;
}

export const sendRetryFields: readonly SendRetryField[] = [
  {
    key: "send_backoff_initial_seconds",
    label: "群发送失败首次重发间隔（秒）",
    min: 5,
    max: 3600,
    fallback: 60,
    hint: "之后每次翻倍，直到下面的最长间隔。"
  },
  {
    key: "send_backoff_max_seconds",
    label: "群发送重发最长间隔（秒）",
    min: 5,
    max: 3600,
    fallback: 900,
    hint: "翻倍到这个值就不再变长。"
  },
  {
    key: "send_failure_window_minutes",
    label: "群发送失败放弃时限（分钟）",
    min: 1,
    max: 1440,
    fallback: 30,
    hint: "从第一次失败算起，超过这个时长仍没发出去就丢弃这条。通道离线不算，恢复后会补发。"
  },
  {
    key: "send_drop_cooldown_minutes",
    label: "放弃后本群冷却（分钟）",
    min: 1,
    max: 1440,
    fallback: 30,
    hint: "丢弃一条之后，这个群在冷却期内的新回复直接放弃，不再排队。"
  },
  {
    key: "inbound_retry_max_attempts",
    label: "整轮重跑上限（次）",
    min: 1,
    max: 20,
    fallback: 5,
    hint: "一条消息处理失败后退回队列重新生成回复，最多跑这么多轮；每轮都会调用模型。明确拒收（如对方已删好友）不重跑。"
  }
];

/** 表单里的空值、非整数统一成 0，后端按 0 补默认（机器人级）或跟随（分群级）。 */
export function sendRetryPayload(settings: SendRetrySettings): Record<SendRetryKey, number> {
  const payload = {} as Record<SendRetryKey, number>;
  for (const field of sendRetryFields) {
    const value = Number(settings[field.key]);
    payload[field.key] = Number.isInteger(value) && value > 0 ? value : 0;
  }
  return payload;
}

/** 0 和留空一样表示「没填」：分群跟随机器人，机器人级按默认值。 */
function unset(raw: unknown): boolean {
  return raw === undefined || raw === null || `${raw}`.trim() === "" || Number(raw) === 0;
}

/** 编辑前把 0 清成留空，输入框才显示「跟随机器人」的占位符。 */
export function withUnsetSendRetryCleared<T extends SendRetrySettings>(settings: T): T {
  for (const field of sendRetryFields) {
    if (unset(settings[field.key])) delete settings[field.key];
  }
  return settings;
}

/** 返回第一条越界提示；全部合法返回空串。留空和 0 不算越界。 */
export function sendRetryValidationError(settings: SendRetrySettings): string {
  const payload = sendRetryPayload(settings);
  for (const field of sendRetryFields) {
    const raw = settings[field.key] as unknown;
    if (unset(raw)) continue;
    const value = Number(raw);
    if (!Number.isInteger(value) || value < field.min || value > field.max) {
      return `${field.label.replace(/（.*）$/, "")}请输入 ${field.min} 到 ${field.max} 之间的整数`;
    }
  }
  const initial = payload.send_backoff_initial_seconds;
  const maximum = payload.send_backoff_max_seconds;
  if (initial > 0 && maximum > 0 && maximum < initial) {
    return "最长间隔不能比首次间隔短";
  }
  return "";
}
