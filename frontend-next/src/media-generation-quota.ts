// 生图次数上限：0 表示不限（机器人页）或跟随机器人（群配置）。
// 数字框清空后 v-model.number 给的是空串，后端按整数解析会整份拒收，统一转成非负整数。
export function dailyLimitValue(value: unknown): number {
  const parsed = Number(value);
  return value === "" || value == null || !Number.isFinite(parsed) ? 0 : Math.max(0, Math.round(parsed));
}

export interface ImageGenerationLimits {
  image_generation_daily_group_limit?: number | string;
  image_generation_daily_user_limit?: number | string;
}

export function botImageGenerationLimitsPayload(current: ImageGenerationLimits): { image_generation_daily_group_limit: number; image_generation_daily_user_limit: number } {
  return {
    image_generation_daily_group_limit: dailyLimitValue(current.image_generation_daily_group_limit),
    image_generation_daily_user_limit: dailyLimitValue(current.image_generation_daily_user_limit)
  };
}

// 每人上限只在机器人上设：它跨群合计，群配置里不带。
export function groupImageGenerationLimitsPayload(current: ImageGenerationLimits): { image_generation_daily_group_limit: number } {
  return { image_generation_daily_group_limit: dailyLimitValue(current.image_generation_daily_group_limit) };
}
