// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/llm"
)

func voiceMigrationStores(t *testing.T, bots ...assistant.BotConfig) (*MemoryBotProfileStore, *MemoryLLMProfileStore) {
	t.Helper()
	providers := NewMemoryLLMProfileStore(llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "sk-chat-000", Model: "chat"})
	return NewMemoryBotProfileStoreFromSet(assistant.ProfileSet{Profiles: bots}), providers
}

func speechProviders(set llm.ProfileSet, group string) []llm.Profile {
	var out []llm.Profile
	for _, profile := range set.Profiles {
		if llm.NormalizeProfileGroup(profile.Group) == group {
			out = append(out, profile)
		}
	}
	return out
}

func TestMigrateVoicePluginsMovesGPTSoVITSIntoSpeechSlot(t *testing.T) {
	t.Setenv("DIANA_TTS_ENDPOINT", "")
	t.Setenv("DIANA_TTS_REF_AUDIO_PATH", "")
	bots, providers := voiceMigrationStores(t, assistant.BotConfig{ID: "a", Name: "A"}, assistant.BotConfig{ID: "b", Name: "B"}, assistant.BotConfig{ID: "off", Name: "Off"})
	states := map[string]assistant.PersistedPluginState{
		legacyVoiceTTSPluginID: {
			ProfileEnabled: map[string]bool{"off": false},
			Settings: map[string]any{
				"preset": "local", "ref_audio_path": "/ref/nana.wav", "prompt_text": "你好呀",
				"speed_factor": 1.1, "max_chars": float64(120), "send_mode": "always", "timeout_seconds": float64(60),
			},
		},
	}
	if err := MigrateVoicePlugins(states, bots, providers); err != nil {
		t.Fatal(err)
	}
	created := speechProviders(providers.Profiles(), llm.GroupTTS)
	if len(created) != 1 || created[0].Config.BaseURL != "http://127.0.0.1:9880" || created[0].Config.APIKey != "" {
		t.Fatalf("providers = %#v", created)
	}
	for _, bot := range bots.Profiles().Profiles {
		role, ok := bot.ModelRoles["tts"]
		if bot.ID == "off" {
			if ok {
				t.Fatalf("disabled bot got a tts role: %#v", role)
			}
			continue
		}
		want := map[string]string{"api": "gpt_sovits", "ref_audio_path": "/ref/nana.wav", "prompt_text": "你好呀", "speed": "1.1", "max_chars": "120", "send_mode": "always", "timeout_seconds": "60"}
		if role.ProfileID != created[0].ID || role.Model != "gpt-sovits" {
			t.Fatalf("bot %s role = %#v", bot.ID, role)
		}
		for key, value := range want {
			if role.Params[key] != value {
				t.Fatalf("bot %s param %s = %q, want %q (%#v)", bot.ID, key, role.Params[key], value, role.Params)
			}
		}
	}
	// 再跑一遍不重复建提供商，也不改已经迁好的绑定。
	if err := MigrateVoicePlugins(states, bots, providers); err != nil {
		t.Fatal(err)
	}
	if again := speechProviders(providers.Profiles(), llm.GroupTTS); len(again) != 1 {
		t.Fatalf("migration is not idempotent: %#v", again)
	}
}

func TestMigrateVoicePluginsSkipsUnconfiguredDefaultTTS(t *testing.T) {
	t.Setenv("DIANA_TTS_ENDPOINT", "")
	t.Setenv("DIANA_TTS_REF_AUDIO_PATH", "")
	bots, providers := voiceMigrationStores(t, assistant.BotConfig{ID: "a", Name: "A"})
	// 内置插件默认开着、预设是本机，但大多数人根本没装 GPT-SoVITS。
	states := map[string]assistant.PersistedPluginState{legacyVoiceTTSPluginID: {Settings: map[string]any{"send_mode": "on_demand"}}}
	if err := MigrateVoicePlugins(states, bots, providers); err != nil {
		t.Fatal(err)
	}
	if roles := bots.Profiles().Profiles[0].ModelRoles; len(roles) != 0 || len(speechProviders(providers.Profiles(), llm.GroupTTS)) != 0 {
		t.Fatalf("roles=%#v providers=%#v", roles, providers.Profiles())
	}
}

