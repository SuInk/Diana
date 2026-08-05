import { reactive } from "vue";
import { getQQBotConfig, type QQBotConfig } from "./api";

/**
 * 机器人实例在侧栏里作为「机器人」的子菜单出现，选择发生在导航里而不是
 * 一个只用来点一下的列表页。App.vue 渲染子项、AssistantView 响应选中，
 * 两边共享这一份状态，避免各自拉一次配置后不同步。
 */
export const bots = reactive({
  profiles: [] as QQBotConfig[],
  activeID: "" as string,
  loaded: false
});

/** 用一份已经取到的配置更新 store，省掉重复请求。 */
export function applyBotProfiles(config: QQBotConfig): void {
  bots.profiles = config.profiles ?? [];
  bots.activeID = config.active_profile_id ?? bots.profiles[0]?.id ?? "";
  bots.loaded = true;
}

export async function refreshBotProfiles(): Promise<void> {
  try {
    applyBotProfiles(await getQQBotConfig());
  } catch {
    // 侧栏子菜单是辅助导航，取不到时退化成只有「机器人」一项。
    bots.loaded = true;
  }
}
