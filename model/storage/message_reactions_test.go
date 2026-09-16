// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

// Telegram 推的是完整集合（整体替换），QQ 推的是单个表情的贴上与取消（增删）。
// 两种都要落到同一张「当前挂着什么」的表里，统计才对得上。
func TestSQLiteStoreMessageReactionsReplaceAndToggle(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	const session = "bot:group:1"
	record := func(r assistant.MessageReaction) {
		t.Helper()
		r.Session = session
		if err := store.RecordMessageReaction(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	emojis := func(messageID string) map[string][]string {
		t.Helper()
		rows, err := store.ListMessageReactions(ctx, session, messageID)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][]string{}
		for _, row := range rows {
			out[row.UserID] = append(out[row.UserID], row.Emoji)
		}
		return out
	}

	// Telegram：alice 先挂 👍，再改成 ❤️ 和 🔥——旧的 👍 必须消失。
	record(assistant.MessageReaction{MessageID: "m1", UserID: "alice", UserName: "Alice", Emojis: []string{"👍"}, Replace: true, At: 100})
	record(assistant.MessageReaction{MessageID: "m1", UserID: "alice", Emojis: []string{"❤️", "🔥"}, Replace: true, At: 110})
	// QQ：bob 贴上 76 号表情，carol 贴上又取消。
	record(assistant.MessageReaction{MessageID: "m1", UserID: "bob", UserName: "Bob", Emojis: []string{"76"}, Added: true, At: 120})
	record(assistant.MessageReaction{MessageID: "m1", UserID: "carol", Emojis: []string{"76"}, Added: true, At: 130})
	record(assistant.MessageReaction{MessageID: "m1", UserID: "carol", Emojis: []string{"76"}, Added: false, At: 140})
	got := emojis("m1")
	want := map[string][]string{"alice": {"❤️", "🔥"}, "bob": {"76"}}
	for user := range want {
		if !reflect.DeepEqual(sortedStrings(got[user]), sortedStrings(want[user])) {
			t.Fatalf("m1 当前表情 = %#v，期望 %#v", got, want)
		}
	}
	if _, ok := got["carol"]; ok {
		t.Fatalf("取消的表情还挂着：%#v", got)
	}
	// 清空集合等于全部取消。
	record(assistant.MessageReaction{MessageID: "m1", UserID: "alice", Replace: true, At: 150})
	if _, ok := emojis("m1")["alice"]; ok {
		t.Fatal("Telegram 推空集合时应当清掉这个人的全部表情")
	}

	record(assistant.MessageReaction{MessageID: "m2", UserID: "bob", Emojis: []string{"76", "66"}, Added: true, At: 200})
	ranks, total, err := store.RankReactionGivers(ctx, session, 0, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(ranks) != 1 || ranks[0].UserID != "bob" || ranks[0].Count != 3 || ranks[0].UserName != "Bob" {
		t.Fatalf("点赞排行 = %#v total=%d", ranks, total)
	}
	// 时间段过滤：只看 150 之后挂上的。
	ranks, _, err = store.RankReactionGivers(ctx, session, 150, 0, 10)
	if err != nil || len(ranks) != 1 || ranks[0].Count != 2 {
		t.Fatalf("按时间过滤的点赞排行 = %#v err=%v", ranks, err)
	}
	if err := store.RecordMessageReaction(ctx, assistant.MessageReaction{Session: session, MessageID: "m3"}); err == nil {
		t.Fatal("缺用户的表情回应不该落库")
	}
}

// 发言排行数的是本地记下的群消息：不算撤回等通知，不跨群，名字取最近一条。
func TestSQLiteStoreRankGroupSpeakers(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	add := func(session, user, name, id string, at int64, kind assistant.EventKind) {
		t.Helper()
		event := assistant.MessageEvent{Kind: kind, Time: at, GroupID: "1", UserID: user, MessageID: id, SenderName: name, RawMessage: "hi",
			Segments: []assistant.MessageSegment{{Type: "text", Data: map[string]string{"text": "hi"}}}}
		if kind == assistant.EventKindNotice {
			event.SubType = "group_recall"
		}
		if err := store.AppendMessageEvent(ctx, session, event); err != nil {
			t.Fatal(err)
		}
	}
	add("bot:group:1", "alice", "阿丽", "1", 100, assistant.EventKindGroup)
	add("bot:group:1", "alice", "Alice新名片", "2", 200, assistant.EventKindGroup)
	add("bot:group:1", "alice", "Alice新名片", "3", 300, assistant.EventKindGroup)
	add("bot:group:1", "bob", "Bob", "4", 250, assistant.EventKindGroup)
	add("bot:group:1", "bob", "Bob", "4", 260, assistant.EventKindNotice)
	add("bot:group:2", "carol", "Carol", "5", 250, assistant.EventKindGroup)

	ranks, total, err := store.RankGroupSpeakers(ctx, "bot:group:1", 0, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []assistant.GroupStatsRank{{UserID: "alice", UserName: "Alice新名片", Count: 3}, {UserID: "bob", UserName: "Bob", Count: 1}}
	if total != 2 || !reflect.DeepEqual(ranks, want) {
		t.Fatalf("发言排行 = %#v total=%d", ranks, total)
	}
	ranks, total, err = store.RankGroupSpeakers(ctx, "bot:group:1", 150, 0, 1)
	if err != nil || total != 2 || len(ranks) != 1 || ranks[0].UserID != "alice" || ranks[0].Count != 2 {
		t.Fatalf("时间段与条数限制 = %#v total=%d err=%v", ranks, total, err)
	}
}

func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
