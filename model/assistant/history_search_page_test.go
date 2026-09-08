package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func decodeHistoryPage(t *testing.T, raw string) dianaChatHistoryResult {
	t.Helper()
	var result dianaChatHistoryResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestHistoryBudgetPreserves18MatchesBeforeSurroundings(t *testing.T) {
	result := dianaChatHistoryResult{OK: true, Action: "search", Query: "快递", Total: 18, searchPage: &historySearchCursor{Version: 1, Scope: historySearchScope("s", "快递", "current", "oldest"), Through: 100}}
	for i := 0; i < 18; i++ {
		result.Items = append(result.Items, dianaChatHistoryItem{MessageID: fmt.Sprint(i), Sender: "用户", Text: strings.Repeat("快递内容", 100), ImageDescriptions: []string{strings.Repeat("图片细节", 500)}, ContextBefore: []dianaChatHistoryItem{{Text: strings.Repeat("旁支话题", 2000)}}})
	}
	raw, err := marshalDianaChatHistoryResult(result)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeHistoryPage(t, raw)
	if len([]rune(raw)) > maximumChatHistoryOutputRunes || got.ReturnedCount != 18 || got.ClippedCount != 0 || !got.Truncated || !got.SearchComplete || got.HasMore {
		t.Fatalf("bad counts: %+v", got)
	}
	if !strings.Contains(strings.Join(got.TruncationReasons, ","), "surrounding_context_removed") {
		t.Fatal("missing trimming notice")
	}
	if len(result.Items[0].ContextBefore) == 0 {
		t.Fatal("mutated source evidence")
	}
}

func TestHistoryBudgetKeepsOneMatchAndCursorUsesReturnedCount(t *testing.T) {
	result := dianaChatHistoryResult{OK: true, Action: "search", Total: 100, searchPage: &historySearchCursor{Version: 1, Scope: historySearchScope("s", "q", "current", "oldest"), Through: 100, Offset: 5}}
	for i := 0; i < 50; i++ {
		result.Items = append(result.Items, dianaChatHistoryItem{MessageID: fmt.Sprint(i), Sender: strings.Repeat("名", 60), SenderUserID: fmt.Sprint(100000 + i), Text: strings.Repeat("内容", 1000), ImageDescriptions: []string{strings.Repeat("图片", 2000)}})
	}
	raw, err := marshalDianaChatHistoryResult(result)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeHistoryPage(t, raw)
	if got.ReturnedCount < 1 || got.ReturnedCount >= 50 || got.ClippedCount != 50-got.ReturnedCount || !got.HasMore || got.SearchComplete {
		t.Fatalf("invalid clipping counts: %+v", got)
	}
	cursor, err := decodeHistorySearchCursor(got.NextCursor)
	if err != nil || cursor.Offset != 5+got.ReturnedCount || got.RemainingCount != 100-cursor.Offset {
		t.Fatalf("cursor skipped evidence: %+v %v", cursor, err)
	}
	single := result
	single.Items = result.Items[:1]
	if raw, err = marshalDianaChatHistoryResult(single); err != nil || len(decodeHistoryPage(t, raw).Items) != 1 {
		t.Fatalf("single match lost: %v", err)
	}
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
			if len(item.ContextBefore)+len(item.ContextAfter) > 0 {
				t.Fatal("search expanded context")
			}
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
	cfg := BotConfig{CrossGroupMemoryEnabled: boolPointer(true)}
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetMessageHistoryStore(newSemanticTimelineStore())
	r.remember(chatHistoryTextEvent(100, "alice", "Alice", "source", "old evidence"))
	event := MessageEvent{Kind: EventKindGroup, GroupID: "another-group", Time: 200}
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
	raw, err := marshalDianaChatHistoryResult(r)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeHistoryPage(t, raw)
	for _, item := range got.Items {
		if item.MessageID == "anchor" {
			return
		}
	}
	t.Fatal("around anchor was clipped away")
}

func TestHistoryMatchSnippetKeepsLateOCRMatch(t *testing.T) {
	text := strings.Repeat("无关地图细节", 100) + "待取件 09-02 18:52" + strings.Repeat("商品说明", 100)
	snippet := historyMatchSnippet(text, "待取件", 100)
	if !strings.Contains(snippet, "待取件 09-02 18:52") || len([]rune(snippet)) > 102 {
		t.Fatalf("lost OCR date: %s", snippet)
	}
}
