// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/md5"  //nolint:gosec // 和平台 ETag 一致
	"crypto/sha1" //nolint:gosec // 和实现一样按 SHA1 认图
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// qqFakeMaterial 模拟 /v2/media-images 的 prepare、PUT 直传、complete、查询和删除。
type qqFakeMaterial struct {
	prepares  []map[string]any
	completes []map[string]any
	putTypes  []string
	deletes   []string
	lists     int
	// listed 是查询接口返回的已有素材。
	listed []map[string]any
	// quotaFull 让 prepare 回 40035007，直到删过一张。
	quotaFull bool
	// failPrepare 让 prepare 回服务端错误。
	failPrepare bool
}

func (a *qqFakeAPI) serveMaterial(w http.ResponseWriter, r *http.Request, body map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := &a.material
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v2/media-images":
		m.lists++
		_ = json.NewEncoder(w).Encode(map[string]any{"images": m.listed, "next_cursor": "", "total": len(m.listed)})
	case r.Method == http.MethodPost && r.URL.Path == "/v2/media-images/prepare":
		m.prepares = append(m.prepares, body)
		if m.failPrepare {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 40035006, "message": "internal error"})
			return
		}
		if m.quotaFull {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 40035007, "message": "图片数量超过上限"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"upload_id": "mup1", "presigned_url": a.server.URL + "/put/material", "expires_in": 3600})
	case r.Method == http.MethodPost && r.URL.Path == "/v2/media-images/complete":
		m.completes = append(m.completes, body)
		n := len(m.completes)
		_ = json.NewEncoder(w).Encode(map[string]any{"image_id": "img" + string(rune('0'+n)), "url": "qqbot-media://image/v1/tok" + string(rune('0'+n)), "ttl": 7776000})
	case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/delete"):
		m.deletes = append(m.deletes, r.URL.Path)
		m.quotaFull = false
		_, _ = w.Write([]byte(`{}`))
	default:
		http.NotFound(w, r)
	}
}

func (a *qqFakeAPI) serveMaterialPut(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	a.material.putTypes = append(a.material.putTypes, r.Header.Get("Content-Type"))
	a.mu.Unlock()
	sum := md5Hex(data)
	w.Header().Set("ETag", `"`+sum+`"`)
}

const qqTestPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

func qqTestPNGFile(t *testing.T) (string, []byte) {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(qqTestPNGBase64)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "chart.png")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path, data
}

func qqLastMarkdown(t *testing.T, api *qqFakeAPI) string {
	t.Helper()
	if len(api.messages) == 0 {
		t.Fatal("no message sent")
	}
	markdown, _ := api.messages[len(api.messages)-1]["markdown"].(map[string]any)
	content, _ := markdown["content"].(string)
	return content
}

// 本机图片先传素材换成 qqbot-media 引用；同一张图第二次直接用缓存，不再占额度。
// 公网图片原样保留。
func TestQQOfficialMarkdownUploadsLocalImageAsMaterial(t *testing.T) {
	api := newQQFakeAPI(t)
	channel := api.channel()
	path, data := qqTestPNGFile(t)
	text := "**走势**\n![图](" + path + ")\n![外链](https://example.com/a.png)"
	for i := 0; i < 2; i++ {
		if err := channel.Send(context.Background(), OutgoingMessage{GroupID: "G", Text: text}); err != nil {
			t.Fatal(err)
		}
		content := qqLastMarkdown(t, api)
		if !strings.Contains(content, "![图](qqbot-media://image/v1/tok1)") || strings.Contains(content, path) || !strings.Contains(content, "![外链](https://example.com/a.png)") {
			t.Fatalf("send %d markdown = %q", i, content)
		}
	}
	if api.messages[0]["force_verify_image_resource"] != true {
		t.Fatalf("message = %#v", api.messages[0])
	}
	m := api.material
	if len(m.prepares) != 1 || len(m.completes) != 1 || m.lists != 1 {
		t.Fatalf("prepares = %v, completes = %v, lists = %d", m.prepares, m.completes, m.lists)
	}
	sum := sha1.Sum(data) //nolint:gosec
	prepare := m.prepares[0]
	if prepare["file_name"] != "diana-"+hex.EncodeToString(sum[:])+".png" || prepare["content_type"] != "image/png" ||
		prepare["file_size"] != float64(len(data)) || prepare["md5"] != md5Hex(data) || prepare["sha1"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("prepare = %#v", prepare)
	}
	if len(m.putTypes) != 1 || m.putTypes[0] != "image/png" {
		t.Fatalf("PUT content types = %v", m.putTypes)
	}
	if m.completes[0]["upload_id"] != "mup1" || m.completes[0]["md5"] != md5Hex(data) {
		t.Fatalf("complete = %#v", m.completes[0])
	}
}

