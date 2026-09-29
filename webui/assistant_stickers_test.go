// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

// 插件设置页能浏览表情包池并点开原图；列表不下发服务器本地路径，按机器人筛选时
// 取不到别的机器人收的图。
func TestStickerLibraryEndpoints(t *testing.T) {
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "sticker-library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	stickerPath := filepath.Join(t.TempDir(), "sticker.png")
	file, err := os.Create(stickerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("e", 64)
	if err := store.AppendMessageEvent(context.Background(), "group:g1", assistant.MessageEvent{
		ProfileID: "bot", Kind: assistant.EventKindGroup, GroupID: "g1", UserID: "u", MessageID: "m1", Time: time.Now().Unix(),
		Segments: []assistant.MessageSegment{{Type: "image", Data: map[string]string{
			"summary": "[懂了]", "sub_type": "1", "cached_file": stickerPath, "content_sha256": hash,
		}}},
	}); err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &BotHandler{sqlite: store}
	router.GET("/api/assistant/stickers", handler.listStickers)
	router.GET("/api/assistant/stickers/facets", handler.stickerFacets)
	router.POST("/api/assistant/stickers/cleanup", handler.cleanupStickers)
	router.GET("/api/assistant/stickers/:hash/image", handler.stickerImage)
	router.DELETE("/api/assistant/stickers/:hash", handler.deleteSticker)

	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/assistant/stickers?profile=bot", nil))
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), stickerPath) {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	var page storage.StickerLibraryPage
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Items[0].Hash != hash || page.Items[0].Summary != "懂了" {
		t.Fatalf("page = %#v", page)
	}

	img := httptest.NewRecorder()
	router.ServeHTTP(img, httptest.NewRequest(http.MethodGet, "/api/assistant/stickers/"+hash+"/image?profile=bot", nil))
	want, _ := os.ReadFile(stickerPath)
	if img.Code != http.StatusOK || img.Header().Get("Content-Type") != "image/png" || !bytes.Equal(img.Body.Bytes(), want) {
		t.Fatalf("image status=%d type=%q", img.Code, img.Header().Get("Content-Type"))
	}
	for _, url := range []string{
		"/api/assistant/stickers/" + hash + "/image?profile=other-bot",
		"/api/assistant/stickers/" + strings.Repeat("f", 64) + "/image",
		"/api/assistant/stickers/not-a-hash/image",
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, url, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d", url, recorder.Code)
		}
	}

	facets := httptest.NewRecorder()
	router.ServeHTTP(facets, httptest.NewRequest(http.MethodGet, "/api/assistant/stickers/facets?profile=bot", nil))
	if facets.Code != http.StatusOK || !strings.Contains(facets.Body.String(), `"value":"group:g1","count":1`) {
		t.Fatalf("facets status=%d body=%s", facets.Code, facets.Body.String())
	}
	// 清理先试算：按来源筛到这张，但不删。
	dry := httptest.NewRecorder()
	router.ServeHTTP(dry, httptest.NewRequest(http.MethodPost, "/api/assistant/stickers/cleanup?profile=bot&source=group:g1&dry_run=true", nil))
	if dry.Code != http.StatusOK || !strings.Contains(dry.Body.String(), `"stickers":1`) {
		t.Fatalf("dry run status=%d body=%s", dry.Code, dry.Body.String())
	}

	// 别的机器人删不到这张；本机器人删掉后列表清空，再删一次是 404。
	for _, step := range []struct {
		url  string
		code int
	}{
		{"/api/assistant/stickers/" + hash + "?profile=other-bot", http.StatusNotFound},
		{"/api/assistant/stickers/" + hash + "?profile=bot", http.StatusOK},
		{"/api/assistant/stickers/" + hash + "?profile=bot", http.StatusNotFound},
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, step.url, nil))
		if recorder.Code != step.code {
			t.Fatalf("DELETE %s status=%d want %d", step.url, recorder.Code, step.code)
		}
	}
	after := httptest.NewRecorder()
	router.ServeHTTP(after, httptest.NewRequest(http.MethodGet, "/api/assistant/stickers?profile=bot", nil))
	if !strings.Contains(after.Body.String(), `"total":0`) {
		t.Fatalf("after delete body=%s", after.Body.String())
	}
}
