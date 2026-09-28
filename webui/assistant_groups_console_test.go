// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
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
		t.Fatalf("diana groups alias status = %d, body = %s", aliasRec.Code, aliasRec.Body.String())
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
	fail   atomic.Bool
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
	if c.fail.Load() {
		return nil, errors.New("get_group_list timeout")
	}
	return c.result, nil
}

// 实时拉取失败时退回上一次成功的列表，下拉框不能因为一次超时就空掉。
func TestConsoleGroupsFallsBackToLastLiveListOnFailure(t *testing.T) {
	channel := &countingGroupListChannel{result: map[string]any{
		"items": []any{map[string]any{"group_id": "10001", "group_name": "上次的群"}},
	}}
	runtime := assistant.NewRuntime(assistant.DefaultBotConfig(), channel, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewBotHandler(context.Background(), runtime)
	if groups, ok, _ := handler.liveConsoleGroups(t.Context(), "", true); !ok || len(groups) != 1 {
		t.Fatalf("first fetch = %#v ok=%v", groups, ok)
	}
	channel.fail.Store(true)
	groups, ok, warning := handler.liveConsoleGroups(t.Context(), "", true)
	if !ok || len(groups) != 1 || groups[0].GroupName != "上次的群" {
		t.Fatalf("fallback = %#v ok=%v", groups, ok)
	}
	if warning == "" {
		t.Fatal("fallback should tell the console the list is stale")
	}
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
	// 协议端自己的群列表缓存不随入群、改名失效，每次真去问都要绕过它。
	for index, params := range channel.params {
		if params["no_cache"] != true {
			t.Fatalf("call %d should force no_cache: %#v", index, params)
		}
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
	saved, ok := store.ConfigForGroup(response.Config.BotProfileID, "50005")
	if !ok || saved.NaturalInterjectionEnabled == nil || !*saved.NaturalInterjectionEnabled || saved.RecallReplyAutoDeleteEnabled == nil || !*saved.RecallReplyAutoDeleteEnabled || saved.RecallReplyTTLSeconds != 90 {
		t.Fatalf("saved config = %#v, ok = %v", saved, ok)
	}
}

func TestConsoleGroupsRejectsAllBotScopeWhenMultipleProfilesExist(t *testing.T) {
	base := assistant.DefaultBotConfig()
	runtime := assistant.NewRuntime(base, consoleGroupListChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewBotHandler(context.Background(), runtime)
	profiles := NewMemoryBotProfileStore(base)
	set := profiles.Profiles()
	second := base
	second.ID = "second-bot"
	second.Name = "第二台机器人"
	set.Profiles = append(set.Profiles, second)
	if err := profiles.SaveProfiles(set); err != nil {
		t.Fatal(err)
	}
	handler.SetProfileStore(profiles)
	handler.SetGroupConfigStore(NewMemoryBotGroupConfigStore())
	router := botTestRouter(handler)

	body := `{"config":{"group_id":"50007","enabled":true}}`
	req := httptest.NewRequest(http.MethodPost, "/api/assistant/groups", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "具体机器人") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
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

	profileID := handler.profiles.Profiles().Profiles[0].ID
	saved, ok := store.ConfigForGroup(profileID, "50006")
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
	saved, ok = store.ConfigForGroup(profileID, "50006")
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

// 关系图接口：按群取数、range 参数校验、没有消息存储时明确报错。
func TestGroupRelationGraphEndpoint(t *testing.T) {
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "relations-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	now := time.Now()
	for index, seed := range []struct{ userID, name string }{{"1001", "Alice"}, {"1001", "Alice"}, {"1002", "Bob"}} {
		event := assistant.MessageEvent{
			Kind: assistant.EventKindGroup, GroupID: "555", UserID: seed.userID,
			MessageID: fmt.Sprintf("m%d", index), SenderName: seed.name,
			Time: now.Unix(), ToMe: true, RawMessage: "在吗",
		}
		if err := store.AppendMessageEvent(ctx, "group:555", event); err != nil {
			t.Fatal(err)
		}
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &BotHandler{sqlite: store, runtime: assistant.NewRuntime(
		assistant.BotConfig{BotAccount: "42"}, fakeChannel{}, assistant.NewPluginManager(), nil, nil, nil, nil)}
	handler.registerConsoleGroupRoutes(router)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/groups/555/relations?range=24h", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Range string                       `json:"range"`
		Graph assistant.GroupRelationGraph `json:"graph"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Range != "24h" || payload.Graph.GroupID != "555" {
		t.Fatalf("payload = %#v", payload)
	}
	// 配置里的机器人账号要当中心节点：新群还没有机器人自己的发言，光扫历史找不出中心。
	if payload.Graph.BotID != "42" {
		t.Fatalf("bot = %q, want 42", payload.Graph.BotID)
	}
	if payload.Graph.Messages != 3 || payload.Graph.Participants != 2 {
		t.Fatalf("messages = %d, participants = %d", payload.Graph.Messages, payload.Graph.Participants)
	}

	bad := httptest.NewRecorder()
	router.ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/api/assistant/groups/555/relations?range=nonsense", nil))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("非法 range 应当 400，实际 %d", bad.Code)
	}

	missing := gin.New()
	(&BotHandler{}).registerConsoleGroupRoutes(missing)
	unavailable := httptest.NewRecorder()
	missing.ServeHTTP(unavailable, httptest.NewRequest(http.MethodGet, "/api/assistant/groups/555/relations", nil))
	if unavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("没有消息存储时应当 503，实际 %d", unavailable.Code)
	}
}

// 群级补充判据和机器人级共用一个上限：后端不卡住的话，超长文本会一路拼到评分
// 提示词尾部，而这条链路解析失败的兜底是整群沉默。
func TestConsoleGroupsCapsProactiveReplyExtraCriteria(t *testing.T) {
	base := assistant.DefaultBotConfig()
	runtime := assistant.NewRuntime(base, consoleGroupListChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	store := NewMemoryBotGroupConfigStore()
	handler := NewBotHandler(context.Background(), runtime)
	handler.SetGroupConfigStore(store)
	router := botTestRouter(handler)

	save := func(criteria string) *httptest.ResponseRecorder {
		body, err := json.Marshal(map[string]any{"config": map[string]any{
			"group_id": "50007", "enabled": true, "enabled_set": true,
			"proactive_reply_extra_criteria": criteria,
		}})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/assistant/groups", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	if rec := save(strings.Repeat("规", assistant.ProactiveReplyExtraCriteriaMaxRunes+1)); rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := save("  本群叫鸽子是催更  "); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	profileID := handler.profiles.Profiles().Profiles[0].ID
	saved, ok := store.ConfigForGroup(profileID, "50007")
	if !ok || saved.ProactiveReplyExtraCriteria != "本群叫鸽子是催更" {
		t.Fatalf("saved = %q, ok = %v", saved.ProactiveReplyExtraCriteria, ok)
	}
}

// 额度是个「悄悄生效」的闸门：用满之后机器人就是不说话。群列表里必须带上用了多少
// 和生效上限，不然界面上没人知道是撞了额度还是坏了。
func TestConsoleGroupsReportsQuotaUsage(t *testing.T) {
	base := assistant.DefaultBotConfig()
	base.ModelCallQuota = 400
	runtime := assistant.NewRuntime(base, consoleGroupListChannel{result: map[string]any{"items": []any{}}}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	store := NewMemoryBotGroupConfigStore()
	// 40001 跟随机器人，40002 自己填了一档次数上限。
	if _, err := store.SaveGroupConfig(assistant.GroupConfig{GroupID: "40001", Enabled: true, EnabledSet: true}, base); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveGroupConfig(assistant.GroupConfig{GroupID: "40002", Enabled: true, EnabledSet: true, ModelCallQuota: 2}, base); err != nil {
		t.Fatal(err)
	}
	sqlite, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "quota.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqlite.Close()
	ctx := context.Background()
	now := time.Now()
	for _, entry := range []struct {
		groupID string
		at      time.Time
	}{
		{"40001", now.Add(-time.Minute)},
		{"40001", now.Add(-2 * time.Minute)},
		{"40002", now.Add(-3 * time.Minute)},
		// 窗口之外的不该算进来。
		{"40001", now.Add(-assistant.GroupModelQuotaWindow() - time.Hour)},
	} {
		meta := map[string]any{"group_id": entry.groupID, "total_tokens": 100}
		if err := sqlite.AppendLog(ctx, applog.Entry{Action: "llm_usage", CreatedAt: entry.at, Metadata: meta}); err != nil {
			t.Fatal(err)
		}
	}
	handler := NewBotHandler(ctx, runtime)
	handler.SetGroupConfigStore(store)
	handler.SetSQLiteStore(sqlite)
	router := botTestRouter(handler)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/groups", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response consoleGroupsResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.QuotaWindowSeconds != int(assistant.GroupModelQuotaWindow()/time.Second) {
		t.Fatalf("窗口长度没带出来: %d", response.QuotaWindowSeconds)
	}
	groups := make(map[string]consoleGroupItem, len(response.Groups))
	for _, group := range response.Groups {
		groups[group.GroupID] = group
	}
	// 群里没填就该显示机器人那一档，显示的上限必须和真正拦人的那个一致。
	if group := groups["40001"]; group.QuotaCallLimit != 400 || group.QuotaCallsUsed != 2 {
		t.Fatalf("跟随机器人的群 = %#v", group)
	}
	// 群里填了以群为准。
	if group := groups["40002"]; group.QuotaCallLimit != 2 || group.QuotaCallsUsed != 1 {
		t.Fatalf("自己填了额度的群 = %#v", group)
	}
}

// perProfileGroupListRuntime 按机器人给出不同的群列表；不指明机器人的调用照真实
// 运行时的样子报错，因为有两台 OneBot 机器人时运行时不会替调用方挑。
type perProfileGroupListRuntime struct {
	BotRuntime
	groups map[string][]any
	calls  atomic.Int32
}

func (r *perProfileGroupListRuntime) CallOneBotAPI(context.Context, string, map[string]any) (map[string]any, error) {
	return nil, errors.New("diana: 有多台机器人，请指定要用哪一台")
}

func (r *perProfileGroupListRuntime) CallOneBotAPIForProfile(_ context.Context, profileID, action string, _ map[string]any) (map[string]any, error) {
	if action != "get_group_list" {
		return nil, errors.New("unexpected OneBot action")
	}
	r.calls.Add(1)
	return map[string]any{"items": r.groups[profileID]}, nil
}

// 两台 OneBot 机器人时，全部机器人视图要把两边的群都列出来，并且各自记上归属；
// 选了某一台时只列它自己的群。
func TestConsoleGroupsListsEveryOneBotProfile(t *testing.T) {
	first := assistant.DefaultBotConfig()
	first.ID, first.Platform, first.Enabled = "qq-a", assistant.PlatformOneBotV11, true
	second := assistant.DefaultBotConfig()
	second.ID, second.Platform, second.Enabled = "qq-b", assistant.PlatformOneBotV11, true
	store := NewMemoryBotProfileStore(first)
	if err := store.SaveProfiles(assistant.ProfileSet{Profiles: []assistant.BotConfig{first, second}}); err != nil {
		t.Fatal(err)
	}
	handler := &BotHandler{profiles: store, runtime: &perProfileGroupListRuntime{groups: map[string][]any{
		"qq-a": {map[string]any{"group_id": "10001", "group_name": "甲群"}},
		"qq-b": {map[string]any{"group_id": "10002", "group_name": "乙群"}},
	}}}

	all, ok, _ := handler.liveConsoleGroupsForAllBots(t.Context(), false)
	owners := map[string]string{}
	for _, group := range all {
		owners[group.GroupID] = group.BotProfileID
	}
	if !ok || owners["10001"] != "qq-a" || owners["10002"] != "qq-b" {
		t.Fatalf("all bots = %#v ok=%v", all, ok)
	}

	scoped, ok, _ := handler.liveConsoleGroups(t.Context(), "qq-b", false)
	if !ok || len(scoped) != 1 || scoped[0].GroupID != "10002" || scoped[0].BotProfileID != "qq-b" {
		t.Fatalf("scoped = %#v ok=%v", scoped, ok)
	}
}

// 复用同一条连接的两台机器人是同一个 QQ 号，群列表只问一次，各自记上归属。
func TestConsoleGroupsShareListAcrossSameConnection(t *testing.T) {
	first := assistant.DefaultBotConfig()
	first.ID, first.Platform, first.Enabled = "qq-a", assistant.PlatformOneBotV11, true
	second := assistant.DefaultBotConfig()
	second.ID, second.Platform, second.Enabled, second.ConnectionProfileID = "qq-b", assistant.PlatformOneBotV11, true, "qq-a"
	store := NewMemoryBotProfileStore(first)
	if err := store.SaveProfiles(assistant.ProfileSet{Profiles: []assistant.BotConfig{first, second}}); err != nil {
		t.Fatal(err)
	}
	runtime := &perProfileGroupListRuntime{groups: map[string][]any{
		"qq-a": {map[string]any{"group_id": "10001", "group_name": "同一个号的群"}},
	}}
	handler := &BotHandler{profiles: store, runtime: runtime}
	if groups, ok, _ := handler.liveConsoleGroups(t.Context(), "qq-a", false); !ok || len(groups) != 1 || groups[0].BotProfileID != "qq-a" {
		t.Fatalf("qq-a = %#v ok=%v", groups, ok)
	}
	groups, ok, _ := handler.liveConsoleGroups(t.Context(), "qq-b", false)
	if !ok || len(groups) != 1 || groups[0].BotProfileID != "qq-b" {
		t.Fatalf("qq-b = %#v ok=%v", groups, ok)
	}
	if got := runtime.calls.Load(); got != 1 {
		t.Fatalf("同一条连接问了 %d 次群列表", got)
	}
}
