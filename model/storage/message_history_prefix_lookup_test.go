// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

// 按消息编号跨会话查找：只认同一前缀（同一机器人的群），每个会话只取一条，通知不算。
func TestFindMessageEventsBySessionPrefix(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	items := []struct {
		session string
		event   assistant.MessageEvent
	}{
		{"onebot-main:group:one", historySearchEvent(10, "one", "target", "Alice", "在一群")},
		{"onebot-main:group:one", historySearchEvent(20, "one", "target", "Alice", "一群里同编号的新记录")},
		{"onebot-main:group:two", historySearchEvent(30, "two", "target", "Bob", "在二群")},
		{"onebot-other:group:three", historySearchEvent(40, "three", "target", "Carol", "别的机器人")},
		{"onebot-main:private:9", historySearchEvent(50, "", "target", "Dan", "私聊")},
		{"onebot-main:group:four", historySearchEvent(60, "four", "other", "Eve", "别的编号")},
	}
	for _, item := range items {
		if err := store.AppendMessageEvent(ctx, item.session, item.event); err != nil {
			t.Fatal(err)
		}
	}
	notice := historySearchEvent(70, "five", "target", "Sys", "通知")
	notice.Kind = assistant.EventKindNotice
	if err := store.AppendMessageEvent(ctx, "onebot-main:group:five", notice); err != nil {
		t.Fatal(err)
	}

	got, err := store.FindMessageEventsBySessionPrefix(ctx, "onebot-main:group:", "target", 5)
	if err != nil {
		t.Fatal(err)
	}
	sessions := map[string]string{}
	for _, item := range got {
		sessions[item.Session] = item.Event.MessageID
	}
	if len(got) != 2 || sessions["onebot-main:group:one"] != "target" || sessions["onebot-main:group:two"] != "target" {
		t.Fatalf("got=%#v", got)
	}
	if got[0].Session != "onebot-main:group:two" {
		t.Fatalf("应按时间倒序，最新的会话在前：%#v", got)
	}
	if limited, _ := store.FindMessageEventsBySessionPrefix(ctx, "onebot-main:group:", "target", 1); len(limited) != 1 {
		t.Fatalf("limit 未生效：%#v", limited)
	}
	if escaped, _ := store.FindMessageEventsBySessionPrefix(ctx, "onebot_main:group:", "target", 5); len(escaped) != 0 {
		t.Fatalf("前缀里的 _ 应按字面匹配：%#v", escaped)
	}
}
