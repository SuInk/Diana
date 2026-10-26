// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/llm"
	"github.com/google/uuid"
)

// 语音合成、语音识别两个插件已经拆掉，服务改由模型分配里的插槽管。这里把插件里
// 配好的服务搬过去：GPT-SoVITS 和 OpenAI 兼容的转写接口变成提供商，本机 Whisper
// 用内置的「本机 Whisper」，插件里的发送模式、字数上限、私聊开关变成插槽参数。
//
// 群级的插件开关和设置覆盖没有对应的地方，不迁，只记一行日志。
const (
	legacyVoiceTTSPluginID = "official.voice-tts"
	legacyVoiceSTTPluginID = "official.voice-stt"
)

type voiceMigrationLLMStore interface {
	Profiles() llm.ProfileSet
	SaveProfiles(llm.ProfileSet) error
}

// MigrateVoicePlugins 按插件旧状态补上各机器人的语音插槽。插槽已经配了的机器人
// 不动，所以重复执行是安全的：插件状态在下次保存插件时才会从库里消失，在那之前
// 每次启动都会再看一遍，但不会重复建提供商或覆盖用户后来改的绑定。
func MigrateVoicePlugins(states map[string]assistant.PersistedPluginState, bots groupScopeProfiles, providers voiceMigrationLLMStore) error {
	tts, hasTTS := states[legacyVoiceTTSPluginID]
	stt, hasSTT := states[legacyVoiceSTTPluginID]
	if !hasTTS && !hasSTT {
		return nil
	}
	botSet := bots.Profiles()
	providerSet := providers.Profiles()
	botsChanged, providersChanged := false, false
	for index := range botSet.Profiles {
		bot := &botSet.Profiles[index]
		if hasTTS && legacyPluginEnabled(tts, bot.ID) {
			if role, ok := migratedTTSRole(tts.Settings, bot.ModelRoles["tts"], &providerSet, &providersChanged); ok {
				setModelRole(bot, "tts", role)
				botsChanged = true
				log.Printf("diana 机器人「%s」的语音合成插件设置已迁到模型分配", bot.Name)
			}
		}
		if hasSTT && legacyPluginEnabled(stt, bot.ID) {
			if role, ok := migratedSTTRole(stt.Settings, bot.ModelRoles["stt"], &providerSet, &providersChanged); ok {
				setModelRole(bot, "stt", role)
				botsChanged = true
				log.Printf("diana 机器人「%s」的语音识别插件设置已迁到模型分配", bot.Name)
			}
		}
	}
	// 提供商先落库：机器人绑定引用的是它的 ID，反过来存会留下指向不存在提供商的绑定。
	if providersChanged {
		if err := providers.SaveProfiles(providerSet); err != nil {
			return fmt.Errorf("迁移语音插件：保存提供商失败: %w", err)
		}
	}
	if botsChanged {
		if err := bots.SaveProfiles(botSet); err != nil {
			return fmt.Errorf("迁移语音插件：保存机器人配置失败: %w", err)
		}
	}
	return nil
}

// legacyPluginEnabled 按插件管理器的规则算这台机器人有没有开插件：有逐台开关看
// 逐台的，升级前的全局开关次之，都没有时按内置插件默认开启。
func legacyPluginEnabled(state assistant.PersistedPluginState, botID string) bool {
	if enabled, ok := state.ProfileEnabled[botID]; ok {
		return enabled
	}
	if state.Enabled != nil && !state.ProfileConfigMigrated {
		return *state.Enabled
	}
	return true
}

func setModelRole(bot *assistant.BotConfig, key string, role assistant.ModelRole) {
	roles := make(map[string]assistant.ModelRole, len(bot.ModelRoles)+1)
	for name, existing := range bot.ModelRoles {
		roles[name] = existing
	}
	roles[key] = role
	bot.ModelRoles = roles
}

