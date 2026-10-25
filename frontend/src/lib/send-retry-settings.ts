// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

/** 仅整轮重跑上限仍可配置。旧发送退避字段保留类型兼容，不再展示、校验或生成提交值。 */
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
    key: "inbound_retry_max_attempts",
    label: "整轮重跑上限（次）",
    min: 1,
    max: 20,
    fallback: 5,
    hint: "一条消息处理失败后退回队列重新生成回复，最多跑这么多轮；每轮都会调用模型。明确拒收（如对方已删好友）不重跑。"
  }
];

/** 表单里的空值、非整数统一成 0，后端按 0 补默认（机器人级）或跟随（分群级）。 */
export function sendRetryPayload(settings: SendRetrySettings): SendRetrySettings {
  const payload: SendRetrySettings = {};
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
  for (const field of sendRetryFields) {
    const raw = settings[field.key] as unknown;
    if (unset(raw)) continue;
    const value = Number(raw);
    if (!Number.isInteger(value) || value < field.min || value > field.max) {
      return `${field.label.replace(/（.*）$/, "")}请输入 ${field.min} 到 ${field.max} 之间的整数`;
    }
  }
  return "";
}
