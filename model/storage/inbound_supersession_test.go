// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

// 只带图的消息不再压 15 秒等后一句话来并：进来就能领，同一个人紧跟的文字也是
// 各自一条，谁都不会把谁注销。
func TestInboundMediaOnlyMessageIsClaimableImmediately(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "media-now.db"))
	defer func() { _ = store.Close() }()
	now := time.Now()
	media := inboundMediaEvent("media-1", "user-1", now.Unix())
	mediaID, inserted, err := store.EnqueueInboundEvent(ctx, "group:1", media)
	if err != nil || !inserted {
		t.Fatalf("enqueue media id=%q inserted=%v err=%v", mediaID, inserted, err)
	}
	item, ok, err := store.ClaimNextInboundEvent(ctx, "worker-media", time.Now().Add(time.Minute))
	if err != nil || !ok || item.ID != mediaID {
		t.Fatalf("media-only message should be claimable immediately: item=%#v ok=%v err=%v", item, ok, err)
	}
	question := inboundQuestionEvent("question-1", "user-1", now.Add(5*time.Second).Unix(), "这是什么？")
	questionID, _, err := store.EnqueueInboundEvent(ctx, "group:1", question, assistant.InboundPriorityTriggered)
	if err != nil {
		t.Fatal(err)
	}
	if _, superseded, err := store.InboundEventSuperseded(ctx, media); err != nil || superseded {
		t.Fatalf("media message must stay its own turn: superseded=%v err=%v", superseded, err)
	}
	var status string
	if err := store.db.QueryRow(`SELECT status FROM inbound_events WHERE id = ?`, questionID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != inboundStatusPending {
		t.Fatalf("question status=%q", status)
	}
}

// 追发合并写下的标记，发送前那道闸门要读得到。
func TestInboundEventSupersededReadsReplyMergeMarker(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "reply-merge.db"))
	defer func() { _ = store.Close() }()
	event := inboundQuestionEvent("followup-1", "user-1", time.Now().Unix(), "再补一句")
	if _, _, err := store.EnqueueInboundEvent(ctx, "group:1", event); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordInboundEventReplyMerge(ctx, event, "root-turn"); err != nil {
		t.Fatal(err)
	}
	turnID, superseded, err := store.InboundEventSuperseded(ctx, event)
	if err != nil || !superseded || turnID != "root-turn" {
		t.Fatalf("turn=%q superseded=%v err=%v", turnID, superseded, err)
	}
}

// 旧版相邻媒体合并留下的行（outcome=superseded_media_turn）照样读得出来。
func TestInboundEventSupersededReadsLegacyMediaTurnRows(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "legacy.db"))
	defer func() { _ = store.Close() }()
	media := inboundMediaEvent("media-1", "user-1", time.Now().Unix())
	mediaID, _, err := store.EnqueueInboundEvent(ctx, "group:1", media)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE inbound_events SET status = ?, outcome = 'superseded_media_turn', superseded_by = 'old-turn' WHERE id = ?`, inboundStatusDone, mediaID); err != nil {
		t.Fatal(err)
	}
	turnID, superseded, err := store.InboundEventSuperseded(ctx, media)
	if err != nil || !superseded || turnID != "old-turn" {
		t.Fatalf("turn=%q superseded=%v err=%v", turnID, superseded, err)
	}
}

func inboundMediaEvent(messageID, userID string, eventTime int64) assistant.MessageEvent {
	return assistant.MessageEvent{
		Kind: assistant.EventKindGroup, GroupID: "1", UserID: userID, MessageID: messageID, Time: eventTime,
		Segments: []assistant.MessageSegment{{Type: "image", Data: map[string]string{"url": "https://example.com/" + messageID + ".png"}}},
	}
}

func inboundQuestionEvent(messageID, userID string, eventTime int64, text string) assistant.MessageEvent {
	return assistant.MessageEvent{
		Kind: assistant.EventKindGroup, GroupID: "1", UserID: userID, MessageID: messageID, Time: eventTime,
		RawMessage: text, Segments: []assistant.MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}
