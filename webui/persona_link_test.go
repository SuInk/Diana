// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

func newGroupPersonaLinkTest(t *testing.T) (http.Handler, *MemoryBotGroupConfigStore, *BotHandler) {
	t.Helper()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "persona-link.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	base := assistant.DefaultBotConfig()
	base.ID, base.Name, base.Enabled = "a", "主号", true
	// 反向 WS 没配 Access Token 时整份档案过不了校验，同步机器人配置会失败。
	base.OneBotAccessToken = "token"
	base.GroupAdmission = assistant.GroupAdmission{Mode: assistant.GroupAdmissionBlacklist}.WithDefaults()
	runtime := assistant.NewRuntime(base, consoleGroupListChannel{result: map[string]any{"items": []any{}}}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	profiles := NewMemoryBotProfileStoreFromSet(assistant.ProfileSet{Profiles: []assistant.BotConfig{base}})
	groups := NewMemoryBotGroupConfigStore()
	groups.SetProfileSource(profiles)
	handler := NewBotHandler(context.Background(), runtime)
	handler.SetSQLiteStore(store)
	handler.SetGroupConfigStore(groups)
	handler.SetProfileStore(profiles)
	return botTestRouter(handler), groups, handler
}

func savePersonaForTest(t *testing.T, router http.Handler, persona assistant.Persona) assistant.Persona {
	t.Helper()
	rec := personaRequest(t, router, http.MethodPost, "/api/assistant/personas", personaSavePayload{Persona: persona})
	if rec.Code != http.StatusOK {
		t.Fatalf("save persona status=%d body=%s", rec.Code, rec.Body.String())
	}
	var saved struct {
		Persona assistant.Persona `json:"persona"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	return saved.Persona
}

func saveGroupForTest(t *testing.T, router http.Handler, cfg assistant.GroupConfig) {
	t.Helper()
	rec := personaRequest(t, router, http.MethodPost, "/api/assistant/groups", consoleGroupSavePayload{Config: cfg})
	if rec.Code != http.StatusOK {
		t.Fatalf("save group status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func groupForTest(t *testing.T, groups *MemoryBotGroupConfigStore, groupID string) assistant.GroupConfig {
	t.Helper()
	cfg, ok := groups.ConfigForGroup("a", groupID)
	if !ok {
		t.Fatalf("group %s not saved", groupID)
	}
	return cfg
}

// 绑定人设库的群：保存时以库为准，库里改了自动写过来，没绑定的群不受影响。
func TestGroupPersonaFollowsLibraryUpdates(t *testing.T) {
	router, groups, _ := newGroupPersonaLinkTest(t)
	persona := savePersonaForTest(t, router, assistant.Persona{Name: "技术群", SystemPrompt: "以准确、简洁的方式参与工程讨论。", SelfReference: "我", SentenceEnders: "。"})

	// 前端填进来的内容只是预览：服务端按绑定用库里的内容覆盖。
	saveGroupForTest(t, router, assistant.GroupConfig{BotProfileID: "a", GroupID: "10001", Enabled: true, EnabledSet: true, PersonaID: persona.ID, SystemPrompt: "过期的预览"})
	saveGroupForTest(t, router, assistant.GroupConfig{BotProfileID: "a", GroupID: "10002", Enabled: true, EnabledSet: true, SystemPrompt: "本群自定义"})
	if got := groupForTest(t, groups, "10001"); got.PersonaID != persona.ID || got.SystemPrompt != persona.SystemPrompt || got.SelfReference != "我" {
		t.Fatalf("linked group = %+v", got)
	}

	persona.SystemPrompt = "以准确、简洁、友好的方式参与工程讨论。"
	persona.SentenceEnders = "~"
	rec := personaRequest(t, router, http.MethodPost, "/api/assistant/personas", personaSavePayload{Persona: persona})
	var response struct {
		GroupsSynced int `json:"groups_synced"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.GroupsSynced != 1 {
		t.Fatalf("groups_synced = %d, want 1", response.GroupsSynced)
	}
	if got := groupForTest(t, groups, "10001"); got.SystemPrompt != persona.SystemPrompt || got.SentenceEnders != "~" {
		t.Fatalf("linked group not updated: %+v", got)
	}
	if got := groupForTest(t, groups, "10002"); got.SystemPrompt != "本群自定义" || got.PersonaID != "" {
		t.Fatalf("unlinked group changed: %+v", got)
	}
}

// 库里删掉一套：绑定它的群保留现有文字、改成本群自定义，不会悄悄换回机器人的人设。
func TestDeletingLibraryPersonaKeepsGroupText(t *testing.T) {
	router, groups, _ := newGroupPersonaLinkTest(t)
	persona := savePersonaForTest(t, router, assistant.Persona{Name: "水群", SystemPrompt: "轻松闲聊。"})
	saveGroupForTest(t, router, assistant.GroupConfig{BotProfileID: "a", GroupID: "10001", Enabled: true, EnabledSet: true, PersonaID: persona.ID})

	rec := personaRequest(t, router, http.MethodPost, "/api/assistant/personas/delete", personaDeletePayload{ID: persona.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := groupForTest(t, groups, "10001"); got.PersonaID != "" || got.SystemPrompt != "轻松闲聊。" {
		t.Fatalf("group after persona delete = %+v", got)
	}
}

// 绑定一个库里不存在的 ID：解除绑定，保留提交的文字。
func TestGroupPersonaLinkToMissingPersonaIsDropped(t *testing.T) {
	router, groups, _ := newGroupPersonaLinkTest(t)
	saveGroupForTest(t, router, assistant.GroupConfig{BotProfileID: "a", GroupID: "10001", Enabled: true, EnabledSet: true, PersonaID: "gone", SystemPrompt: "手写的"})
	if got := groupForTest(t, groups, "10001"); got.PersonaID != "" || got.SystemPrompt != "手写的" {
		t.Fatalf("group = %+v", got)
	}
}

// 不认识 persona_id 的调用方原样提交人设：绑定保留；改了任何一项：绑定解除。
func TestKeepGroupPersonaLinkForUnawareClients(t *testing.T) {
	router, groups, handler := newGroupPersonaLinkTest(t)
	persona := savePersonaForTest(t, router, assistant.Persona{Name: "技术群", SystemPrompt: "工程讨论。"})
	saveGroupForTest(t, router, assistant.GroupConfig{BotProfileID: "a", GroupID: "10001", Enabled: true, EnabledSet: true, PersonaID: persona.ID})
	current := groupForTest(t, groups, "10001")
	ctx := context.Background()

	untouched := current
	untouched.PersonaID = ""
	if got := handler.keepGroupPersonaLink(ctx, untouched, current); got.PersonaID != persona.ID {
		t.Fatalf("unchanged persona lost its link: %+v", got)
	}
	edited := untouched
	edited.SystemPrompt = "改过了"
	if got := handler.keepGroupPersonaLink(ctx, edited, current); got.PersonaID != "" {
		t.Fatalf("edited persona kept its link: %+v", got)
	}
}

// 机器人绑定人设库：库里改了机器人也跟着更新，并且立刻交给运行时；删了就解除绑定、保留人设。
func TestBotPersonaFollowsLibraryUpdates(t *testing.T) {
	router, _, handler := newGroupPersonaLinkTest(t)
	persona := savePersonaForTest(t, router, assistant.Persona{Name: "猫娘", SystemPrompt: "你是一只猫。", SelfReference: "咱", SentenceEnders: "喵"})

	set := handler.profiles.Profiles().WithDefaults()
	cfg, _ := set.ConfigForProfile("a")
	linked := cfg.WithLibraryPersona(persona)
	if err := handler.profiles.SaveProfileConfig(linked); err != nil {
		t.Fatal(err)
	}

	persona.SystemPrompt = "你是一只懒猫。"
	persona.SentenceEnders = "喵~"
	rec := personaRequest(t, router, http.MethodPost, "/api/assistant/personas", personaSavePayload{Persona: persona})
	var response struct {
		BotsSynced int    `json:"bots_synced"`
		Warning    string `json:"warning"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Warning != "" {
		t.Fatalf("sync warning: %s", response.Warning)
	}
	if response.BotsSynced != 1 {
		t.Fatalf("bots_synced = %d, want 1 (%s)", response.BotsSynced, rec.Body.String())
	}
	got, _ := handler.profiles.Profiles().ConfigForProfile("a")
	if got.PersonaID != persona.ID || got.SystemPrompt != "你是一只懒猫。" || got.SentenceEnders != "喵~" || got.SelfReference != "咱" {
		t.Fatalf("bot not synced: persona_id=%q prompt=%q enders=%q", got.PersonaID, got.SystemPrompt, got.SentenceEnders)
	}
	if runtimeCfg := handler.runtime.ProfileConfig("a"); runtimeCfg.SystemPrompt != "你是一只懒猫。" {
		t.Fatalf("runtime still uses %q", runtimeCfg.SystemPrompt)
	}

	rec = personaRequest(t, router, http.MethodPost, "/api/assistant/personas/delete", personaDeletePayload{ID: persona.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, _ = handler.profiles.Profiles().ConfigForProfile("a")
	if got.PersonaID != "" || got.SystemPrompt != "你是一只懒猫。" {
		t.Fatalf("bot after delete: persona_id=%q prompt=%q", got.PersonaID, got.SystemPrompt)
	}
}
