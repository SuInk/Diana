// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func switchesTestRouter(t *testing.T) (*BotHandler, *MemoryBotGroupConfigStore, *MemoryBotProfileStore) {
	t.Helper()
	base := assistant.DefaultBotConfig()
	base.ID = "a"
	runtime := assistant.NewRuntime(base, consoleGroupListChannel{result: map[string]any{"items": []any{}}}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	profiles := NewMemoryBotProfileStoreFromSet(assistant.ProfileSet{Profiles: []assistant.BotConfig{base}})
	groups := NewMemoryBotGroupConfigStore()
	groups.SetProfileSource(profiles)
	handler := NewBotHandler(context.Background(), runtime)
	handler.SetGroupConfigStore(groups)
	handler.SetProfileStore(profiles)
	return handler, groups, profiles
}

func postSwitches(t *testing.T, handler *BotHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/assistant/groups/switches", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	botTestRouter(handler).ServeHTTP(rec, request)
	return rec
}

// 一键停用只动前端列出来的那几个群，没列到的群一个都不碰。
func TestConsoleGroupSwitchesBulkDisable(t *testing.T) {
	handler, groups, _ := switchesTestRouter(t)
	base := assistant.DefaultBotConfig()
	base.ID = "a"
	for _, groupID := range []string{"10001", "20002", "30003"} {
		if _, err := groups.SaveGroupConfig(assistant.GroupConfig{BotProfileID: "a", GroupID: groupID, Enabled: true, EnabledSet: true}, base); err != nil {
			t.Fatal(err)
		}
	}

	rec := postSwitches(t, handler, `{"bot_profile_id":"a","group_ids":["10001","20002"],"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Updated int `json:"updated"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Updated != 2 {
		t.Fatalf("updated = %d，只该动列出来的两个群", response.Updated)
	}
	for _, groupID := range []string{"10001", "20002"} {
		if cfg, ok := groups.ConfigForGroup("a", groupID); !ok || cfg.Enabled {
			t.Fatalf("群 %s 应当被关掉：%#v", groupID, cfg)
		}
	}
	if cfg, ok := groups.ConfigForGroup("a", "30003"); !ok || !cfg.Enabled {
		t.Fatalf("没列出来的群被动了：%#v", cfg)
	}
}

// 一键启用要能给还没有群配置的群补一条记录，否则新群默认关的实例点了没反应。
func TestConsoleGroupSwitchesCreatesMissingConfig(t *testing.T) {
	handler, groups, _ := switchesTestRouter(t)

	if rec := postSwitches(t, handler, `{"bot_profile_id":"a","group_ids":["40004"],"enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	if cfg, ok := groups.ConfigForGroup("a", "40004"); !ok || !cfg.Enabled || !cfg.EnabledSet {
		t.Fatalf("没有群配置的群应当补出一条开着的记录：%#v ok=%v", cfg, ok)
	}
}

// 新群默认写进机器人配置，之后新建的群配置跟着它走。
func TestConsoleGroupSwitchesNewGroupDefault(t *testing.T) {
	handler, _, profiles := switchesTestRouter(t)

	if rec := postSwitches(t, handler, `{"bot_profile_id":"a","new_group_enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	profile := profiles.Profiles().Profiles[0]
	if profile.GroupAdmission.NewGroupEnabled() {
		t.Fatalf("新群默认没关掉：%#v", profile.GroupAdmission)
	}
	if assistant.DefaultGroupConfig("50005", profile).Enabled {
		t.Fatal("新群默认关掉之后，新建的群配置不该是开着的")
	}
	if len(profile.GroupAdmission.AllowedGroups) != 0 {
		t.Fatalf("新群默认不该带出白名单：%v", profile.GroupAdmission.AllowedGroups)
	}
}

// 群号不是数字的直接跳过，不能让它落进存储。
func TestConsoleGroupSwitchesIgnoresInvalidGroupIDs(t *testing.T) {
	handler, groups, _ := switchesTestRouter(t)

	if rec := postSwitches(t, handler, `{"bot_profile_id":"a","group_ids":["not-a-group"],"enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	if len(groups.Groups().Groups) != 0 {
		t.Fatalf("非法群号写进了存储：%#v", groups.Groups().Groups)
	}
}

// 复用同一条连接、在同一个群都开着的机器人，会在列表里互相标出来。
func TestConsoleGroupsMarkSharedBots(t *testing.T) {
	base := assistant.DefaultBotConfig()
	base.ID, base.Name, base.Enabled = "a", "主号", true
	reuse := base
	reuse.ID, reuse.Name, reuse.ConnectionProfileID = "b", "分身", "a"
	apart := base
	apart.ID, apart.Name, apart.ConnectionProfileID = "c", "另一条连接", ""

	runtime := assistant.NewRuntime(base, consoleGroupListChannel{result: map[string]any{"items": []any{}}}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	profiles := NewMemoryBotProfileStoreFromSet(assistant.ProfileSet{Profiles: []assistant.BotConfig{base, reuse, apart}})
	groups := NewMemoryBotGroupConfigStore()
	groups.SetProfileSource(profiles)
	for _, profile := range []assistant.BotConfig{base, reuse, apart} {
		if _, err := groups.SaveGroupConfig(assistant.GroupConfig{BotProfileID: profile.ID, GroupID: "10001", Enabled: true, EnabledSet: true}, profile); err != nil {
			t.Fatal(err)
		}
	}
	handler := NewBotHandler(context.Background(), runtime)
	handler.SetGroupConfigStore(groups)
	handler.SetProfileStore(profiles)

	rec := httptest.NewRecorder()
	botTestRouter(handler).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/groups?profile=a", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response consoleGroupsResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	var shared []consoleGroupSharedBot
	for _, group := range response.Groups {
		if group.GroupID == "10001" {
			shared = group.SharedWith
		}
	}
	if len(shared) != 1 || shared[0].BotProfileID != "b" {
		t.Fatalf("同连接共管的机器人 = %#v，应当只有分身", shared)
	}

	// 分身在这个群被关掉之后就不算共管了。
	if _, err := groups.SaveGroupConfig(assistant.GroupConfig{BotProfileID: "b", GroupID: "10001", Enabled: false, EnabledSet: true}, reuse); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	botTestRouter(handler).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/groups?profile=a", nil))
	// 解码到新变量：json 解码切片会复用底层数组，缺字段的元素会留着上一次的值。
	var after consoleGroupsResponse
	if err := json.NewDecoder(rec.Body).Decode(&after); err != nil {
		t.Fatal(err)
	}
	for _, group := range after.Groups {
		if group.GroupID == "10001" && len(group.SharedWith) != 0 {
			t.Fatalf("关掉之后还算共管：%#v", group.SharedWith)
		}
	}
}

// 群等级门槛是所有群的默认，入口在群管理，落点仍是机器人的回复门槛。
func TestConsoleGroupSwitchesGroupLevelDefaults(t *testing.T) {
	handler, _, profiles := switchesTestRouter(t)

	if rec := postSwitches(t, handler, `{"bot_profile_id":"a","min_group_level":3,"level_unknown_policy":"deny"}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	gate := profiles.Profiles().Profiles[0].ReplyGate
	if gate == nil || gate.MinGroupLevel != 3 || gate.LevelUnknownPolicy != assistant.LevelUnknownDeny {
		t.Fatalf("群等级默认没写进回复门槛：%#v", gate)
	}

	// 只改一项时不该把另一项冲掉。
	if rec := postSwitches(t, handler, `{"bot_profile_id":"a","min_group_level":0}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	gate = profiles.Profiles().Profiles[0].ReplyGate
	if gate == nil || gate.MinGroupLevel != 0 || gate.LevelUnknownPolicy != assistant.LevelUnknownDeny {
		t.Fatalf("只改门槛却动了另一项：%#v", gate)
	}
}

// 非法的等级策略直接忽略，不能把它写进配置。
func TestConsoleGroupSwitchesIgnoresUnknownLevelPolicy(t *testing.T) {
	handler, _, profiles := switchesTestRouter(t)

	if rec := postSwitches(t, handler, `{"bot_profile_id":"a","level_unknown_policy":"maybe"}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	if gate := profiles.Profiles().Profiles[0].ReplyGate; gate != nil && gate.LevelUnknownPolicy == "maybe" {
		t.Fatalf("非法策略被写进了配置：%#v", gate)
	}
}
