// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
)

// TestHandleEventDropsDisabledProfile 固定入站兜底：共享一条连接的档案里只要还有
// 一个启用着，连接就不会断，停用档案的事件照样能从那条连接进来——这里必须丢掉，
// 否则「已停用」的机器人还在记聊天记录、还在回消息。
func TestHandleEventDropsDisabledProfile(t *testing.T) {
	runtime := NewRuntime(BotConfig{ID: "on", OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetProfiles(ProfileSet{
		Profiles: []BotConfig{
			{ID: "on", Enabled: true},
			{ID: "off", Enabled: false},
		},
	})

	if !runtime.profileDisabled("off") {
		t.Fatal("停用档案必须被认出来")
	}
	for _, id := range []string{"on", "", "unknown"} {
		if runtime.profileDisabled(id) {
			t.Fatalf("profileDisabled(%q) = true", id)
		}
	}

	// 群成员缓存是入站流程最早的一站：停用的事件连这一步都不该走到。
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "off", GroupID: "1", UserID: "2", SenderRole: "member", SenderLevel: 3, RawMessage: "在吗"}
	if err := runtime.HandleEvent(context.Background(), event); err != nil {
		t.Fatalf("HandleEvent() error = %v", err)
	}
	if _, ok := runtime.members.lookup("1", "2"); ok {
		t.Fatal("停用档案的事件不该进入入站流程")
	}

	// 同一条消息换成启用中的档案就该照常处理，证明拦的是停用状态本身。
	event.ProfileID = "on"
	if err := runtime.HandleEvent(context.Background(), event); err != nil {
		t.Fatalf("HandleEvent() error = %v", err)
	}
	if _, ok := runtime.members.lookup("1", "2"); !ok {
		t.Fatal("启用中的档案不该被拦")
	}
}
