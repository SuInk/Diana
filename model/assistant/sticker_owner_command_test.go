package assistant

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

type stickerRemoveTestStore struct {
	*memoryMessageHistoryStore
	library map[string]bool
	calls   []string
}

func (s *stickerRemoveTestStore) DeleteStickerAsset(_ context.Context, hash, profileID string) (int, error) {
	s.calls = append(s.calls, profileID+"/"+hash)
	if s.library[hash] {
		delete(s.library, hash)
		return 1, nil
	}
	return 0, nil
}

func newStickerRemoveRuntime(t *testing.T, library ...string) (*Runtime, *stickerRemoveTestStore) {
	t.Helper()
	store := &stickerRemoveTestStore{memoryMessageHistoryStore: newMemoryMessageHistoryStore(), library: map[string]bool{}}
	for _, hash := range library {
		store.library[hash] = true
	}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	return runtime, store
}

func TestOwnerStickerRemoveDeletesQuotedSticker(t *testing.T) {
	body := []byte("sticker-bytes")
	hash := imageBytesSHA256(body)
	runtime, store := newStickerRemoveRuntime(t, hash)
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "bot", GroupID: "20001", UserID: "10001", Quoted: &QuotedMessage{
		MessageID: "m1",
		UserID:    "30001",
		// 没有 content_sha256，也没有本地缓存：得现读图算哈希，结果要和入库时一致。
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"sub_type": "1", "url": "base64://" + base64.StdEncoding.EncodeToString(body)}}},
	}}
	if !runtime.wouldHandleOwnerCommand(event, "删除表情包") {
		t.Fatal("command not recognized")
	}
	reply, handled := runtime.handleOwnerCommand(event, "删除表情包")
	if !handled || !strings.Contains(reply, "移除 1 张") {
		t.Fatalf("reply=%q handled=%v", reply, handled)
	}
	if len(store.calls) != 1 || store.calls[0] != "bot/"+hash {
		t.Fatalf("calls=%v", store.calls)
	}
}

func TestOwnerStickerRemoveReportsImageOutsideLibrary(t *testing.T) {
	hash := imageBytesSHA256([]byte("other"))
	runtime, store := newStickerRemoveRuntime(t)
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "bot", GroupID: "20001", UserID: "10001", Quoted: &QuotedMessage{
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{imageContentSHA256Key: hash}}},
	}}
	reply, handled := runtime.handleOwnerCommand(event, "表情包 删除")
	if !handled || !strings.Contains(reply, "不在表情包库") {
		t.Fatalf("reply=%q handled=%v", reply, handled)
	}
	if len(store.calls) != 1 {
		t.Fatalf("blocklist not written: %v", store.calls)
	}
}

func TestOwnerStickerRemoveNeedsQuotedImage(t *testing.T) {
	runtime, store := newStickerRemoveRuntime(t)
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "bot", GroupID: "20001", UserID: "10001"}
	reply, handled := runtime.handleOwnerCommand(event, "移除表情包")
	if !handled || !strings.Contains(reply, "请回复") {
		t.Fatalf("reply=%q handled=%v", reply, handled)
	}
	if len(store.calls) != 0 {
		t.Fatalf("calls=%v", store.calls)
	}
}

func TestStickerRemoveIgnoredForNonOwner(t *testing.T) {
	runtime, _ := newStickerRemoveRuntime(t)
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "bot", GroupID: "20001", UserID: "30001"}
	if _, handled := runtime.handleOwnerCommand(event, "删除表情包"); handled {
		t.Fatal("non-owner handled")
	}
}

func TestStickerRemoveToolChecksOwnerAndRemovesQuoted(t *testing.T) {
	hash := imageBytesSHA256([]byte("tool"))
	runtime, store := newStickerRemoveRuntime(t, hash)
	quoted := &QuotedMessage{Segments: []MessageSegment{{Type: "image", Data: map[string]string{imageContentSHA256Key: hash}}}}
	member := &dianaStickerRemoveTool{runtime: runtime, event: MessageEvent{Kind: EventKindGroup, ProfileID: "bot", GroupID: "20001", UserID: "30001", Quoted: quoted}}
	if _, err := member.Run(context.Background(), nil); err == nil || len(store.calls) != 0 {
		t.Fatalf("non-owner err=%v calls=%v", err, store.calls)
	}
	owner := &dianaStickerRemoveTool{runtime: runtime, event: MessageEvent{Kind: EventKindGroup, ProfileID: "bot", GroupID: "20001", UserID: "10001", Quoted: quoted}}
	out, err := owner.Run(context.Background(), nil)
	if err != nil || !strings.Contains(out, "移除 1 张") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	noQuote := &dianaStickerRemoveTool{runtime: runtime, event: MessageEvent{Kind: EventKindGroup, ProfileID: "bot", GroupID: "20001", UserID: "10001"}}
	if _, err := noQuote.Run(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "引用") {
		t.Fatalf("no quote err=%v", err)
	}
}
