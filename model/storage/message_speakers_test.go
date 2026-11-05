// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func speakerEvent(at int64, userID, name, username, messageID string) assistant.MessageEvent {
	return assistant.MessageEvent{
		Kind: assistant.EventKindGroup, Platform: "telegram", GroupID: "-10042", UserID: userID,
		SenderName: name, SenderUsername: username, MessageID: messageID, RawMessage: "hi",
		Segments: []assistant.MessageSegment{{Type: "text", Data: map[string]string{"text": "hi"}}},
		Time:     at,
	}
}

// 改过昵称的人用旧昵称、新昵称、@用户名都能找回同一个账号，昵称按最近使用排序。
func TestListMessageSpeakersResolvesOldNamesAndUsername(t *testing.T) {
	ctx := context.Background()
	store := vectorStore(t)
	session := "tg:group:-10042"
	for _, event := range []assistant.MessageEvent{
		speakerEvent(10, "50001", "小明", "", "1"),
		speakerEvent(20, "50001", "明明同学", "ming_cat", "2"),
		speakerEvent(30, "50002", "小明的猫", "", "3"),
		speakerEvent(40, "50003", "路人", "", "4"),
	} {
		if err := store.AppendMessageEvent(ctx, session, event); err != nil {
			t.Fatal(err)
		}
	}
	bot := speakerEvent(50, "90000", "机器人", "", "5")
	bot.Outbound = true
	if err := store.AppendMessageEvent(ctx, session, bot); err != nil {
		t.Fatal(err)
	}

	got, err := store.ListMessageSpeakers(ctx, assistant.MessageSpeakerQuery{Session: session, Name: "小明"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].UserID != "50001" || !got[0].ExactMatch || got[1].UserID != "50002" || got[1].ExactMatch {
		t.Fatalf("exact old-name match should lead: %+v", got)
	}
	if len(got[0].Names) != 2 || got[0].Names[0] != "明明同学" || got[0].Username != "ming_cat" {
		t.Fatalf("all names of the account should be returned newest first: %+v", got[0])
	}

	got, err = store.ListMessageSpeakers(ctx, assistant.MessageSpeakerQuery{Session: session, Name: "@Ming_Cat"})
	if err != nil || len(got) != 1 || got[0].UserID != "50001" || !got[0].ExactMatch {
		t.Fatalf("username lookup failed: %+v err=%v", got, err)
	}

	all, err := store.ListMessageSpeakers(ctx, assistant.MessageSpeakerQuery{Session: session})
	if err != nil || len(all) != 3 || all[0].UserID != "50003" {
		t.Fatalf("listing should skip outbound and order by recency: %+v err=%v", all, err)
	}
	for _, speaker := range all {
		if speaker.UserID == "90000" {
			t.Fatalf("bot outbound message must not enter the directory: %+v", all)
		}
	}

	other, err := store.ListMessageSpeakers(ctx, assistant.MessageSpeakerQuery{Session: "tg:group:-10099", Name: "小明"})
	if err != nil || len(other) != 0 {
		t.Fatalf("other session must not leak: %+v err=%v", other, err)
	}
	cross, err := store.ListMessageSpeakers(ctx, assistant.MessageSpeakerQuery{SessionPrefix: "tg:group:", CrossSession: true, Name: "路人"})
	if err != nil || len(cross) != 1 {
		t.Fatalf("cross-session lookup failed: %+v err=%v", cross, err)
	}
}

// 升级前的存量历史由回填补进名录。
func TestBackfillMessageSpeakersCoversExistingHistory(t *testing.T) {
	ctx := context.Background()
	store := vectorStore(t)
	store.stopWALMaintenance()
	session := "tg:group:-10042"
	for _, event := range []assistant.MessageEvent{
		speakerEvent(10, "50001", "老名字", "", "1"),
		speakerEvent(20, "50001", "新名字", "handle", "2"),
	} {
		if err := store.AppendMessageEvent(ctx, session, event); err != nil {
			t.Fatal(err)
		}
	}
	// 模拟升级前：名录为空、回填游标未建立。
	if _, err := store.db.Exec(`DELETE FROM message_speakers`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM app_state WHERE key = ?`, messageSpeakersBackfillKey); err != nil {
		t.Fatal(err)
	}
	if err := store.BackfillMessageSpeakers(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := store.ListMessageSpeakers(ctx, assistant.MessageSpeakerQuery{Session: session, Name: "老名字"})
	if err != nil || len(got) != 1 || got[0].UserID != "50001" || got[0].Username != "handle" || len(got[0].Names) != 2 {
		t.Fatalf("backfill missed history: %+v err=%v", got, err)
	}
	if _, pending := store.messageSpeakersBackfillPending(ctx); pending {
		t.Fatal("backfill should be marked done")
	}
}
