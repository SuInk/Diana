package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestInboundMediaTurnMediaFirstClaimsOnceAndSupersedes(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "media-first.db"))
	defer func() { _ = store.Close() }()
	now := time.Now()
	media := inboundMediaEvent("media-1", "user-1", now.Unix())
	mediaID, inserted, err := store.EnqueueInboundEvent(ctx, "group:1", media)
	if err != nil || !inserted {
		t.Fatalf("enqueue media id=%q inserted=%v err=%v", mediaID, inserted, err)
	}
	question := inboundQuestionEvent("question-1", "user-1", now.Add(5*time.Second).Unix(), "这是什么？")
	questionID, inserted, err := store.EnqueueInboundEvent(ctx, "group:1", question, assistant.InboundPriorityTriggered)
	if err != nil || !inserted {
		t.Fatalf("enqueue question id=%q inserted=%v err=%v", questionID, inserted, err)
	}
	item, ok, err := store.ClaimNextInboundEvent(ctx, "worker-question", time.Now().Add(time.Minute))
	if err != nil || !ok || item.ID != questionID {
		t.Fatalf("claim item=%#v ok=%v err=%v, want question", item, ok, err)
	}
	sources, err := store.ClaimInboundMediaForTurn(ctx, questionID, "group:1", question, assistant.InboundMediaMergeWindow)
	if err != nil || len(sources) != 1 || sources[0].MessageID != media.MessageID {
		t.Fatalf("claimed sources=%#v err=%v", sources, err)
	}
	if again, err := store.ClaimInboundMediaForTurn(ctx, questionID, "group:1", question, assistant.InboundMediaMergeWindow); err != nil || len(again) != 0 {
		t.Fatalf("duplicate claim=%#v err=%v", again, err)
	}
	turnID, superseded, err := store.InboundEventSuperseded(ctx, media)
	if err != nil || !superseded || turnID != questionID {
		t.Fatalf("superseded=%v turn=%q err=%v", superseded, turnID, err)
	}
	var status, outcome string
	if err := store.db.QueryRow(`SELECT status, outcome FROM inbound_events WHERE id = ?`, mediaID).Scan(&status, &outcome); err != nil {
		t.Fatal(err)
	}
	if status != inboundStatusDone || outcome != "superseded_media_turn" {
		t.Fatalf("media status=%q outcome=%q", status, outcome)
	}
}

func TestInboundMediaTurnFollowupNeedsNoMediaKeyword(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "structural-trigger.db"))
	defer func() { _ = store.Close() }()
	now := time.Now()
	media := inboundMediaEvent("voice-1", "user-1", now.Unix())
	media.Segments = []assistant.MessageSegment{{Type: "record", Data: map[string]string{"file": "voice.amr"}}}
	if _, inserted, err := store.EnqueueInboundEvent(ctx, "group:1", media); err != nil || !inserted {
		t.Fatalf("enqueue voice inserted=%v err=%v", inserted, err)
	}
	followup := inboundQuestionEvent("followup-1", "user-1", now.Add(time.Second).Unix(), "帮我看看")
	followupID, inserted, err := store.EnqueueInboundEvent(ctx, "group:1", followup)
	if err != nil || !inserted {
		t.Fatalf("enqueue followup inserted=%v err=%v", inserted, err)
	}
	item, ok, err := store.ClaimNextInboundEvent(ctx, "worker", time.Now().Add(time.Minute))
	if err != nil || !ok || item.ID != followupID {
		t.Fatalf("claim item=%#v ok=%v err=%v", item, ok, err)
	}
	sources, err := store.ClaimInboundMediaForTurn(ctx, followupID, "group:1", followup, assistant.InboundMediaMergeWindow)
	if err != nil || len(sources) != 1 || sources[0].Segments[0].Type != "record" {
		t.Fatalf("sources=%#v err=%v", sources, err)
	}
}

func TestInboundMediaTurnTextFirstCanClaimLaterMedia(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "text-first.db"))
	defer func() { _ = store.Close() }()
	now := time.Now()
	question := inboundQuestionEvent("question-1", "user-1", now.Unix(), "请看这张截图")
	questionID, _, err := store.EnqueueInboundEvent(ctx, "group:1", question, assistant.InboundPriorityTriggered)
	if err != nil {
		t.Fatal(err)
	}
	media := inboundMediaEvent("media-1", "user-1", now.Add(5*time.Second).Unix())
	if _, _, err := store.EnqueueInboundEvent(ctx, "group:1", media); err != nil {
		t.Fatal(err)
	}
	sources, err := store.ClaimInboundMediaForTurn(ctx, questionID, "group:1", question, assistant.InboundMediaMergeWindow)
	if err != nil || len(sources) != 1 || sources[0].MessageID != media.MessageID {
		t.Fatalf("text-first sources=%#v err=%v", sources, err)
	}
}

func TestInboundMediaTurnNeverCrossesUsers(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "users.db"))
	defer func() { _ = store.Close() }()
	now := time.Now()
	media := inboundMediaEvent("media-a", "user-a", now.Unix())
	if _, _, err := store.EnqueueInboundEvent(ctx, "group:1", media); err != nil {
		t.Fatal(err)
	}
	question := inboundQuestionEvent("question-b", "user-b", now.Add(time.Second).Unix(), "图片里是什么？")
	questionID, _, err := store.EnqueueInboundEvent(ctx, "group:1", question)
	if err != nil {
		t.Fatal(err)
	}
	sources, err := store.ClaimInboundMediaForTurn(ctx, questionID, "group:1", question, assistant.InboundMediaMergeWindow)
	if err != nil || len(sources) != 0 {
		t.Fatalf("cross-user sources=%#v err=%v", sources, err)
	}
	if _, superseded, err := store.InboundEventSuperseded(ctx, media); err != nil || superseded {
		t.Fatalf("other user's media superseded=%v err=%v", superseded, err)
	}
}

func TestInboundMediaTurnMarksProcessingJobBeforeSend(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "processing.db"))
	defer func() { _ = store.Close() }()
	now := time.Now()
	media := inboundMediaEvent("media-1", "user-1", now.Unix())
	mediaID, _, err := store.EnqueueInboundEvent(ctx, "group:1", media)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE inbound_events SET available_at = ? WHERE id = ?`, time.Now().Add(-time.Second).UnixNano(), mediaID); err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNextInboundEvent(ctx, "slow-media-worker", time.Now().Add(time.Minute))
	if err != nil || !ok || item.ID != mediaID {
		t.Fatalf("claim media item=%#v ok=%v err=%v", item, ok, err)
	}
	question := inboundQuestionEvent("question-1", "user-1", now.Add(time.Second).Unix(), "图中是什么？")
	questionID, _, err := store.EnqueueInboundEvent(ctx, "group:1", question)
	if err != nil {
		t.Fatal(err)
	}
	sources, err := store.ClaimInboundMediaForTurn(ctx, questionID, "group:1", question, assistant.InboundMediaMergeWindow)
	if err != nil || len(sources) != 1 {
		t.Fatalf("processing sources=%#v err=%v", sources, err)
	}
	var status, supersededBy string
	if err := store.db.QueryRow(`SELECT status, superseded_by FROM inbound_events WHERE id = ?`, mediaID).Scan(&status, &supersededBy); err != nil {
		t.Fatal(err)
	}
	if status != inboundStatusProcessing || supersededBy != questionID {
		t.Fatalf("processing media status=%q superseded_by=%q", status, supersededBy)
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
