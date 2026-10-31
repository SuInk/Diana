// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
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

func stickerTransferRouter(store *storage.SQLiteStore) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &BotHandler{sqlite: store}
	router.GET("/api/assistant/stickers", handler.listStickers)
	router.GET("/api/assistant/stickers/facets", handler.stickerFacets)
	router.GET("/api/assistant/stickers/export", handler.exportStickers)
	router.POST("/api/assistant/stickers/import", handler.importStickers)
	router.GET("/api/assistant/stickers/:hash/image", handler.stickerImage)
	router.DELETE("/api/assistant/stickers/:hash", handler.deleteSticker)
	return router
}

func newStickerTransferStore(t *testing.T, name string) *storage.SQLiteStore {
	t.Helper()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func stickerPNG(t *testing.T, shade uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{R: shade, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func postStickerZip(t *testing.T, router *gin.Engine, url string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/octet-stream")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

// 一台机器人导出的表情包，导进另一个实例的另一台机器人后标注跟着走，在任何会话里都能挑到；
// 重复导入不重复收，删掉（拉黑）后主人再导入会解除拉黑。
func TestStickerExportImportRoundTrip(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	source := newStickerTransferStore(t, "source.db")
	body := stickerPNG(t, 200)
	digest := sha256.Sum256(body)
	hash := hex.EncodeToString(digest[:])
	stickerPath := filepath.Join(t.TempDir(), "sticker.png")
	if err := os.WriteFile(stickerPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := source.AppendMessageEvent(ctx, "group:10001", assistant.MessageEvent{
		ProfileID: "bot", Kind: assistant.EventKindGroup, GroupID: "10001", UserID: "20001", MessageID: "m1", Time: time.Now().Unix(),
		Segments: []assistant.MessageSegment{{Type: "image", Data: map[string]string{
			"summary": "[无语]", "sub_type": "1", "cached_file": stickerPath, "cached_mime": "image/png", "content_sha256": hash,
		}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := source.SaveStickerTags(ctx, assistant.StickerTagRecord{
		ContentSHA256: hash, Gist: "翻白眼表示无语", Tags: []string{"无语", "白眼"}, Category: "二次元", Version: assistant.StickerAnnotationVersion,
	}); err != nil {
		t.Fatal(err)
	}

	exported := httptest.NewRecorder()
	stickerTransferRouter(source).ServeHTTP(exported, httptest.NewRequest(http.MethodGet, "/api/assistant/stickers/export?profile=bot", nil))
	if exported.Code != http.StatusOK || exported.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("export status=%d body=%s", exported.Code, exported.Body.String())
	}
	archive, err := zip.NewReader(bytes.NewReader(exported.Body.Bytes()), int64(exported.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, file := range archive.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		files[file.Name], _ = io.ReadAll(reader)
		_ = reader.Close()
	}
	if !bytes.Equal(files["images/"+hash+".png"], body) {
		t.Fatalf("exported files = %v", len(files))
	}
	var manifest stickerPackManifest
	if err := json.Unmarshal(files[stickerPackManifestName], &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Stickers) != 1 || manifest.Stickers[0].Summary != "无语" || manifest.Stickers[0].Category != "二次元" ||
		strings.Contains(string(files[stickerPackManifestName]), stickerPath) {
		t.Fatalf("manifest = %s", files[stickerPackManifestName])
	}

	target := newStickerTransferStore(t, "target.db")
	router := stickerTransferRouter(target)
	imported := postStickerZip(t, router, "/api/assistant/stickers/import?profile=bot2", exported.Body.Bytes())
	if imported.Code != http.StatusOK || !strings.Contains(imported.Body.String(), `"imported":1`) {
		t.Fatalf("import status=%d body=%s", imported.Code, imported.Body.String())
	}
	again := postStickerZip(t, router, "/api/assistant/stickers/import?profile=bot2", exported.Body.Bytes())
	if !strings.Contains(again.Body.String(), `"imported":0`) || !strings.Contains(again.Body.String(), `"duplicate":1`) {
		t.Fatalf("re-import body=%s", again.Body.String())
	}

	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/assistant/stickers?profile=bot2", nil))
	var page storage.StickerLibraryPage
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Items[0].Kind != string(assistant.StickerImportKind) || page.Items[0].Summary != "无语" ||
		page.Items[0].Category != "二次元" || strings.Join(page.Items[0].Tags, ",") != "无语,白眼" {
		t.Fatalf("page = %#v", page)
	}
	facets := httptest.NewRecorder()
	router.ServeHTTP(facets, httptest.NewRequest(http.MethodGet, "/api/assistant/stickers/facets?profile=bot2", nil))
	if !strings.Contains(facets.Body.String(), `"value":"imported","count":1`) {
		t.Fatalf("facets body=%s", facets.Body.String())
	}

	// 跨会话共享全关时，导入的图照样在这台机器人的任意会话里；别的机器人看不到。
	for _, step := range []struct {
		profile string
		want    int
	}{{"bot2", 1}, {"other-bot", 0}} {
		assets, err := target.ListStickerAssets(ctx, assistant.StickerHistoryQuery{Session: "private:30001", ProfileID: step.profile, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(assets) != step.want || (step.want == 1 && (assets[0].ContentSHA256 != hash || !assets[0].Tagged)) {
			t.Fatalf("%s assets = %#v", step.profile, assets)
		}
	}

	deleted := httptest.NewRecorder()
	router.ServeHTTP(deleted, httptest.NewRequest(http.MethodDelete, "/api/assistant/stickers/"+hash+"?profile=bot2", nil))
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete status=%d", deleted.Code)
	}
	restored := postStickerZip(t, router, "/api/assistant/stickers/import?profile=bot2", exported.Body.Bytes())
	if !strings.Contains(restored.Body.String(), `"imported":1`) {
		t.Fatalf("import after delete body=%s", restored.Body.String())
	}
}

// 别处打包的一堆图片也能导入，文件名当表情名；不是图片的文件跳过，清单里冒用的哈希不认。
func TestStickerImportPlainZip(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	var buf bytes.Buffer
	archive := zip.NewWriter(&buf)
	for name, body := range map[string][]byte{
		"pack/狗头.png":            stickerPNG(t, 10),
		"pack/readme.txt":        []byte("hello"),
		"__MACOSX/pack/._狗头.png": []byte("junk"),
	} {
		writer, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	store := newStickerTransferStore(t, "plain.db")
	router := stickerTransferRouter(store)
	response := postStickerZip(t, router, "/api/assistant/stickers/import?profile=bot", buf.Bytes())
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"images":1`) || !strings.Contains(response.Body.String(), `"skipped":1`) {
		t.Fatalf("import status=%d body=%s", response.Code, response.Body.String())
	}
	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/assistant/stickers?profile=bot", nil))
	if !strings.Contains(list.Body.String(), `"summary":"狗头"`) {
		t.Fatalf("list body=%s", list.Body.String())
	}

	for _, step := range []struct {
		name string
		url  string
		body []byte
	}{
		{"not zip", "/api/assistant/stickers/import?profile=bot", []byte("not a zip")},
		{"no profile", "/api/assistant/stickers/import", buf.Bytes()},
	} {
		recorder := postStickerZip(t, router, step.url, step.body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s", step.name, recorder.Code, recorder.Body.String())
		}
	}
}