func TestMigrateVoicePluginsKeepsExistingSlotAndAddsSendSettings(t *testing.T) {
	existing := assistant.ModelRole{ProfileID: "p1", Model: "tts-1", Params: map[string]string{"voice": "alloy", "max_chars": "80"}}
	bots, providers := voiceMigrationStores(t, assistant.BotConfig{ID: "a", Name: "A", ModelRoles: map[string]assistant.ModelRole{"tts": existing}})
	states := map[string]assistant.PersistedPluginState{legacyVoiceTTSPluginID: {Settings: map[string]any{"preset": "model_slot", "send_mode": "always", "max_chars": float64(300)}}}
	if err := MigrateVoicePlugins(states, bots, providers); err != nil {
		t.Fatal(err)
	}
	role := bots.Profiles().Profiles[0].ModelRoles["tts"]
	if role.ProfileID != "p1" || role.Params["voice"] != "alloy" || role.Params["send_mode"] != "always" || role.Params["max_chars"] != "80" {
		t.Fatalf("role = %#v", role)
	}
}

func TestMigrateVoicePluginsMovesSTTBackends(t *testing.T) {
	bots, providers := voiceMigrationStores(t, assistant.BotConfig{ID: "cloud", Name: "Cloud"}, assistant.BotConfig{ID: "local", Name: "Local"})
	cloud := map[string]assistant.PersistedPluginState{legacyVoiceSTTPluginID: {
		ProfileEnabled: map[string]bool{"local": false},
		Settings: map[string]any{
			"backend": "openai_compatible", "endpoint": "https://api.siliconflow.cn/v1/audio/transcriptions", "api_key": "sk-silicon-1",
			"model": "FunAudioLLM/SenseVoiceSmall", "language": "zh", "private_enabled": false, "max_duration_seconds": float64(120),
		},
	}}
	if err := MigrateVoicePlugins(cloud, bots, providers); err != nil {
		t.Fatal(err)
	}
	created := speechProviders(providers.Profiles(), llm.GroupSTT)
	if len(created) != 1 || created[0].Config.BaseURL != "https://api.siliconflow.cn/v1" || created[0].Config.APIKey != "sk-silicon-1" {
		t.Fatalf("providers = %#v", created)
	}
	set := bots.Profiles()
	role := set.Profiles[0].ModelRoles["stt"]
	if role.ProfileID != created[0].ID || role.Model != "FunAudioLLM/SenseVoiceSmall" || role.Params["language"] != "zh" || role.Params["private_enabled"] != "false" || role.Params["max_duration_seconds"] != "120" {
		t.Fatalf("role = %#v", role)
	}
	if _, ok := set.Profiles[1].ModelRoles["stt"]; ok {
		t.Fatal("bot with the plugin switched off must not be migrated")
	}

	localBots, localProviders := voiceMigrationStores(t, assistant.BotConfig{ID: "local", Name: "Local"})
	local := map[string]assistant.PersistedPluginState{legacyVoiceSTTPluginID: {Settings: map[string]any{
		"backend": "local", "local_command": "/opt/whisper/whisper-cli", "local_model_path": "/models/ggml-small.bin",
	}}}
	if err := MigrateVoicePlugins(local, localBots, localProviders); err != nil {
		t.Fatal(err)
	}
	localRole := localBots.Profiles().Profiles[0].ModelRoles["stt"]
	if localRole.ProfileID != "builtin:whisper-cli" || localRole.Params["command"] != "/opt/whisper/whisper-cli" || localRole.Params["model_path"] != "/models/ggml-small.bin" {
		t.Fatalf("local role = %#v", localRole)
	}
	if len(speechProviders(localProviders.Profiles(), llm.GroupSTT)) != 0 {
		t.Fatal("local whisper must not create a provider")
	}
}
