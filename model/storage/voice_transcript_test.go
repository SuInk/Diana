package storage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestInboundVoiceBase64IsStoredOutOfBand(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, t.TempDir()+"/voice.db")
	defer func() { _ = store.Close() }()
	body := []byte("voice-payload-that-must-not-live-in-event-json")
	encoded := base64.StdEncoding.EncodeToString(body)
	event := inboundTestEvent("voice-1", "", time.Now().Unix())
	event.Segments = []assistant.MessageSegment{{Type: "record", Data: map[string]string{"file": "base64://" + encoded}}}
	event.Quoted = &assistant.QuotedMessage{MessageID: "quoted-voice", Segments: []assistant.MessageSegment{{Type: "record", Data: map[string]string{"url": "data:audio/wav;base64," + encoded}}}}
	id, inserted, err := store.EnqueueInboundEvent(ctx, "group:1", event)
	if err != nil || !inserted {
		t.Fatalf("enqueue inserted=%v err=%v", inserted, err)
	}
	var payload string
	if err := store.db.QueryRowContext(ctx, `SELECT payload FROM inbound_events WHERE id = ?`, id).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, encoded) || strings.Contains(payload, "base64://") {
		t.Fatalf("durable event leaked inline audio: %s", payload)
	}
	var persisted assistant.MessageEvent
	if err := json.Unmarshal([]byte(payload), &persisted); err != nil {
		t.Fatal(err)
	}
	hash := persisted.Segments[0].Data["cached_blob_sha256"]
	if persisted.Quoted == nil || persisted.Quoted.Segments[0].Data["cached_blob_sha256"] != hash {
		t.Fatalf("quoted voice was not deduplicated out of band: %#v", persisted.Quoted)
	}
	loaded, found, err := store.LoadVoiceBlob(ctx, hash)
	if err != nil || !found || string(loaded) != string(body) {
		t.Fatalf("blob found=%v body=%q err=%v", found, loaded, err)
	}
}

func TestVoiceTranscriptCacheRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, t.TempDir()+"/voice-cache.db")
	defer func() { _ = store.Close() }()
	want := assistant.VoiceTranscriptRecord{CacheKey: "cache", AudioSHA256: "audio", Backend: "openai_compatible", Model: "whisper-1", Language: "zh", Transcript: "测试语音", DurationMS: 1200, CreatedAt: time.Now().Unix()}
	if err := store.SaveVoiceTranscript(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.LoadVoiceTranscript(ctx, want.CacheKey)
	if err != nil || !found || got.Transcript != want.Transcript || got.DurationMS != want.DurationMS {
		t.Fatalf("cache found=%v got=%#v err=%v", found, got, err)
	}
}
