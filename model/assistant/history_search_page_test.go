package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
)

func decodeHistoryPage(t *testing.T, raw string) dianaChatHistoryResult {
	t.Helper()
	var result dianaChatHistoryResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// 条目一律给原文：50 条长消息超出单次上限时不缩短任何一条，装不下的整条留给下一页，
// 游标按实际返回条数推进，message 写明本页覆盖哪段、怎么接着读。
func TestHistoryPageKeepsWholeItemsAndDefersRest(t *testing.T) {
	result := dianaChatHistoryResult{OK: true, Action: "search", Total: 100, searchPage: &historySearchCursor{Version: 1, Scope: historySearchScope("s", "q", "current", "oldest"), Through: 100, Offset: 5}}
	text := strings.Repeat("内容", 300)
	description := strings.Repeat("图片", 200)
	for i := 0; i < 50; i++ {
		result.Items = append(result.Items, dianaChatHistoryItem{MessageID: fmt.Sprint(i), LocalTime: fmt.Sprintf("2026-11-13 23:%02d:00 +08:00", 59-i), Sender: "用户", Text: text, ImageDescriptions: []string{description}})
	}
	raw, err := marshalDianaChatHistoryResult(context.Background(), result)
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(raw)) > agent.ToolOutputMaxChars {
		t.Fatalf("page exceeds limit: %d", len([]rune(raw)))
	}
	got := decodeHistoryPage(t, raw)
	if got.ReturnedCount < 1 || got.ReturnedCount >= 50 || !got.HasMore || got.SearchComplete {
		t.Fatalf("invalid paging: %+v", got)
	}
	for _, item := range got.Items {
		if item.Text != text || len(item.ImageDescriptions) != 1 || item.ImageDescriptions[0] != description {
			t.Fatalf("item content was shortened: %+v", item)
		}
	}
	cursor, err := decodeHistorySearchCursor(got.NextCursor)
	if err != nil || cursor.Offset != 5+got.ReturnedCount || got.RemainingCount != 100-cursor.Offset {
		t.Fatalf("cursor skipped evidence: %+v %v", cursor, err)
	}
	if got.PageWindow != "2026-11-13 23:59:00 +08:00 ~ "+got.Items[len(got.Items)-1].LocalTime || !strings.Contains(got.Message, "next_cursor") {
		t.Fatalf("page window or continuation missing: %q %q", got.PageWindow, got.Message)
	}
}

// 上限内的结果原样给出，没有分页说明。
func TestHistoryPageFitsWithoutNotice(t *testing.T) {
	result := dianaChatHistoryResult{OK: true, Action: "search", Total: 18, searchPage: &historySearchCursor{Version: 1, Scope: historySearchScope("s", "快递", "current", "oldest"), Through: 100}}
	for i := 0; i < 18; i++ {
		result.Items = append(result.Items, dianaChatHistoryItem{MessageID: fmt.Sprint(i), Sender: "用户", Text: strings.Repeat("快递内容", 100)})
	}
	raw, err := marshalDianaChatHistoryResult(context.Background(), result)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeHistoryPage(t, raw)
	if got.ReturnedCount != 18 || !got.SearchComplete || got.HasMore || got.PageWindow != "" || strings.Contains(got.Message, "单次结果上限") {
		t.Fatalf("bad counts: %+v", got)
	}
}

// 不分页的操作装不下时列出没给的消息，让模型用 ids 接着读。
func TestHistoryPageListsUnreturnedIDsForUnpagedResults(t *testing.T) {
	result := dianaChatHistoryResult{OK: true, Action: "recent", Total: 60}
	for i := 0; i < 60; i++ {
		result.Items = append(result.Items, dianaChatHistoryItem{MessageID: fmt.Sprint(i), Sender: "用户", Text: strings.Repeat("正文", 300)})
	}
	got := decodeHistoryPage(t, mustMarshalHistory(t, result))
	if len(got.UnreturnedIDs) == 0 || got.ReturnedCount+len(got.UnreturnedIDs) != 60 || got.UnreturnedIDs[0] != fmt.Sprint(got.ReturnedCount) || !strings.Contains(got.Message, "unreturned_message_ids") {
		t.Fatalf("unreturned ids missing: returned=%d unreturned=%v", got.ReturnedCount, got.UnreturnedIDs)
	}
}

