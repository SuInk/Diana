// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func groupListTestChannel() *moderationTestChannel {
	channel := newModerationTestChannel("member")
	channel.apiResponses["get_group_list"] = map[string]any{"items": []any{
		map[string]any{"group_id": float64(20001), "group_name": "摸鱼群", "member_count": float64(42)},
		map[string]any{"group_id": "20002", "group_name": "番剧讨论"},
		map[string]any{"group_name": "没有群号的脏数据"},
	}}
	return channel
}

// 主人能拿到明细；查询要带 no_cache，否则 SnowLuma 返回的是启动时的快照。
func TestPlatformToolGroupListOwnerSeesGroups(t *testing.T) {
	channel := groupListTestChannel()
	event := MessageEvent{Kind: EventKindGroup, UserID: "10001", GroupID: "20001", SelfID: "10000", Platform: PlatformOneBotV11}
	tool, _, _ := platformToolFor(t, BotConfig{OwnerID: "10001", BotAccount: "10000", Platform: PlatformOneBotV11}, channel, event)

	raw, err := tool.Run(context.Background(), map[string]any{"operation": "group_list"})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		GroupCount int                      `json:"group_count"`
		Groups     []dianaPlatformGroupItem `json:"groups"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.GroupCount != 2 || len(result.Groups) != 2 {
		t.Fatalf("result = %s", raw)
	}
	if result.Groups[0] != (dianaPlatformGroupItem{GroupID: "20001", GroupName: "摸鱼群", MemberCount: 42}) || result.Groups[1].GroupID != "20002" {
		t.Fatalf("groups = %#v", result.Groups)
	}
	calls := recordedCallsByAction(channel.callsSnapshot(), "get_group_list")
	if len(calls) != 1 || calls[0].params["no_cache"] != true {
		t.Fatalf("get_group_list calls = %#v", calls)
	}
}

// 别的群叫什么不该被随便哪个群友问出来：非主人只拿到群数。
func TestPlatformToolGroupListHidesGroupsFromNonOwner(t *testing.T) {
	channel := groupListTestChannel()
	event := MessageEvent{Kind: EventKindGroup, UserID: "10005", GroupID: "20001", SelfID: "10000", Platform: PlatformOneBotV11}
	tool, _, _ := platformToolFor(t, BotConfig{OwnerID: "10001", BotAccount: "10000", Platform: PlatformOneBotV11}, channel, event)

	raw, err := tool.Run(context.Background(), map[string]any{"operation": "group_list"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"group_count":2`) || strings.Contains(raw, "摸鱼群") || strings.Contains(raw, "20002") || strings.Contains(raw, `"groups":`) {
		t.Fatalf("non-owner result leaked group details: %s", raw)
	}
}

// Telegram Bot API 没有列出已加入群的接口，如实拒绝，不去瞎调。
func TestPlatformToolGroupListUnsupportedOnTelegram(t *testing.T) {
	channel := newModerationTestChannel("member")
	event := MessageEvent{Kind: EventKindGroup, UserID: "10001", GroupID: "-10020", SelfID: "10000", Platform: PlatformTelegram}
	tool, _, _ := platformToolFor(t, BotConfig{OwnerID: "10001", BotAccount: "10000", Platform: PlatformTelegram}, channel, event)

	if _, err := tool.Run(context.Background(), map[string]any{"operation": "group_list"}); err == nil || !strings.Contains(err.Error(), "不支持") {
		t.Fatalf("telegram group_list error = %v", err)
	}
	if calls := channel.callsSnapshot(); len(calls) != 0 {
		t.Fatalf("unsupported platform should not call the API: %#v", calls)
	}
}

// 群列表不设条数上限，按整条分页：写明 total/has_more/next_offset，带 offset 续读拿到剩下的。
func TestPlatformToolGroupListPagesWithOffset(t *testing.T) {
	channel := newModerationTestChannel("member")
	items := make([]any, 0, 250)
	for i := range 250 {
		items = append(items, map[string]any{"group_id": float64(30000 + i), "group_name": "群" + itoa(i)})
	}
	channel.apiResponses["get_group_list"] = map[string]any{"items": items}
	event := MessageEvent{Kind: EventKindGroup, UserID: "10001", GroupID: "30000", SelfID: "10000", Platform: PlatformOneBotV11}
	tool, _, _ := platformToolFor(t, BotConfig{OwnerID: "10001", BotAccount: "10000", Platform: PlatformOneBotV11}, channel, event)

	type pageResult struct {
		GroupCount int                      `json:"group_count"`
		Groups     []dianaPlatformGroupItem `json:"groups"`
		toolResultPage
	}
	decode := func(input map[string]any) pageResult {
		t.Helper()
		raw, err := tool.Run(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		var result pageResult
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	all := decode(map[string]any{"operation": "group_list"})
	if all.GroupCount != 250 || len(all.Groups) != 250 || all.Total != 250 || all.HasMore {
		t.Fatalf("超过旧上限 200 的群也要全给：count=%d groups=%d page=%+v", all.GroupCount, len(all.Groups), all.toolResultPage)
	}
	first := decode(map[string]any{"operation": "group_list", "limit": 100})
	if len(first.Groups) != 100 || first.Total != 250 || !first.HasMore || first.NextOffset != 100 || first.RemainingCount != 150 || first.PageNote == "" {
		t.Fatalf("first page = %+v", first.toolResultPage)
	}
	rest := decode(map[string]any{"operation": "group_list", "offset": first.NextOffset, "limit": 1000})
	if len(rest.Groups) != 150 || rest.HasMore || rest.Groups[0].GroupID != "30100" || rest.Groups[149].GroupID != "30249" {
		t.Fatalf("rest page = %+v first=%v", rest.toolResultPage, rest.Groups[0])
	}
}
