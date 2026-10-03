package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestAdminGroupHistoryScopePaginationAndLiteralSearch(t *testing.T) {
	ctx := context.Background()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fixtures := []struct {
		profile, group, id, text string
		at                       int64
		kind                     assistant.EventKind
		outbound                 bool
	}{
		{"bot_a", "same", "first", "MCP 100%_ready", 10, assistant.EventKindGroup, false},
		{"bot_a", "same", "reply", "MCP 100%_ready 回复", 20, assistant.EventKindGroup, true},
		{"bot_a", "same", "tie", "MCP 100%_ready 继续", 20, assistant.EventKindGroup, false},
		{"bot_a", "other", "another-group", "MCP", 30, assistant.EventKindGroup, false},
		{"botXa", "same", "foreign", "MCP 100%_ready", 30, assistant.EventKindGroup, false},
		{"", "same", "unknown", "MCP", 40, assistant.EventKindGroup, false},
		{"bot_a", "same", "private", "MCP 100%_ready", 40, assistant.EventKindPrivate, false},
		{"bot_a", "same", "notice", "MCP 100%_ready", 40, assistant.EventKindNotice, false},
		{"bot_a", "same", "wildcards", "MCP 100XXready", 40, assistant.EventKindGroup, false},
		{"bot_a", "same", "outside", "MCP 100%_ready", 101, assistant.EventKindGroup, false},
	}
	for _, f := range fixtures {
		e := assistant.MessageEvent{Kind: f.kind, ProfileID: f.profile, ContextNamespace: f.profile, Platform: assistant.PlatformOneBotV11, GroupID: f.group, GroupName: "测试群", MessageID: f.id, Time: f.at, UserID: "123", SenderName: "Alice", RawMessage: f.text, Outbound: f.outbound}
		if err := s.AppendMessageEvent(ctx, fmt.Sprintf("%s:%s:%s", f.profile, f.kind, f.group), e); err != nil {
			t.Fatal(err)
		}
	}
	for _, oldest := range []bool{true, false} {
		q := AdminGroupHistoryQuery{ProfileID: "bot_a", GroupID: "same", Search: "mcp 100%_ready", ThroughTime: 100, Limit: 1, OldestFirst: oldest}
		seen := map[string]bool{}
		var previous int64
		for page := 0; page < 3; page++ {
			items, total, err := s.SearchAdminGroupHistory(ctx, q)
			if err != nil || total != 3 || len(items) != 1 {
				t.Fatalf("items=%+v total=%d err=%v", items, total, err)
			}
			item := items[0]
			if seen[item.MessageID] || item.ProfileID != "bot_a" || item.GroupID != "same" || item.GroupName != "测试群" {
				t.Fatalf("wrong scope or duplicate: %+v", item)
			}
			if page > 0 && (oldest && item.Time < previous || !oldest && item.Time > previous) {
				t.Fatal("wrong chronological order")
			}
			if item.MessageID == "reply" && !item.Outbound {
				t.Fatal("outgoing reply lost")
			}
			seen[item.MessageID] = true
			previous = item.Time
			q.Offset++
		}
		items, total, err := s.SearchAdminGroupHistory(ctx, q)
		if err != nil || total != 3 || len(items) != 0 {
			t.Fatal("end page changed total")
		}
	}
	// No keyword reads recent messages; all-bot mode preserves unknown origins.
	items, total, err := s.SearchAdminGroupHistory(ctx, AdminGroupHistoryQuery{GroupID: "same", ThroughTime: 100, Limit: 1000})
	if err != nil || total != 6 || len(items) != 6 {
		t.Fatalf("all: %+v total=%d err=%v", items, total, err)
	}
	if items[0].ProfileID != "" && items[1].ProfileID != "" {
		t.Fatal("unknown source hidden")
	}
	if _, _, err := s.SearchAdminGroupHistory(ctx, AdminGroupHistoryQuery{FromTime: 20, ThroughTime: 10}); err == nil {
		t.Fatal("invalid range accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := s.SearchAdminGroupHistory(cancelled, AdminGroupHistoryQuery{ThroughTime: 100}); err == nil {
		t.Fatal("cancelled search continued")
	}
}

func TestAdminGroupHistorySearchesImageDescriptionAndHandlesLegacyNulls(t *testing.T) {
	ctx := context.Background()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "images.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := assistant.MessageEvent{Kind: assistant.EventKindGroup, ProfileID: "bot", GroupID: "group", MessageID: "image", Time: 20}
	if err := s.AppendMessageEvent(ctx, "bot:group:group", e); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveMessageSearchExtra(ctx, "bot:group:group", "image", "屏幕显示 MCP 连接失败"); err != nil {
		t.Fatal(err)
	}
	items, total, err := s.SearchAdminGroupHistory(ctx, AdminGroupHistoryQuery{ProfileID: "bot", Search: "MCP", ThroughTime: 100})
	if err != nil || total != 1 || len(items) != 1 || items[0].SearchExtra != "屏幕显示 MCP 连接失败" {
		t.Fatalf("items=%+v total=%d err=%v", items, total, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE message_events SET profile_id=NULL,user_id=NULL,message_id=NULL,sender_name=NULL,text=NULL`); err != nil {
		t.Fatal(err)
	}
	items, total, err = s.SearchAdminGroupHistory(ctx, AdminGroupHistoryQuery{ThroughTime: 100})
	if err != nil || total != 1 || items[0].ProfileID != "" {
		t.Fatalf("legacy nulls: %+v total=%d err=%v", items, total, err)
	}
}