func mustMarshalHistory(t *testing.T, result dianaChatHistoryResult) string {
	t.Helper()
	raw, err := marshalDianaChatHistoryResult(context.Background(), result)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestHistorySearchOldestCursorResumesAndRejectsDifferentScope(t *testing.T) {
	r := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetMessageHistoryStore(newSemanticTimelineStore())
	base := time.Now().Add(-7 * 24 * time.Hour).Unix()
	for i := 0; i < 5; i++ {
		r.remember(chatHistoryTextEvent(base, "alice", "Alice", fmt.Sprintf("id-%d", i), "快递待取件"))
	}
	tool := newDianaChatHistoryTool(r, MessageEvent{Kind: EventKindGroup, GroupID: "group-1", Time: time.Now().Unix()})
	input := map[string]any{"operation": "search", "query": "快递", "order": "oldest", "limit": 2}
	seen := map[string]bool{}
	for page := 0; page < 3; page++ {
		raw, err := tool.Run(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		got := decodeHistoryPage(t, raw)
		for _, item := range got.Items {
			if seen[item.MessageID] {
				t.Fatal("duplicate cursor row")
			}
			seen[item.MessageID] = true
		}
		if got.SearchComplete != (page == 2) {
			t.Fatalf("incorrect completeness: %+v", got)
		}
		if page == 0 {
			bad := map[string]any{"operation": "search", "query": "别的", "order": "oldest", "cursor": got.NextCursor}
			if _, err := tool.Run(context.Background(), bad); err == nil {
				t.Fatal("accepted cursor for another query")
			}
			other := newDianaChatHistoryTool(r, MessageEvent{Kind: EventKindGroup, GroupID: "different"})
			bad["query"] = "快递"
			if _, err := other.Run(context.Background(), bad); err == nil {
				t.Fatal("accepted foreign-session cursor")
			}
		}
		input["cursor"] = got.NextCursor
	}
	if len(seen) != 5 {
		t.Fatalf("lost rows: %v", seen)
	}
}

func TestHistoryRangeCursorKeepsSameSecondMessages(t *testing.T) {
	r := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetMessageHistoryStore(newSemanticTimelineStore())
	for i := 0; i < 3; i++ {
		r.remember(chatHistoryTextEvent(100, "alice", "Alice", fmt.Sprint(i), "内容"))
	}
	tool := newDianaChatHistoryTool(r, MessageEvent{Kind: EventKindGroup, GroupID: "group-1", Time: 200})
	raw, err := tool.Run(context.Background(), map[string]any{"operation": "range", "all_time": true, "limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	first := decodeHistoryPage(t, raw)
	if first.NextCursor == "" || first.NextFromTime != 0 {
		t.Fatalf("unsafe timestamp continuation: %+v", first)
	}
	raw, err = tool.Run(context.Background(), map[string]any{"operation": "range", "cursor": first.NextCursor, "limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	second := decodeHistoryPage(t, raw)
	if len(second.Items) != 1 || !second.SearchComplete || second.Items[0].MessageID == first.Items[1].MessageID {
		t.Fatalf("lost same-second remainder: %+v", second)
	}
}

func TestHistoryAroundCrossGroupRequiresOptInAndUsesSourceSession(t *testing.T) {
	cfg := BotConfig{OwnerID: "owner", CrossGroupMemoryEnabled: boolPointer(true)}
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetMessageHistoryStore(newSemanticTimelineStore())
	r.remember(chatHistoryTextEvent(100, "alice", "Alice", "source", "old evidence"))
	event := MessageEvent{Kind: EventKindGroup, GroupID: "another-group", UserID: "owner", Time: 200}
	tool := newDianaChatHistoryTool(r, event)
	input := map[string]any{"operation": "around", "group_id": "group-1", "message_id": "source"}
	raw, err := tool.Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeHistoryPage(t, raw)
	if len(got.Items) != 1 || got.Items[0].Text != "old evidence" {
		t.Fatalf("wrong source session: %+v", got)
	}
	r2 := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if _, err := newDianaChatHistoryTool(r2, event).Run(context.Background(), input); err == nil {
		t.Fatal("cross-group expansion ignored opt-in")
	}
}

func TestHistoryBudgetNeverDropsRequestedAroundAnchor(t *testing.T) {
	r := dianaChatHistoryResult{OK: true, Action: "around", AnchorMessageID: "anchor", Total: 100}
	for i := 0; i < 100; i++ {
		r.Items = append(r.Items, dianaChatHistoryItem{MessageID: fmt.Sprint(i), Sender: strings.Repeat("名", 100), Text: strings.Repeat("正文", 200)})
	}
	r.Items[70].MessageID = "anchor"
	got := decodeHistoryPage(t, mustMarshalHistory(t, r))
	for _, item := range got.Items {
		if item.MessageID == "anchor" {
			return
		}
	}
	t.Fatal("around anchor was clipped away")
}
