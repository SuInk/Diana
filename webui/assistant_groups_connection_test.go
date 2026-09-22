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

// connectionTestHandler 起一条连接上的三台机器人：a 和 b 复用同一条连接，c 自己一条。
func connectionTestHandler(t *testing.T, newGroupEnabled bool) (*BotHandler, *MemoryBotGroupConfigStore, []assistant.BotConfig) {
	t.Helper()
	mode := assistant.GroupAdmissionWhitelist
	if newGroupEnabled {
		mode = assistant.GroupAdmissionBlacklist
	}
	base := assistant.DefaultBotConfig()
	base.ID, base.Name, base.Enabled = "a", "主号", true
	// 复用连接要求来源那台配了反向 WS 的 Access Token，否则整份档案存不下去。
	base.OneBotAccessToken = "token"
	base.GroupAdmission = assistant.GroupAdmission{Mode: mode}.WithDefaults()
	reuse := base
	reuse.ID, reuse.Name, reuse.ConnectionProfileID = "b", "分身", "a"
	apart := base
	apart.ID, apart.Name, apart.ConnectionProfileID = "c", "另一条连接", ""

	runtime := assistant.NewRuntime(base, consoleGroupListChannel{result: map[string]any{"items": []any{}}}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	profiles := NewMemoryBotProfileStoreFromSet(assistant.ProfileSet{Profiles: []assistant.BotConfig{base, reuse, apart}})
	groups := NewMemoryBotGroupConfigStore()
	groups.SetProfileSource(profiles)
	handler := NewBotHandler(context.Background(), runtime)
	handler.SetGroupConfigStore(groups)
	handler.SetProfileStore(profiles)
	return handler, groups, []assistant.BotConfig{base, reuse, apart}
}

// TestConnectionPeersExposeGroupOwnership 一条连接上「哪个群归谁」要能一次看全：
// 这张路由表散在每台自己的白名单里，单台的配置页永远显示正常。
func TestConnectionPeersExposeGroupOwnership(t *testing.T) {
	handler, groups, profiles := connectionTestHandler(t, false)
	reuse, apart := profiles[1], profiles[2]
	for _, seed := range []struct {
		profile assistant.BotConfig
		groupID string
		enabled bool
	}{
		{reuse, "10001", true},
		{reuse, "10002", false},
		{apart, "10003", true},
	} {
		if _, err := groups.SaveGroupConfig(assistant.GroupConfig{BotProfileID: seed.profile.ID, GroupID: seed.groupID, Enabled: seed.enabled, EnabledSet: true}, seed.profile); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptest.NewRecorder()
	botTestRouter(handler).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/groups?profile=a", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response consoleGroupsResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.ConnectionPeers) != 1 {
		t.Fatalf("同连接机器人 = %#v，别的连接不该混进来", response.ConnectionPeers)
	}
	peer := response.ConnectionPeers[0]
	if peer.BotProfileID != "b" || peer.Name != "分身" || !peer.Enabled || peer.NewGroupEnabled {
		t.Fatalf("同连接机器人 = %#v", peer)
	}
	// 只列点过头的群：关掉的那个不算，新群默认是另一回事，界面上分开说。
	if strings.Join(peer.EnabledGroups, ",") != "10001" {
		t.Fatalf("准入群 = %v", peer.EnabledGroups)
	}
}

// TestGroupSaveWarnsAboutConnectionConflict 打开一个别人也开着的群时，保存照常完成，
// 但必须当场说清楚是哪个群、和哪台冲突。
func TestGroupSaveWarnsAboutConnectionConflict(t *testing.T) {
	handler, groups, profiles := connectionTestHandler(t, false)
	reuse := profiles[1]
	if _, err := groups.SaveGroupConfig(assistant.GroupConfig{BotProfileID: reuse.ID, GroupID: "10001", Enabled: true, EnabledSet: true}, reuse); err != nil {
		t.Fatal(err)
	}

	body := `{"config":{"bot_profile_id":"a","group_id":"10001","enabled":true,"enabled_set":true}}`
	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/assistant/groups", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	botTestRouter(handler).ServeHTTP(rec, request)
	if rec.Code != http.StatusOK {
		t.Fatalf("冲突不该阻断保存：%d %s", rec.Code, rec.Body.String())
	}
	var saved struct {
		Warning string `json:"warning"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&saved); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"10001", "分身"} {
		if !strings.Contains(saved.Warning, want) {
			t.Fatalf("冲突提示缺少 %q：%s", want, saved.Warning)
		}
	}
	// 配置确实落盘了：提示是告警，不是拒绝。
	if cfg, ok := groups.ConfigForGroup("a", "10001"); !ok || !cfg.Enabled {
		t.Fatalf("保存没生效：%#v %v", cfg, ok)
	}

	// 关掉这个群时不该再提冲突：这一步正是在解决冲突。
	off := `{"config":{"bot_profile_id":"a","group_id":"10001","enabled":false,"enabled_set":true}}`
	rec = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/assistant/groups", strings.NewReader(off))
	request.Header.Set("Content-Type", "application/json")
	botTestRouter(handler).ServeHTTP(rec, request)
	var closed struct {
		Warning string `json:"warning"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&closed); err != nil {
		t.Fatal(err)
	}
	if closed.Warning != "" {
		t.Fatalf("停用这个群还在报冲突：%s", closed.Warning)
	}
}

// TestGroupSwitchesWarnAboutAllGroupsDefault 同一条连接上几台都是「新群默认工作」，
// 等于每台都收所有群。这件事在任何单个群的配置页上都看不出来，只能在这里说。
func TestGroupSwitchesWarnAboutAllGroupsDefault(t *testing.T) {
	handler, _, _ := connectionTestHandler(t, true)

	rec := postSwitches(t, handler, `{"bot_profile_id":"a","new_group_enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Warning string `json:"warning"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"分身", "新群默认改成不工作"} {
		if !strings.Contains(response.Warning, want) {
			t.Fatalf("提示缺少 %q：%s", want, response.Warning)
		}
	}
	if strings.Contains(response.Warning, "另一条连接") {
		t.Fatalf("别的连接被算进来了：%s", response.Warning)
	}

	// 改成默认不工作是在解决问题，不该再提示。
	rec = postSwitches(t, handler, `{"bot_profile_id":"a","new_group_enabled":false}`)
	var off struct {
		Warning string `json:"warning"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&off); err != nil {
		t.Fatal(err)
	}
	if off.Warning != "" {
		t.Fatalf("关掉默认还在提示：%s", off.Warning)
	}
}
