import type { PluginState } from "./api";

export function pluginForBot(state: PluginState, profile: string): PluginState {
  if (!profile || state.manifest.id === "official.open-api") return state;
  return { ...state, enabled: state.profile_enabled?.[profile] ?? state.enabled };
}