func roleBound(role assistant.ModelRole) bool {
	return !role.FollowChat && (role.ProfileID != "" || role.ProviderID != "" || role.Group != "")
}

// migratedTTSRole 算出这台机器人迁移后的语音合成绑定，ok 为 false 表示不用改。
func migratedTTSRole(settings map[string]any, current assistant.ModelRole, providers *llm.ProfileSet, changed *bool) (assistant.ModelRole, bool) {
	params := map[string]string{}
	putSetting(params, "send_mode", settings["send_mode"])
	putSetting(params, "max_chars", settings["max_chars"])
	putSetting(params, "timeout_seconds", settings["timeout_seconds"])
	if roleBound(current) {
		// 插件原来就走插槽（或者已经迁过）：只把发送层的设置补进去，不覆盖已有参数。
		merged := false
		next := current
		next.Params = cloneParams(current.Params)
		for key, value := range params {
			if _, exists := next.Params[key]; !exists {
				next.Params[key] = value
				merged = true
			}
		}
		return next, merged
	}
	if settingString(settings, "preset", "local") == "model_slot" {
		return assistant.ModelRole{}, false
	}
	endpoint, ok := legacyGPTSoVITSEndpoint(settings)
	if !ok {
		return assistant.ModelRole{}, false
	}
	params["api"] = string(llm.SpeechAPIGPTSoVITS)
	putSetting(params, "ref_audio_path", firstSetting(settings["ref_audio_path"], os.Getenv("DIANA_TTS_REF_AUDIO_PATH")))
	putSetting(params, "prompt_text", firstSetting(settings["prompt_text"], os.Getenv("DIANA_TTS_PROMPT_TEXT")))
	putSetting(params, "text_lang", settings["text_lang"])
	putSetting(params, "prompt_lang", settings["prompt_lang"])
	putSetting(params, "speed", settings["speed_factor"])
	const model = "gpt-sovits"
	id := ensureSpeechProvider(providers, changed, llm.GroupTTS, "GPT-SoVITS", endpoint, strings.TrimSpace(os.Getenv("DIANA_TTS_API_KEY")), model)
	return assistant.ModelRole{ProfileID: id, Model: model, Params: params}, true
}

// legacyGPTSoVITSEndpoint 判断插件是不是真的配过 GPT-SoVITS。内置插件默认开着、
// 预设指向本机 9880，可绝大多数人根本没装；GPT-SoVITS v2 又必须有参考音频，所以
// 只有填过地址、参考音频，或者选了 Docker / 自定义预设的才迁。
func legacyGPTSoVITSEndpoint(settings map[string]any) (string, bool) {
	preset := settingString(settings, "preset", "local")
	endpoint := settingString(settings, "endpoint", "")
	configured := endpoint != "" || settingString(settings, "ref_audio_path", "") != "" ||
		preset == "docker" || preset == "custom" ||
		strings.TrimSpace(os.Getenv("DIANA_TTS_ENDPOINT")) != "" || strings.TrimSpace(os.Getenv("DIANA_TTS_REF_AUDIO_PATH")) != ""
	if !configured {
		return "", false
	}
	if endpoint == "" {
		switch preset {
		case "docker":
			endpoint = "http://gpt-sovits:9880/tts"
		case "custom":
			return "", false
		default:
			endpoint = firstNonEmpty(strings.TrimSpace(os.Getenv("DIANA_TTS_ENDPOINT")), "http://127.0.0.1:9880/tts")
		}
	}
	return strings.TrimSuffix(strings.TrimRight(endpoint, "/"), "/tts"), true
}

