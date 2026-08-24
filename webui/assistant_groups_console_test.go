// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

type consoleGroupListChannel struct {
	result map[string]any
	err    error
}

func (consoleGroupListChannel) Connect(context.Context, assistant.EventHandler) error { return nil }
func (consoleGroupListChannel) Send(context.Context, assistant.OutgoingMessage) error { return nil }
func (consoleGroupListChannel) Status() assistant.ChannelStatus                       { return assistant.ChannelStatus{} }
func (consoleGroupListChannel) Close() error                                          { return nil }

func (c consoleGroupListChannel) CallAPI(_ context.Context, action string, _ map[string]any) (map[string]any, error) {
	if action != "get_group_list" {
		return nil, errors.New("unexpected OneBot action")
	}
	return c.result, c.err
}

func TestConsoleGroupsListsJoinedAndSavedGroups(t *testing.T) {
	base := assistant.DefaultBotConfig()
	runtime := assistant.NewRuntime(base, consoleGroupListChannel{result: map[string]any{
		"items": []any{
			map[string]any{"group_id": float64(10001), "group_name": "Alpha 群", "member_count": float64(12), "max_member_count": float64(200)},
			map[string]any{"group_id": "20002", "group_name": "Beta 群", "member_count": 34, "max_member_count": 500},
		},
	}}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	store := NewMemoryBotGroupConfigStore()
	if _, err := store.SaveGroupConfig(assistant.GroupConfig{GroupID: "20002", Enabled: false, EnabledSet: true, SystemPrompt: "专属人设"}, base); err != nil {
		t.Fatalf("SaveGroupConfig(20002) error = %v", err)
	}
	if _, err := store.SaveGroupConfig(assistant.GroupConfig{GroupID: "30003", Enabled: true, EnabledSet: true}, base); err != nil {
		t.Fatalf("SaveGroupConfig(30003) error = %v", err)
	}
	handler := NewBotHandler(context.Background(), runtime)
	handler.SetGroupConfigStore(store)
	router := botTestRouter(handler)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/groups", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("assistant groups status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response consoleGroupsResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !response.LiveAvailable || response.Warning != "" || len(response.Groups) != 3 {
		t.Fatalf("response = %#v", response)
	}
	groups := make(map[string]consoleGroupItem, len(response.Groups))
	for _, group := range response.Groups {
		groups[group.GroupID] = group
	}
	if group := groups["10001"]; !group.Joined || group.Configured || group.GroupName != "Alpha 群" || group.MemberCount != 12 || group.MaxMemberCount != 200 || group.AvatarURL == "" {
		t.Fatalf("live unconfigured group = %#v", group)
	}
	if group := groups["20002"]; !group.Joined || !group.Configured || group.Enabled || group.SystemPrompt != "专属人设" || group.GroupName != "Beta 群" {
		t.Fatalf("live configured group = %#v", group)
	}
	if group := groups["30003"]; group.Joined || !group.Configured {
		t.Fatalf("saved group = %#v", group)
	}

	aliasRec := httptest.NewRecorder()
	router.ServeHTTP(aliasRec, httptest.NewRequest(http.MethodGet, "/api/assistant/groups", nil))
	if aliasRec.Code != http.StatusOK {
		t.Fatalf("chatbot groups alias status = %d, body = %s", aliasRec.Code, aliasRec.Body.String())
	}
}

func TestConsoleGroupsFallsBackToSavedConfigWhenLiveListUnavailable(t *testing.T) {
	base := assistant.DefaultBotConfig()
	runtime := assistant.NewRuntime(base, consoleGroupListChannel{err: errors.New("not connected")}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	store := NewMemoryBotGroupConfigStore()
	if _, err := store.SaveGroupConfig(assistant.GroupConfig{GroupID: "40004", Enabled: true, EnabledSet: true}, base); err != nil {
		t.Fatalf("SaveGroupConfig() error = %v", err)
	}
	handler := NewBotHandler(context.Background(), runtime)
	handler.SetGroupConfigStore(store)
	router := botTestRouter(handler)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/groups", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response consoleGroupsResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if response.LiveAvailable || response.Warning == "" || len(response.Groups) != 1 || response.Groups[0].GroupID != "40004" || !response.Groups[0].Configured {
		t.Fatalf("response = %#v", response)
	}
}

type countingGroupListChannel struct {
	calls  atomic.Int32
	result map[string]any
	params []map[string]any
	mu     sync.Mutex
}

// 结构体里有 atomic.Int32 和 sync.Mutex，接收者必须取指针，否则每次调用都复制一份锁。
func (*countingGroupListChannel) Connect(context.Context, assistant.EventHandler) error { return nil }
func (*countingGroupListChannel) Send(context.Context, assistant.OutgoingMessage) error { return nil }
func (*countingGroupListChannel) Status() assistant.ChannelStatus                       { return assistant.ChannelStatus{} }
func (*countingGroupListChannel) Close() error                                          { return nil }

func (c *countingGroupListChannel) CallAPI(_ context.Context, action string, params map[string]any) (map[string]any, error) {
	if action != "get_group_list" {
		return nil, errors.New("unexpected OneBot action")
	}
	c.calls.Add(1)
	c.mu.Lock()
	c.params = append(c.params, params)
	c.mu.Unlock()
	return c.result, nil
}

func TestConsoleGroupsCachesLiveListUntilRefresh(t *testing.T) {
	base := assistant.DefaultBotConfig()
	channel := &countingGroupListChannel{result: map[string]any{
		"items": []any{
			map[string]any{"group_id": "10001", "group_name": "Cached 群"},
		},
	}}
	runtime := assistant.NewRuntime(base, channel, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewBotHandler(context.Background(), runtime)
	router := botTestRouter(handler)

	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/assistant/groups", nil))
	second := httptest.NewRecorder()
	router.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/api/assistant/groups", nil))
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("status first=%d second=%d", first.Code, second.Code)
	}
	if channel.calls.Load() != 1 {
		t.Fatalf("live group list calls = %d, want 1", channel.calls.Load())
	}

	refresh := httptest.NewRecorder()
	router.ServeHTTP(refresh, httptest.NewRequest(http.MethodGet, "/api/assistant/groups?refresh=1", nil))
	if refresh.Code != http.StatusOK {
		t.Fatalf("refresh status = %d, body = %s", refresh.Code, refresh.Body.String())
	}
	if channel.calls.Load() != 2 {
		t.Fatalf("live group list calls after refresh = %d, want 2", channel.calls.Load())
	}
	channel.mu.Lock()
	defer channel.mu.Unlock()
	if len(channel.params) != 2 {
		t.Fatalf("params = %#v", channel.params)
	}
	if _, cachedCallHasNoCache := channel.params[0]["no_cache"]; cachedCallHasNoCache {
		t.Fatalf("cached list should not force no_cache: %#v", channel.params[0])
	}
	if channel.params[1]["no_cache"] != true {
		t.Fatalf("refresh list should force no_cache: %#v", channel.params[1])
	}
}

type blockingGroupListChannel struct{}

func (blockingGroupListChannel) Connect(context.Context, assistant.EventHandler) error { return nil }
func (blockingGroupListChannel) Send(context.Context, assistant.OutgoingMessage) error { return nil }
func (blockingGroupListChannel) Status() assistant.ChannelStatus                       { return assistant.ChannelStatus{} }
func (blockingGroupListChannel) Close() error                                          { return nil }

func (blockingGroupListChannel) CallAPI(ctx context.Context, action string, _ map[string]any) (map[string]any, error) {
	if action != "get_group_list" {
		return nil, errors.New("unexpected OneBot action")
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestConsoleGroupsTimesOutSlowLiveList(t *testing.T) {
	originalTimeout := consoleLiveGroupTimeout
	consoleLiveGroupTimeout = 40 * time.Millisecond
	t.Cleanup(func() { consoleLiveGroupTimeout = originalTimeout })

	base := assistant.DefaultBotConfig()
	runtime := assistant.NewRuntime(base, blockingGroupListChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	store := NewMemoryBotGroupConfigStore()
	if _, err := store.SaveGroupConfig(assistant.GroupConfig{GroupID: "50005", Enabled: true, EnabledSet: true}, base); err != nil {
		t.Fatalf("SaveGroupConfig() error = %v", err)
	}
	handler := NewBotHandler(context.Background(), runtime)
	handler.SetGroupConfigStore(store)
	router := botTestRouter(handler)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/groups", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response consoleGroupsResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if response.LiveAvailable || !strings.Contains(response.Warning, "超时") || len(response.Groups) != 1 || response.Groups[0].GroupID != "50005" {
		t.Fatalf("response = %#v", response)
	}
}

func TestConsoleGroupsSavesRecallReplyAutoDeletePolicy(t *testing.T) {
	base := assistant.DefaultBotConfig()
	runtime := assistant.NewRuntime(base, consoleGroupListChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	store := NewMemoryBotGroupConfigStore()
	handler := NewBotHandler(context.Background(), runtime)
	handler.SetGroupConfigStore(store)
	router := botTestRouter(handler)

	body := `{"config":{"group_id":"50005","enabled":true,"enabled_set":true,"natural_interjection_enabled":true,"recall_reply_auto_delete_enabled":true,"recall_reply_auto_delete_delay_seconds":90}}`
	req := httptest.NewRequest(http.MethodPost, "/api/assistant/groups", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Config assistant.GroupConfig `json:"config"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Config.NaturalInterjectionEnabled == nil || !*response.Config.NaturalInterjectionEnabled || response.Config.RecallReplyAutoDeleteEnabled == nil || !*response.Config.RecallReplyAutoDeleteEnabled || response.Config.RecallReplyTTLSeconds != 90 {
		t.Fatalf("response config = %#v", response.Config)
	}
	saved, ok := store.ConfigForGroup("", "50005")
	if !ok || saved.NaturalInterjectionEnabled == nil || !*saved.NaturalInterjectionEnabled || saved.RecallReplyAutoDeleteEnabled == nil || !*saved.RecallReplyAutoDeleteEnabled || saved.RecallReplyTTLSeconds != 90 {
		t.Fatalf("saved config = %#v, ok = %v", saved, ok)
	}
}

func TestConsoleGroupsValidatesPluginSettingOverrides(t *testing.T) {
	base := assistant.DefaultBotConfig()
	runtime := assistant.NewRuntime(base, consoleGroupListChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	store := NewMemoryBotGroupConfigStore()
	handler := NewBotHandler(context.Background(), runtime)
	handler.SetGroupConfigStore(store)
	router := botTestRouter(handler)

	body := `{"config":{"group_id":"50006","enabled":true,"enabled_set":true,"plugin_setting_overrides":{"official.nonebot-plugin-resolver-go":{"fetch_title":false,"max_links":8}}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/assistant/groups", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Config assistant.GroupConfig `json:"config"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	settings := response.Config.PluginSettingOverrides["official.nonebot-plugin-resolver-go"]
	if settings["fetch_title"] != false || settings["max_links"] != float64(8) {
		t.Fatalf("settings = %#v", settings)
	}

	// A client predating group plugin settings must not erase the field while
	// saving an unrelated group option.
	legacyBody := `{"config":{"group_id":"50006","enabled":false,"enabled_set":true,"system_prompt":"legacy update"}}`
	req = httptest.NewRequest(http.MethodPost, "/api/assistant/groups", strings.NewReader(legacyBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy status = %d, body = %s", rec.Code, rec.Body.String())
	}

	secretBody := `{"config":{"group_id":"50006","enabled":true,"enabled_set":true,"plugin_setting_overrides":{"official.nonebot-plugin-resolver-go":{"xhs_cookie":"must-not-save"}}}}`
	req = httptest.NewRequest(http.MethodPost, "/api/assistant/groups", strings.NewReader(secretBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "must-not-save") {
		t.Fatalf("secret status = %d, body = %s", rec.Code, rec.Body.String())
	}

	saved, ok := store.ConfigForGroup("", "50006")
	if !ok || saved.PluginSettingOverrides["official.nonebot-plugin-resolver-go"]["max_links"] != float64(8) {
		t.Fatalf("saved = %#v, ok = %v", saved, ok)
	}

	resetBody := `{"config":{"group_id":"50006","enabled":true,"enabled_set":true,"plugin_setting_overrides":{}}}`
	req = httptest.NewRequest(http.MethodPost, "/api/assistant/groups", strings.NewReader(resetBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset status = %d, body = %s", rec.Code, rec.Body.String())
	}
	saved, ok = store.ConfigForGroup("", "50006")
	if !ok || len(saved.PluginSettingOverrides) != 0 {
		t.Fatalf("reset saved = %#v, ok = %v", saved, ok)
	}
}

// 群配置按机器人各存一份：同一个群号下两台机器人各配各的，控制台按作用域取。
func TestConsoleGroupsAreScopedPerBotProfile(t *testing.T) {
	store := NewMemoryBotGroupConfigStore()
	base := assistant.BotConfig{}
	if _, err := store.SaveGroupConfig(assistant.GroupConfig{BotProfileID: "bot-onebot", GroupID: "100", GroupTriggers: []string{"小 A"}}, base); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveGroupConfig(assistant.GroupConfig{BotProfileID: "bot-telegram", GroupID: "100", GroupTriggers: []string{"小 B"}}, base); err != nil {
		t.Fatal(err)
	}

	onebot, ok := store.ConfigForGroup("bot-onebot", "100")
	if !ok || len(onebot.GroupTriggers) == 0 || onebot.GroupTriggers[0] != "小 A" {
		t.Fatalf("onebot group config = %#v ok=%v", onebot, ok)
	}
	telegram, ok := store.ConfigForGroup("bot-telegram", "100")
	if !ok || len(telegram.GroupTriggers) == 0 || telegram.GroupTriggers[0] != "小 B" {
		t.Fatalf("telegram group config = %#v ok=%v", telegram, ok)
	}
	if len(store.Groups().GroupsForProfile("bot-onebot")) != 1 {
		t.Fatalf("每台机器人应各有一份配置: %#v", store.Groups().Groups)
	}
}

// 升级前的群配置没有机器人标记，在迁移把它填上之前不能整体失效。
func TestConsoleGroupsFallBackToLegacyRecords(t *testing.T) {
	set := assistant.GroupConfigSet{Groups: []assistant.GroupConfig{{GroupID: "100", GroupTriggers: []string{"老配置"}}}}
	cfg, ok := set.ConfigForGroup("bot-onebot", "100")
	if !ok || len(cfg.GroupTriggers) == 0 || cfg.GroupTriggers[0] != "老配置" {
		t.Fatalf("legacy fallback = %#v ok=%v", cfg, ok)
	}
}