// 重启后从查询接口认回以前传过的图，不重复上传。
func TestQQOfficialMarkdownReusesListedMaterial(t *testing.T) {
	api := newQQFakeAPI(t)
	path, data := qqTestPNGFile(t)
	sum := sha1.Sum(data) //nolint:gosec
	api.material.listed = []map[string]any{{
		"image_id": "old", "url": "qqbot-media://image/v1/old", "file_name": "diana-" + hex.EncodeToString(sum[:]) + ".png",
		"created_at": 1, "expires_at": 4102444800,
	}}
	if err := api.channel().Send(context.Background(), OutgoingMessage{GroupID: "G", Text: "![图](" + path + ")"}); err != nil {
		t.Fatal(err)
	}
	if content := qqLastMarkdown(t, api); content != "![图](qqbot-media://image/v1/old)" || len(api.material.prepares) != 0 {
		t.Fatalf("markdown = %q, prepares = %v", content, api.material.prepares)
	}
}

// 额度满了删掉自己传的最旧一张再传；平台出错就保留原地址，整条消息照发。
func TestQQOfficialMarkdownMaterialQuotaAndFailure(t *testing.T) {
	t.Run("quota", func(t *testing.T) {
		api := newQQFakeAPI(t)
		path, _ := qqTestPNGFile(t)
		api.material.quotaFull = true
		api.material.listed = []map[string]any{
			{"image_id": "newer", "url": "qqbot-media://image/v1/b", "file_name": "diana-" + strings.Repeat("b", 40) + ".png", "created_at": 20, "expires_at": 4102444800},
			{"image_id": "older", "url": "qqbot-media://image/v1/a", "file_name": "diana-" + strings.Repeat("a", 40) + ".png", "created_at": 10, "expires_at": 4102444800},
			{"image_id": "foreign", "url": "qqbot-media://image/v1/f", "file_name": "kline.png", "created_at": 1, "expires_at": 4102444800},
		}
		if err := api.channel().Send(context.Background(), OutgoingMessage{GroupID: "G", Text: "![图](" + path + ")"}); err != nil {
			t.Fatal(err)
		}
		if content := qqLastMarkdown(t, api); content != "![图](qqbot-media://image/v1/tok1)" {
			t.Fatalf("markdown = %q", content)
		}
		if len(api.material.deletes) != 1 || api.material.deletes[0] != "/v2/media-images/older/delete" {
			t.Fatalf("deletes = %v", api.material.deletes)
		}
	})
	t.Run("failure", func(t *testing.T) {
		api := newQQFakeAPI(t)
		path, _ := qqTestPNGFile(t)
		api.material.failPrepare = true
		text := "![图](" + path + ")"
		if err := api.channel().Send(context.Background(), OutgoingMessage{GroupID: "G", Text: text}); err != nil {
			t.Fatal(err)
		}
		if content := qqLastMarkdown(t, api); content != text {
			t.Fatalf("markdown = %q", content)
		}
		if _, ok := api.messages[0]["force_verify_image_resource"]; ok {
			t.Fatalf("verify flag without material: %#v", api.messages[0])
		}
	})
}

func TestQQMaterialNeedsUpload(t *testing.T) {
	cases := map[string]bool{
		"https://example.com/a.png":   false,
		"qqbot-media://image/v1/tok":  false,
		"http://127.0.0.1:8080/a.png": true,
		"http://192.168.1.2/a.png":    true,
		"data:image/png;base64,AAAA":  true,
		"/tmp/diana/chart.png":        true,
		"relative/chart.png":          false,
	}
	for source, want := range cases {
		if got := qqMaterialNeedsUpload(source); got != want {
			t.Errorf("qqMaterialNeedsUpload(%q) = %v, want %v", source, got, want)
		}
	}
}

func md5Hex(data []byte) string {
	sum := md5.Sum(data) //nolint:gosec
	return hex.EncodeToString(sum[:])
}

// 带校验的消息被拒就去掉校验重发，不让整条消息丢掉。
func TestQQOfficialMarkdownMaterialVerifyFallback(t *testing.T) {
	api := newQQFakeAPI(t)
	api.rejectVerify = true
	path, _ := qqTestPNGFile(t)
	if err := api.channel().Send(context.Background(), OutgoingMessage{GroupID: "G", Text: "![图](" + path + ")"}); err != nil {
		t.Fatal(err)
	}
	if len(api.messagePosts) != 2 || len(api.messages) != 1 {
		t.Fatalf("posts = %#v", api.messagePosts)
	}
	if _, ok := api.messages[0]["force_verify_image_resource"]; ok || qqLastMarkdown(t, api) != "![图](qqbot-media://image/v1/tok1)" {
		t.Fatalf("message = %#v", api.messages[0])
	}
}