// migratedSTTRole 算出这台机器人迁移后的语音识别绑定，ok 为 false 表示不用改。
func migratedSTTRole(settings map[string]any, current assistant.ModelRole, providers *llm.ProfileSet, changed *bool) (assistant.ModelRole, bool) {
	backend := settingString(settings, "backend", "disabled")
	if backend == "disabled" {
		return assistant.ModelRole{}, false
	}
	params := map[string]string{}
	putSetting(params, "timeout_seconds", settings["timeout_seconds"])
	putSetting(params, "max_duration_seconds", settings["max_duration_seconds"])
	if enabled, ok := settings["private_enabled"].(bool); ok && !enabled {
		params["private_enabled"] = "false"
	}
	if language := settingString(settings, "language", "auto"); language != "auto" {
		params["language"] = language
	}
	if roleBound(current) {
		merged := false
		next := current
		next.Params = cloneParams(current.Params)
		for key, value := range params {
			if _, exists := next.Params[key]; !exists {
				next.Params[key] = value
				merged = true
			}
		}
		return next, merged
	}
	switch backend {
	case "local":
		putSetting(params, "command", settings["local_command"])
		putSetting(params, "model_path", settings["local_model_path"])
		return assistant.ModelRole{ProfileID: "builtin:whisper-cli", Model: "whisper-cli", Params: params}, true
	case "openai_compatible":
		endpoint := settingString(settings, "endpoint", "https://api.openai.com/v1/audio/transcriptions")
		base := strings.TrimSuffix(strings.TrimRight(endpoint, "/"), "/audio/transcriptions")
		model := settingString(settings, "model", "whisper-1")
		params["api"] = string(llm.SpeechAPIOpenAI)
		id := ensureSpeechProvider(providers, changed, llm.GroupSTT, "语音识别", base, settingString(settings, "api_key", ""), model)
		return assistant.ModelRole{ProfileID: id, Model: model, Params: params}, true
	}
	return assistant.ModelRole{}, false
}

// ensureSpeechProvider 找同组同地址同 Key 的提供商，没有就建一个，返回它的 ID。
// 多台机器人用同一个服务时共用一个提供商。
func ensureSpeechProvider(set *llm.ProfileSet, changed *bool, group, name, baseURL, apiKey, model string) string {
	for _, profile := range set.Profiles {
		if llm.NormalizeProfileGroup(profile.Group) == group && strings.TrimRight(profile.Config.BaseURL, "/") == strings.TrimRight(baseURL, "/") && profile.Config.APIKey == apiKey {
			return profile.ID
		}
	}
	if parsed, err := url.Parse(baseURL); err == nil && parsed.Host != "" {
		name = name + "（" + parsed.Host + "）"
	}
	profile := llm.Profile{
		ID:          uuid.NewString(),
		Name:        name,
		Group:       group,
		Description: "由旧版语音插件设置自动迁移",
		UpdatedAt:   time.Now(),
		Config: llm.ProviderConfig{
			Provider: llm.ProviderOpenAICompatible,
			BaseURL:  baseURL,
			APIKey:   apiKey,
			Model:    model,
			Models:   []llm.ModelInfo{{ID: model}},
		},
	}
	set.Profiles = append(set.Profiles, profile)
	*changed = true
	return profile.ID
}

func cloneParams(params map[string]string) map[string]string {
	out := make(map[string]string, len(params)+4)
	for key, value := range params {
		out[key] = value
	}
	return out
}

func settingString(settings map[string]any, key, fallback string) string {
	if value, ok := settings[key].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func firstSetting(value any, fallback string) any {
	if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
		return text
	}
	if value != nil {
		if _, isString := value.(string); !isString {
			return value
		}
	}
	return fallback
}

// putSetting 把插件设置值写成插槽参数的字符串；空值不写，插槽按默认值走。
func putSetting(params map[string]string, key string, value any) {
	var text string
	switch typed := value.(type) {
	case string:
		text = strings.TrimSpace(typed)
	case float64:
		text = strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		text = strconv.Itoa(typed)
	case int64:
		text = strconv.FormatInt(typed, 10)
	case bool:
		text = strconv.FormatBool(typed)
	}
	if text != "" {
		params[key] = text
	}
}
