// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"
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
	if got := mode(); got != "" {
		t.Fatalf("选回彻底关闭后 disabled_mode = %q", got)
	}
}
