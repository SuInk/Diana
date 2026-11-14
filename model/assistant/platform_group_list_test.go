// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/SuInk/diana/model/agent"
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

// collectPlatformListPages 在单次上限 budget 下按 next_offset 翻完 key 下的列表，
// 检查每页都不超上限、不重复，返回全部条目的 idKey 和页数。
func collectPlatformListPages(t *testing.T, tool *dianaPlatformTool, input map[string]any, key, idKey string, budget int) (map[string]bool, int) {
	t.Helper()
	ctx := agent.WithToolOutputBudget(context.Background(), budget)
	seen := map[string]bool{}
	offset := 0
	for page := 1; ; page++ {
		if page > 1000 {
			t.Fatal("分页没有前进")
		}
		call := maps.Clone(input)
		call["offset"] = offset
		raw, err := tool.Run(ctx, call)
		if err != nil {
			t.Fatal(err)
		}
		if size := utf8.RuneCountInString(raw); size > budget {
			t.Fatalf("第 %d 页 %d 字超过上限 %d", page, size, budget)
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatal(err)
		}
		items, _ := result[key].([]any)
		if len(items) == 0 {
			t.Fatalf("第 %d 页一条都没给：%s", page, raw)
		}
		for _, item := range items {
			id, _ := item.(map[string]any)[idKey].(string)
			if seen[id] {
				t.Fatalf("重复给了 %s", id)
			}
			seen[id] = true
		}
		if result["has_more"] != true {
			if _, ok := result["next_offset"]; ok {
				t.Fatalf("最后一页不该有 next_offset：%s", raw)
			}
			return seen, page
		}
		if page == 1 && !strings.Contains(stringFromAny(result["page_note"]), "单次结果上限") {
			t.Fatalf("第一页应写明按上限切开：%v", result["page_note"])
		}
		offset = int(result["next_offset"].(float64))
	}
}

// group_list 装不下单次上限时按整条切页，续读拿齐全部群。
func TestPlatformToolGroupListSplitsByOutputBudget(t *testing.T) {
	channel := newModerationTestChannel("member")
	const total = 60
	items := make([]any, 0, total)
	for i := range total {
		items = append(items, map[string]any{"group_id": float64(30000 + i), "group_name": strings.Repeat("群名", 30) + itoa(i)})
	}
	channel.apiResponses["get_group_list"] = map[string]any{"items": items}
	event := MessageEvent{Kind: EventKindGroup, UserID: "10001", GroupID: "30000", SelfID: "10000", Platform: PlatformOneBotV11}
	tool, _, _ := platformToolFor(t, BotConfig{OwnerID: "10001", BotAccount: "10000", Platform: PlatformOneBotV11}, channel, event)
	seen, pages := collectPlatformListPages(t, tool, map[string]any{"operation": "group_list", "limit": 1000}, "groups", "group_id", 2500)
	if len(seen) != total || pages < 2 {
		t.Fatalf("拿到 %d 个群、%d 页", len(seen), pages)
	}
}

// member_list 装不下单次上限时按整条切页，续读拿齐全部成员。
func TestPlatformToolMemberListSplitsByOutputBudget(t *testing.T) {
	const total = 60
	members := make([]any, 0, total)
	for i := range total {
		members = append(members, map[string]any{"group_id": "20001", "user_id": itoa(30000 + i), "nickname": "成员", "card": strings.Repeat("名片", 30)})
	}
	channel := &recordingChannel{apiResponses: map[string]map[string]any{"get_group_member_list": {"items": members}}}
	event := MessageEvent{Kind: EventKindGroup, UserID: "10001", GroupID: "20001", SelfID: "10000", Platform: PlatformOneBotV11}
	tool, _, _ := platformToolFor(t, BotConfig{OwnerID: "10001", BotAccount: "10000", Platform: PlatformOneBotV11}, channel, event)
	seen, pages := collectPlatformListPages(t, tool, map[string]any{"operation": "member_list", "limit": 1000}, "members", "user_id", 2500)
	if len(seen) != total || pages < 2 {
		t.Fatalf("拿到 %d 人、%d 页", len(seen), pages)
	}
}

// guildPagingChannel 按 after 分页回频道列表，照开放平台一页最多 100 个。
type guildPagingChannel struct {
	*moderationTestChannel
	total  int
	afters []string
}

func (c *guildPagingChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	if action != "GET /users/@me/guilds" {
		return c.moderationTestChannel.CallAPI(ctx, action, params)
	}
	after, _ := params["after"].(string)
	c.afters = append(c.afters, after)
	start := 0
	if after != "" {
		start = intFromAny(strings.TrimPrefix(after, "G")) - 30000 + 1
	}
	var page []map[string]any
	for i := start; i < c.total && len(page) < qqOfficialGuildPageSize; i++ {
		page = append(page, map[string]any{"id": "G" + itoa(30000+i), "name": "频道" + itoa(i)})
	}
	body, _ := json.Marshal(page)
	return map[string]any{"result": json.RawMessage(body)}, nil
}

// QQ 频道列表接口一页最多 100 个，要用 after 翻完所有页，不只给第一页。
func TestQQOfficialGuildListFollowsAfterAcrossPages(t *testing.T) {
	channel := &guildPagingChannel{moderationTestChannel: newModerationTestChannel("admin"), total: 230}
	event := MessageEvent{Kind: EventKindPrivate, UserID: "owner", Platform: PlatformQQOfficial, SelfID: "bot"}
	tool, _, _ := platformToolFor(t, BotConfig{OwnerID: "owner", BotAccount: "bot", Platform: PlatformQQOfficial}, channel, event)
	raw, err := tool.Run(context.Background(), map[string]any{"operation": "group_list"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"group_count":230`) || !strings.Contains(raw, "G30229") {
		t.Fatalf("应翻完全部 230 个频道：%s", raw[:min(len(raw), 300)])
	}
	if strings.Join(channel.afters, ",") != ",G30099,G30199" {
		t.Fatalf("after 翻页参数 = %q", channel.afters)
	}
}
