// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// 查好感度时默认带上最近几条变更。数据层一直是这么做的（defaultRelationshipHistoryLimit
// = 5），但回复指引原先写的是「只有用户问最近变化时才讲」——查是查了，模型被要求
// 闭嘴。于是聊天里问好感度永远看不到变化，看起来像没查。这两条测试分别钉数据和指引。

func seedRecentChangesRuntime(t *testing.T, count int) (*Runtime, MessageEvent) {
	t.Helper()
	memory := newMemoryUserMemoryStore()
	memory.profiles["10005"] = UserMemoryProfile{UserID: "10005", DisplayName: "Alice", Favorability: 12, MessageCount: 30}
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	// 最新的排最前，和 SQLite 那边 id DESC 的顺序一致。
	for i := 0; i < count; i++ {
		memory.favorabilityChanges["10005"] = append(memory.favorabilityChanges["10005"], UserFavorabilityChange{
			ID: int64(count - i), UserID: "10005", Delta: 1, Before: 11, After: 12,
			Source: "interaction", Reason: "聊得挺开心", CreatedAt: now.Add(-time.Duration(i) * time.Hour),
		})
	}
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_group_member_info": {"group_id": 20002, "user_id": 10005, "nickname": "Alice", "role": "member"},
	}}
	runtime := NewRuntime(BotConfig{OwnerID: "10001", BotAccount: "10000"}, channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetUserMemoryStore(memory)
	event := MessageEvent{
		Kind: EventKindGroup, SelfID: "10000", UserID: "10001", GroupID: "20002",
		Segments: []MessageSegment{
			{Type: "text", Data: map[string]string{"text": "Diana看下"}},
			{Type: "at", Data: map[string]string{"qq": "10005"}},
			{Type: "text", Data: map[string]string{"text": " 的好感度"}},
		},
	}
	return runtime, event
}

// TestRelationshipGetCarriesFiveRecentChangesByDefault 不传 history_limit 也要带上最近 5 条。
func TestRelationshipGetCarriesFiveRecentChangesByDefault(t *testing.T) {
	runtime, event := seedRecentChangesRuntime(t, 7)
	raw, err := newDianaRelationshipTool(runtime, event).Run(context.Background(), map[string]any{"operation": "get"})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaRelationshipResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Target == nil {
		t.Fatalf("没有目标用户：%s", raw)
	}
	if got := len(result.Target.RecentChanges); got != defaultRelationshipHistoryLimit {
		t.Fatalf("默认带了 %d 条变更，应为 %d 条", got, defaultRelationshipHistoryLimit)
	}
	// 带的必须是最新的那几条，不是最旧的。
	if result.Target.RecentChanges[0].ID != 7 || result.Target.RecentChanges[4].ID != 3 {
		t.Fatalf("带的不是最新 5 条：ids=%v", func() []int64 {
			ids := make([]int64, 0, 5)
			for _, c := range result.Target.RecentChanges {
				ids = append(ids, c.ID)
			}
			return ids
		}())
	}
}

// TestRelationshipGuidanceMentionsRecentChangesByDefault 回复指引要让模型默认说，而不是压下去。
//
// 这条和上一条缺一不可：数据带了、指引却让模型闭嘴，用户看到的仍然是「没查」。
func TestRelationshipGuidanceMentionsRecentChangesByDefault(t *testing.T) {
	runtime, event := seedRecentChangesRuntime(t, 3)
	raw, err := newDianaRelationshipTool(runtime, event).Run(context.Background(), map[string]any{"operation": "get"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "只有用户问最近变化时才讲") {
		t.Fatal("回复指引还在让模型只在被问到时才提最近变化")
	}
	for _, want := range []string{"默认顺带说一下", "不要逐条列成清单", "没有记录就一个字都不提"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("回复指引缺少：%s\n%s", want, raw)
		}
	}
}
