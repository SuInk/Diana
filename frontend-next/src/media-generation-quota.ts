// 生图、视频的每日次数上限。机器人页 0 表示不限；群配置用三态：不带字段跟随机器人、
// 0 本群不限、正数本群上限。数字框清空后 v-model.number 给的是空串，后端按整数解析会整份拒收。
export function dailyLimitValue(value: unknown): number {
  const parsed = Number(value);
  return value === "" || value == null || !Number.isFinite(parsed) ? 0 : Math.max(0, Math.round(parsed));
}

export type GroupDailyLimitField = "image_generation_daily_group_limit" | "video_generation_daily_group_limit";

export interface MediaGenerationLimits {
  image_generation_daily_group_limit?: number | string | null;
  image_generation_daily_user_limit?: number | string;
  video_generation_daily_group_limit?: number | string | null;
  video_generation_daily_user_limit?: number | string;
  daily_limit_timezone?: string;
}

export function botImageGenerationLimitsPayload(current: MediaGenerationLimits): {
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

export function botVideoGenerationLimitsPayload(current: MediaGenerationLimits): {
  video_generation_daily_group_limit: number;
  video_generation_daily_user_limit: number;
} {
  return {
    video_generation_daily_group_limit: dailyLimitValue(current.video_generation_daily_group_limit),
    video_generation_daily_user_limit: dailyLimitValue(current.video_generation_daily_user_limit)
  };
}

export type GroupDailyLimitMode = "" | "unlimited" | "custom";

// 选了「本群单独设置」但还没填数时字段是空串，照样算单独设置，输入框才不会消失。
export function groupDailyLimitMode(value: unknown): GroupDailyLimitMode {
  if (value === undefined || value === null) {
    return "";
  }
  return value === 0 ? "unlimited" : "custom";
}

export const groupImageLimitMode = groupDailyLimitMode;

export function setGroupDailyLimitMode(config: MediaGenerationLimits, field: GroupDailyLimitField, mode: string): void {
  if (mode === "unlimited") {
    config[field] = 0;
  } else if (mode === "custom") {
    const current = Number(config[field]);
    config[field] = Number.isFinite(current) && current >= 1 ? current : "";
  } else {
    config[field] = undefined;
  }
}

export function setGroupImageLimitMode(config: MediaGenerationLimits, mode: string): void {
  setGroupDailyLimitMode(config, "image_generation_daily_group_limit", mode);
}

// 跟随机器人和没填数的单独设置都不带这个字段，不能存成 0（那是「不限」）。
function groupDailyLimitValue(value: unknown): number | undefined {
  if (groupDailyLimitMode(value) === "unlimited") {
    return 0;
  }
  const parsed = Number(value);
  if (value === "" || value == null || !Number.isFinite(parsed) || parsed < 1) {
    return undefined;
  }
  return Math.round(parsed);
}

// 每人上限只在机器人上设：它跨群合计，群配置里不带。
export function groupImageGenerationLimitsPayload(current: MediaGenerationLimits): { image_generation_daily_group_limit?: number } {
  return { image_generation_daily_group_limit: groupDailyLimitValue(current.image_generation_daily_group_limit) };
}

export function groupVideoGenerationLimitsPayload(current: MediaGenerationLimits): { video_generation_daily_group_limit?: number } {
  return { video_generation_daily_group_limit: groupDailyLimitValue(current.video_generation_daily_group_limit) };
}
