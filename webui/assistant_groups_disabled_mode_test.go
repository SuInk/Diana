// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

// TestConsoleGroupSwitchesSaveDisabledMode 停用档位是机器人级的群默认：在群管理顶部
// 改，存进机器人配置，切「新群默认」时不能把它冲掉，认不出的值不改动。
func TestConsoleGroupSwitchesSaveDisabledMode(t *testing.T) {
	handler, _, profiles := switchesTestRouter(t)
	mode := func() assistant.GroupDisabledMode {
		t.Helper()
		cfg, ok := profiles.Profiles().ConfigForProfile("a")
		if !ok {
			t.Fatal("profile a missing")
		}
		return cfg.GroupAdmission.DisabledMode
	}
	post := func(body string) {
		t.Helper()
		if rec := postSwitches(t, handler, body); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}

	if got := mode(); got != "" {
		t.Fatalf("默认档位 = %q，应当是彻底关闭（空值）", got)
	}
	post(`{"bot_profile_id":"a","disabled_mode":"observe"}`)
	if got := mode(); got != assistant.GroupDisabledObserve {
		t.Fatalf("disabled_mode = %q, want observe", got)
	}
	post(`{"bot_profile_id":"a","new_group_enabled":false}`)
	if got := mode(); got != assistant.GroupDisabledObserve {
		t.Fatalf("切新群默认把档位冲成了 %q", got)
	}
	post(`{"bot_profile_id":"a","disabled_mode":"whatever"}`)
	if got := mode(); got != assistant.GroupDisabledObserve {
		t.Fatalf("认不出的值改动了档位：%q", got)
	}
	post(`{"bot_profile_id":"a","disabled_mode":"dormant"}`)
	if got := mode(); got.Observes() {
		t.Fatalf("选回彻底关闭后 disabled_mode = %q", got)
	}
}

// TestConsoleGroupDisabledModeOverride 单个群可以覆盖机器人的「群停用后」：存得下、
// 列表读得回，清空就回到跟随机器人；列表开关来回拨不丢覆盖。
func TestConsoleGroupDisabledModeOverride(t *testing.T) {
	handler, groups, _ := switchesTestRouter(t)
	router := botTestRouter(handler)
	save := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/assistant/groups", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}
	override := func() assistant.GroupDisabledMode {
		t.Helper()
		cfg, ok := groups.ConfigForGroup("a", "50006")
		if !ok {
			t.Fatal("group config not saved")
		}
		return cfg.DisabledMode
	}

	save(`{"config":{"bot_profile_id":"a","group_id":"50006","enabled":false,"enabled_set":true,"disabled_mode":"observe"}}`)
	if got := override(); got != assistant.GroupDisabledObserve {
		t.Fatalf("disabled_mode = %q, want observe", got)
	}
	if rec := postSwitches(t, handler, `{"bot_profile_id":"a","group_ids":["50006"],"enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec := postSwitches(t, handler, `{"bot_profile_id":"a","group_ids":["50006"],"enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := override(); got != assistant.GroupDisabledObserve {
		t.Fatalf("列表开关来回拨后覆盖变成了 %q", got)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/groups?profile=a", nil))
	if !strings.Contains(rec.Body.String(), `"disabled_mode":"observe"`) {
		t.Fatalf("群列表没带上覆盖：%s", rec.Body.String())
	}
	save(`{"config":{"bot_profile_id":"a","group_id":"50006","enabled":false,"enabled_set":true,"disabled_mode":"dormant"}}`)
	if got := override(); got != assistant.GroupDisabledDormant {
		t.Fatalf("显式彻底关闭应当留着：%q", got)
	}
	save(`{"config":{"bot_profile_id":"a","group_id":"50006","enabled":false,"enabled_set":true,"disabled_mode":""}}`)
	if got := override(); got != "" {
		t.Fatalf("清空后应当跟随机器人：%q", got)
	}
}
