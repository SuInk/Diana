package assistant

import (
	"context"
	"maps"
	"os"
	"strings"
)

// This reserved entry is created only by Runtime, never accepted as a plugin
// setting. It carries the profile through existing group-aware dispatch APIs.
const pluginSettingsProfileKey = "__diana_runtime_profile__"

func clonePluginValues(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		switch v := value.(type) {
		case map[string]any:
			out[key] = clonePluginValues(v)
		case []string:
			out[key] = append([]string(nil), v...)
		case []any:
			copyValues := make([]any, len(v))
			for i, item := range v {
				copyValues[i] = clonePluginValues(map[string]any{"value": item})["value"]
			}
			out[key] = copyValues
		default:
			out[key] = value
		}
	}
	return out
}

func cloneProfileSettings(profiles map[string]map[string]any) map[string]map[string]any {
	if profiles == nil {
		return nil
	}
	out := make(map[string]map[string]any, len(profiles))
	for id, settings := range profiles {
		out[id] = clonePluginValues(settings)
	}
	return out
}

func scopedPluginSettings(state PluginState, overrides PluginSettingOverrides) SettingValues {
	profile, _ := overrides[pluginSettingsProfileKey]["profile_id"].(string)
	state = state.ForProfile(profile)
	return effectivePluginSettingsForGroup(state.Manifest.Settings, state.Settings, overrides[state.Manifest.ID])
}

func (m *PluginManager) PluginWithSettingsForProfile(id, profileID string) (Plugin, SettingValues, bool) {
	return m.PluginWithSettingsForGroup(id, m.ProfileOverrides(profileID), PluginSettingOverrides{pluginSettingsProfileKey: {"profile_id": profileID}})
}

// MigrateProfileConfigurations consumes legacy shared settings once. Call with
// the complete saved profile set before constructing the runtime at startup.
func (m *PluginManager) MigrateProfileConfigurations(profiles []BotConfig) bool {
	if m == nil {
		return false
	}
	var ids []string
	for _, profile := range profiles {
		if id := strings.TrimSpace(profile.ID); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 && len(profiles) > 0 {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := false
	for id, state := range m.states {
		if id == OpenAPIPluginID || state.ProfileConfigMigrated {
			continue
		}
		state.ProfileSettings = cloneProfileSettings(state.ProfileSettings)
		if state.ProfileSettings == nil {
			state.ProfileSettings = map[string]map[string]any{}
		}
		legacy := state.Settings
		if id == resolverPluginID {
			legacy = legacyResolverProfileSettings(legacy)
		}
		state.ProfileEnabled = maps.Clone(state.ProfileEnabled)
		if state.ProfileEnabled == nil {
			state.ProfileEnabled = map[string]bool{}
		}
		for _, id := range ids {
			if _, ok := state.ProfileSettings[id]; !ok {
				state.ProfileSettings[id] = clonePluginValues(legacy)
			}
			if _, ok := state.ProfileEnabled[id]; !ok {
				state.ProfileEnabled[id] = state.Enabled
			}
		}
		state.Settings = nil
		state.Enabled = !state.Manifest.DefaultDisabled
		state.ProfileConfigMigrated = true
		m.states[id] = state
		changed = true
	}
	return changed
}

func legacyResolverProfileSettings(settings map[string]any) map[string]any {
	out := clonePluginValues(settings)
	if out == nil {
		out = map[string]any{}
	}
	ctx := context.Background()
	for key, value := range map[string]string{
		resolverSettingBiliSessdata: bilibiliSessdata(ctx), resolverSettingDouyinCookie: resolverDouyinCookie(ctx), resolverSettingXHSCookie: resolverXHSCookie(ctx),
		resolverSettingYTDLPCookies: firstNonEmpty(resolverYTDLPCookies(ctx), defaultYTDLPCookiesPath()), resolverSettingCookiesBrowser: os.Getenv("DIANA_YTDLP_COOKIES_FROM_BROWSER"), resolverSettingProxyURL: resolverProxyURL(ctx),
	} {
		current, _ := out[key].(string)
		if strings.TrimSpace(current) == "" && strings.TrimSpace(value) != "" {
			out[key] = strings.TrimSpace(value)
		}
	}
	return out
}
