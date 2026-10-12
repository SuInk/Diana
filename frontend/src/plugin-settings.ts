import type { PluginState } from "./api";

export function pluginForBot(state: PluginState, profile: string): PluginState {
  if (!profile) return state;
  return { ...state, enabled: state.profile_enabled?.[profile] ?? state.enabled };
}
