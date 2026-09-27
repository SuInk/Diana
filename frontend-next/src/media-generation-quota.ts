// 生图次数上限。机器人页 0 表示不限；群配置用三态：不带字段跟随机器人、0 本群不限、
// 正数本群上限。数字框清空后 v-model.number 给的是空串，后端按整数解析会整份拒收。
export function dailyLimitValue(value: unknown): number {
  const parsed = Number(value);
  return value === "" || value == null || !Number.isFinite(parsed) ? 0 : Math.max(0, Math.round(parsed));
}

export interface ImageGenerationLimits {
  image_generation_daily_group_limit?: number | string | null;
  image_generation_daily_user_limit?: number | string;
  daily_limit_timezone?: string;
}

export function botImageGenerationLimitsPayload(current: ImageGenerationLimits): {
  image_generation_daily_group_limit: number;
  image_generation_daily_user_limit: number;
  daily_limit_timezone: string;
} {
  return {
    image_generation_daily_group_limit: dailyLimitValue(current.image_generation_daily_group_limit),
    image_generation_daily_user_limit: dailyLimitValue(current.image_generation_daily_user_limit),
    daily_limit_timezone: (current.daily_limit_timezone ?? "").trim()
  };
}

export type GroupImageLimitMode = "" | "unlimited" | "custom";

// 选了「本群单独设置」但还没填数时字段是空串，照样算单独设置，输入框才不会消失。
export function groupImageLimitMode(value: unknown): GroupImageLimitMode {
  if (value === undefined || value === null) {
    return "";
  }
  return value === 0 ? "unlimited" : "custom";
}

export function setGroupImageLimitMode(config: ImageGenerationLimits, mode: string): void {
  if (mode === "unlimited") {
    config.image_generation_daily_group_limit = 0;
  } else if (mode === "custom") {
    const current = Number(config.image_generation_daily_group_limit);
    config.image_generation_daily_group_limit = Number.isFinite(current) && current >= 1 ? current : "";
  } else {
    config.image_generation_daily_group_limit = undefined;
  }
}

// 每人上限只在机器人上设：它跨群合计，群配置里不带。跟随机器人和没填数的单独设置
// 都不带这个字段。
export function groupImageGenerationLimitsPayload(current: ImageGenerationLimits): { image_generation_daily_group_limit?: number } {
  const value = current.image_generation_daily_group_limit;
  if (groupImageLimitMode(value) === "unlimited") {
    return { image_generation_daily_group_limit: 0 };
  }
  const parsed = Number(value);
  if (value === "" || value == null || !Number.isFinite(parsed) || parsed < 1) {
    return { image_generation_daily_group_limit: undefined };
  }
  return { image_generation_daily_group_limit: Math.round(parsed) };
}
