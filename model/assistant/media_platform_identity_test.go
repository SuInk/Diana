// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestPlatformImageMD5UsesOnlyExplicitDigests(t *testing.T) {
	digest := strings.Repeat("ab", 16)
	for _, key := range []string{"md5", "file_md5", "fileMd5"} {
		segment := MessageSegment{Type: "image", Data: map[string]string{key: strings.ToUpper(digest)}}
		if got := platformImageMD5(PlatformOneBotV11, segment); got != digest {
			t.Fatalf("%s: got %q", key, got)
		}
		if got := platformImageMD5(PlatformTelegram, segment); got != "" {
			t.Fatalf("other platform matched: %q", got)
		}
	}
	for _, data := range []map[string]string{{"file": digest + ".jpg"}, {"file_id": digest}, {"md5": "invalid"}} {
		if got := platformImageMD5(PlatformOneBotV11, MessageSegment{Type: "image", Data: data}); got != "" {
			t.Fatalf("unreliable identifier matched: %q", got)
		}
	}
}

func TestPlatformImageMD5DeduplicatesChangedURLs(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	body := pngBytes(t)
	sum := md5.Sum(body)
	digest := hex.EncodeToString(sum[:])
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(body)
	}))
	defer server.Close()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			segment := MessageSegment{Type: "image", Data: map[string]string{"md5": digest}}
			got, _, err := readPlatformHistoryImage(context.Background(), PlatformOneBotV11, segment, server.URL+"/?token="+strings.Repeat("x", i))
			if err != nil || !bytes.Equal(got, body) {
				t.Errorf("read: %v, bytes=%d", err, len(got))
			}
		}(i)
	}
	wg.Wait()
	if hits.Load() != 1 {
		t.Fatalf("download count = %d", hits.Load())
	}
	dir, _ := historyMediaDir()
	path := cachedMediaSource(dir, "onebot-image-md5:"+digest, maxHistoryImageBytes)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	segment := MessageSegment{Type: "image", Data: map[string]string{"md5": digest}}
	if _, _, err := readPlatformHistoryImage(context.Background(), PlatformOneBotV11, segment, server.URL+"/after-delete"); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("deleted content not refetched: %d", hits.Load())
	}
}

func TestPlatformImageWrongMD5DoesNotPoisonIndex(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", dir)
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	body := pngBytes(t)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(body)
	}))
	defer server.Close()
	digest := strings.Repeat("0", 32)
	segment := MessageSegment{Type: "image", Data: map[string]string{"md5": digest}}
	for _, suffix := range []string{"/a", "/b"} {
		got, _, err := readPlatformHistoryImage(context.Background(), PlatformOneBotV11, segment, server.URL+suffix)
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("incorrect MD5 should retain usable image: %v", err)
		}
	}
	if hits.Load() != 2 || cachedMediaSource(dir, "onebot-image-md5:"+digest, maxHistoryImageBytes) != "" {
		t.Fatal("incorrect MD5 cached or fallback downloaded twice")
	}
}

func TestTelegramThumbnailUsesItsOwnUniqueID(t *testing.T) {
	for _, uniqueID := range []string{"thumbnail-unique", ""} {
		msg := &telegramMessage{Chat: &telegramChat{ID: 1, Type: "private"}, Sticker: &telegramSticker{
			FileID: "original", FileUniqueID: "original-unique", IsAnimated: true,
			Thumbnail: &telegramPhoto{FileID: "thumbnail", FileUniqueID: uniqueID},
		}}
		event := telegramMessageToEvent(msg, "1", "bot")
		if got := event.Segments[1].Data["file_unique_id"]; got != uniqueID {
			t.Fatalf("thumbnail borrowed original identity: %q", got)
		}
	}
}

func TestTelegramUniqueIDReusesLegacyIndex(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	api := newFakeTelegramAPI(t, map[string]any{
		"getFile": map[string]any{"file_path": "docs/a.pdf"}, "download:docs/a.pdf": []byte("document bytes"),
	})
	segment := MessageSegment{Type: "file", Data: map[string]string{"file_id": "old-file", "name": "a.pdf"}}
	first, err := api.channel().downloadIncomingFile(context.Background(), MessageEvent{}, segment)
	if err != nil {
		t.Fatal(err)
	}
	segment.Data["file_unique_id"] = "stable-document"
	second, err := api.channel().downloadIncomingFile(context.Background(), MessageEvent{}, segment)
	segment.Data["file_id"] = "new-file"
	third, thirdErr := api.channel().downloadIncomingFile(context.Background(), MessageEvent{}, segment)
	if err != nil || thirdErr != nil || first != second || first != third || len(api.callsOf("getFile")) != 1 {
		t.Fatalf("legacy migration: paths=%q/%q/%q errors=%v/%v calls=%d", first, second, third, err, thirdErr, len(api.callsOf("getFile")))
	}
}
