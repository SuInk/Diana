// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

type groupInfoStubRuntime struct {
	BotRuntime
	mu    sync.Mutex
	calls int
	name  string
	found bool
}

func (r *groupInfoStubRuntime) GroupInfoForProfile(_ context.Context, _, groupID string) (assistant.GroupInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if !r.found {
		return assistant.GroupInfo{}, false
	}
	return assistant.GroupInfo{GroupID: groupID, GroupName: r.name}, true
}

// newGroupNameHandler 造一个只登记了一台 Telegram 机器人的 handler。
// isOneBotProfile 在找不到配置档时按 OneBot 处理，所以这里必须真的登记进去，
// 否则查询会被当成 OneBot 部署直接跳过。
func newGroupNameHandler(runtime BotRuntime) *BotHandler {
	base := assistant.DefaultBotConfig()
	base.ID = "tg-profile"
	base.Platform = "telegram"
	store := NewMemoryBotProfileStore(base)
	_ = store.SaveProfiles(assistant.ProfileSet{
		ActiveID: "tg-profile",
		Profiles: []assistant.BotConfig{base},
	})
	return &BotHandler{
		runtime:        runtime,
		profiles:       store,
		groupNameCache: map[string]groupNameCacheEntry{},
	}
}

// 事件里记的是「上次见到这个群时叫什么」：升级前的消息根本没记名字，改过名的群
// 也要等下一条消息才更新。能问到当前名字就该用当前的。
func TestResolveGroupNamePrefersLiveName(t *testing.T) {
	runtime := &groupInfoStubRuntime{name: "读书会（新）", found: true}
	handler := newGroupNameHandler(runtime)
	if got := handler.resolveGroupName(context.Background(), "tg-profile", "-1001", "读书会（旧）"); got != "读书会（新）" {
		t.Fatalf("name = %q", got)
	}
}

// 机器人已退群、平台报错或不支持查询时，退回事件里记下的名字，而不是显示空白。
func TestResolveGroupNameFallsBackWhenLookupFails(t *testing.T) {
	runtime := &groupInfoStubRuntime{found: false}
	handler := newGroupNameHandler(runtime)
	if got := handler.resolveGroupName(context.Background(), "tg-profile", "-1001", "上次见到的名字"); got != "上次见到的名字" {
		t.Fatalf("name = %q", got)
	}
}

// 群管理页一次列几十个群，同一个群不该每次刷新都打一遍平台接口；查不到的也要缓存，
// 否则每次刷新都会为同一个群重试。
func TestResolveGroupNameCachesLookups(t *testing.T) {
	runtime := &groupInfoStubRuntime{name: "读书会", found: true}
	handler := newGroupNameHandler(runtime)
	for range 3 {
		handler.resolveGroupName(context.Background(), "tg-profile", "-1001", "")
	}
	missing := &groupInfoStubRuntime{found: false}
	missingHandler := newGroupNameHandler(missing)
	for range 3 {
		missingHandler.resolveGroupName(context.Background(), "tg-profile", "-1002", "旧名字")
	}
	if runtime.calls != 1 {
		t.Fatalf("resolved name was fetched %d times, want 1", runtime.calls)
	}
	if missing.calls != 1 {
		t.Fatalf("missing name was fetched %d times, want 1", missing.calls)
	}
}

// OneBot 侧有 get_group_list 那条权威路径，不该再为每个群多打一次查询。
func TestResolveGroupNameSkipsOneBotProfiles(t *testing.T) {
	runtime := &groupInfoStubRuntime{name: "不该用到", found: true}
	onebot := assistant.DefaultBotConfig()
	onebot.ID = "qq-profile"
	onebot.Platform = "onebot-v11"
	telegram := assistant.DefaultBotConfig()
	telegram.ID = "tg-profile"
	telegram.Platform = "telegram"
	store := NewMemoryBotProfileStore(onebot)
	_ = store.SaveProfiles(assistant.ProfileSet{
		ActiveID: "qq-profile",
		Profiles: []assistant.BotConfig{onebot, telegram},
	})
	handler := &BotHandler{
		runtime:        runtime,
		profiles:       store,
		groupNameCache: map[string]groupNameCacheEntry{},
	}
	if got := handler.resolveGroupName(context.Background(), "qq-profile", "111", "QQ 群"); got != "QQ 群" {
		t.Fatalf("name = %q", got)
	}
	// 混合部署里认不出归属的事件仍按 OneBot 处理，同样不该触发查询。
	if got := handler.resolveGroupName(context.Background(), "", "222", "老 QQ 群"); got != "老 QQ 群" {
		t.Fatalf("legacy name = %q", got)
	}
	if runtime.calls != 0 {
		t.Fatalf("onebot profile triggered %d lookups", runtime.calls)
	}
}
