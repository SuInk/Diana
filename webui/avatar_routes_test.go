// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

// 用 Telegram 配置档建路由：头像必须由通道取回字节，不能让测试真去连腾讯的 CDN。
func newAvatarEndpointRouter(runtime BotRuntime) (*gin.Engine, *BotHandler) {
	gin.SetMode(gin.TestMode)
	telegram := assistant.DefaultBotConfig()
	telegram.ID = "tg"
	telegram.Platform = "telegram"
	handler := &BotHandler{runtime: runtime, profiles: NewMemoryBotProfileStore(telegram)}
	router := gin.New()
	handler.registerAvatarRoutes(router)
	return router, handler
}

// 地址带着当前内容的哈希时按 immutable 发：这条地址永远对应这张图，浏览器不用
// 再问。换了头像列表会给出新地址——Discord、GitHub 都是这个路数。
func TestAvatarEndpointServesImmutableWhenHashMatches(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3, 4}
	runtime := &avatarStubRuntime{avatar: assistant.GroupAvatar{Data: png, ContentType: "image/png"}, found: true}
	router, handler := newAvatarEndpointRouter(runtime)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/avatars/group/-1001?bot_profile_id=tg", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	// 地址没带哈希：短缓存 + ETag，浏览器下次靠 ETag 拿 304。
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "must-revalidate") {
		t.Fatalf("没带哈希的地址应当要求重新校验：%q", got)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("没有 ETag，浏览器只能整张重下")
	}
	sha := strings.Trim(etag, `"`)

	// 列表接下来会带上哈希，这时才允许长缓存。
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/avatars/group/-1001?bot_profile_id=tg&v="+sha, nil))
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("带哈希的地址没有按 immutable 发：%q", got)
	}

	// 浏览器带着 ETag 回来：回 304，不传图。
	request := httptest.NewRequest(http.MethodGet, "/api/assistant/avatars/group/-1001?bot_profile_id=tg", nil)
	request.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, request)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("没有走 304：status=%d body=%d", rec.Code, rec.Body.Len())
	}

	// 取过一次之后，列表拼地址就能带上哈希了。
	if got := handler.consoleAvatarURL(avatarKindGroup, "tg", "-1001"); !strings.Contains(got, "v="+sha) {
		t.Fatalf("列表地址没带上哈希：%q", got)
	}
}

// 没有头像、已退群、平台不支持，对前端都是一回事：404，显示占位图。
func TestAvatarEndpointRejectsUnknownKindAndMissingAvatar(t *testing.T) {
	router, _ := newAvatarEndpointRouter(&avatarStubRuntime{found: false})
	for _, path := range []string{
		"/api/assistant/avatars/group/-1001",
		"/api/assistant/avatars/banner/-1001",
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d", path, rec.Code)
		}
	}
}
