package assistant

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

type modelCatalogEntry struct {
	Purpose string                    `json:"purpose"`
	Source  string                    `json:"source"`
	Models  []dianaRuntimeModelResult `json:"models"`
	Enabled *bool                     `json:"enabled,omitempty"`
	Note    string                    `json:"note,omitempty"`
}

func (t *dianaRuntimeModelTool) modelCatalog(key string) (string, error) {
	keys := []string{key}
	if key == "all" {
		keys = append(ModelBindingKeys(), "stt", "tts")
	}
	entries := make([]modelCatalogEntry, 0, len(keys))
	for _, purpose := range keys {
		if !isModelBindingKey(purpose) && purpose != "stt" && purpose != "tts" {
			return "", fmt.Errorf("未知模型用途 %q", purpose)
		}
		entry := t.configuredModelPurpose(purpose)
		entries = append(entries, entry)
	}
	body, err := json.Marshal(struct {
		Entries  []modelCatalogEntry `json:"entries"`
		Guidance string              `json:"reply_guidance"`
	}{entries, "这是当前配置的候选顺序，不代表历史调用结果。模型名用 model_id；未配置、已关闭或服务未公开权重时如实说明。历史图片用 group=history 查询。"})
	return string(body), err
}

func (t *dianaRuntimeModelTool) configuredModelPurpose(key string) modelCatalogEntry {
	r := t.provider.runtime
	entry := modelCatalogEntry{Purpose: key, Source: "configured_route", Models: []dianaRuntimeModelResult{}}
	if key == "image" {
		entry.Models = t.provider.configuredImageModelIdentities()
		return entry
	}
	if key == "stt" || key == "tts" {
		id := voiceSTTPluginID
		if key == "tts" {
			id = voiceTTSPluginID
		}
		_, settings, enabled := r.pluginWithSettingsForEvent(id, t.event)
		entry.Enabled = &enabled
		if key == "tts" {
			entry.Models = []dianaRuntimeModelResult{{Provider: "GPT-SoVITS", Group: "tts", GroupLabel: "语音合成", Source: "external_service"}}
			entry.Note = "具体权重由外部 GPT-SoVITS 服务加载，本程序未获得权重模型名；不能把服务名或音色当成模型 ID。"
		} else {
			cfg := voiceSTTConfigFromSettings(settings)
			enabled = enabled && cfg.Backend != voiceSTTBackendDisabled
			model := cfg.Model
			if cfg.Backend == voiceSTTBackendLocal {
				model = ""
				entry.Note = "本地 Whisper 权重由本地文件配置，未公开模型 ID。"
			}
			entry.Models = []dianaRuntimeModelResult{{ModelID: model, Provider: cfg.Backend, Group: "stt", GroupLabel: "语音识别", Source: "configured_route"}}
		}
		return entry
	}
	if key == "embedding" {
		cfg, ok := r.embeddingProviderConfig()
		if ok {
			entry.Models = []dianaRuntimeModelResult{configuredProfileIdentity(cfg, "", key)}
		} else {
			entry.Note = "未配置向量嵌入模型"
		}
		return entry
	}
	r.mu.RLock()
	store := r.llmStore
	registry := r.llmRegistry
	r.mu.RUnlock()
	if store == nil {
		entry.Note = "未配置模型存储；自定义 Provider 未公开此用途的配置"
		return entry
	}
	group := key
	purpose := ""
	if owner, ok := llmPurposeGroup[key]; ok {
		group, purpose = owner, key
	}
	if group == "chat" {
		group = llm.GroupChat
	}
	set := store.Profiles().WithDefaults()
	if registry == nil {
		if source, ok := store.(LLMProviderRegistryStore); ok {
			registry, _ = source.ProviderRegistry()
		}
	}
	profiles, err := r.roleBoundProfiles(purpose, set, group, r.modelRolesForContext(t.provider.ctx))
	if err != nil {
		entry.Note = "该用途的模型绑定无法解析"
		return entry
	}
	if len(profiles) == 0 {
		profiles = llmProfilesInGroup(set, llm.NormalizeProfileGroup(group))
	}
	if len(profiles) == 0 {
		profiles = fallbackProfilesForGroup(set, group)
	}
	for _, p := range profiles {
		identity := configuredProfileIdentity(p.Config, p.Name, group)
		if registry != nil {
			selection := profileRegistrySelection(registry, p)
			if model, ok := registry.Model(selection.ModelID); ok {
				identity.ModelID = model.ModelID
				if provider, ok := registry.Provider(model.ProviderID); ok {
					identity.Provider, identity.Protocol = provider.ID, string(provider.Protocol)
				}
			}
		}
		entry.Models = append(entry.Models, identity)
	}
	if len(entry.Models) == 0 {
		entry.Note = "未配置可用模型"
	}
	return entry
}

func configuredProfileIdentity(cfg llm.ProviderConfig, name, group string) dianaRuntimeModelResult {
	return dianaRuntimeModelResult{ModelID: strings.TrimSpace(cfg.Model), Provider: string(cfg.Provider), ConfigName: name, Protocol: string(cfg.APIFormat), Group: group, GroupLabel: runtimeModelGroupLabel(group), Source: "configured_route"}
}
