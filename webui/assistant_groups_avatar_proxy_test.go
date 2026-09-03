// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

type avatarStubRuntime struct {
	BotRuntime
	avatar    assistant.GroupAvatar
	found     bool
	gotGroup  string
	gotProfil string
}

func (r *avatarStubRuntime) GroupAvatarForProfile(_ context.Context, profileID, groupID string) (assistant.GroupAvatar, bool) {
	r.gotGroup = groupID
	r.gotProfil = profileID
	return r.avatar, r.found
}

func newAvatarRouter(runtime BotRuntime) *gin.Engine {
	gin.SetMode(gin.TestMode)
	handler := &BotHandler{runtime: runtime}
	router := gin.New()
	router.GET("/api/assistant/groups/:id/avatar", handler.groupAvatar)
	return router
}

// 头像字节由服务端取回后转发：Telegram 的文件地址里带着 Bot Token，
// 绝不能让浏览器直接去请求那个地址。
func TestGroupAvatarProxyServesImageBytes(t *testing.T) {
	pngHeader := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	runtime := &avatarStubRuntime{
		avatar: assistant.GroupAvatar{Data: pngHeader, ContentType: "image/png"},
		found:  true,
	}
	rec := httptest.NewRecorder()
	newAvatarRouter(runtime).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/groups/-1001/avatar?bot_profile_id=tg", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("content type = %q", got)
	}
	if rec.Body.Len() != len(pngHeader) {
		t.Fatalf("body length = %d, want %d", rec.Body.Len(), len(pngHeader))
	}
	if runtime.gotGroup != "-1001" || runtime.gotProfil != "tg" {
		t.Fatalf("group = %q profile = %q", runtime.gotGroup, runtime.gotProfil)
	}
	// 响应里任何地方都不该出现上游文件地址或凭据。
	if strings.Contains(rec.Body.String(), "/file/bot") {
		t.Fatal("response leaked the upstream file URL")
	}
}

// 没有头像、机器人已退群、平台不支持——前端都按加载失败处理，显示占位图。
func TestGroupAvatarProxyReturnsNotFoundWhenUnavailable(t *testing.T) {
	rec := httptest.NewRecorder()
	newAvatarRouter(&avatarStubRuntime{found: false}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/groups/-1001/avatar", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// 平台返回的内容不是图片时不能照单转发给浏览器。
func TestGroupAvatarProxyRejectsNonImageContent(t *testing.T) {
	runtime := &avatarStubRuntime{
		avatar: assistant.GroupAvatar{Data: []byte("<html>nope</html>"), ContentType: "text/html; charset=utf-8"},
		found:  true,
	}
	rec := httptest.NewRecorder()
	newAvatarRouter(runtime).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/groups/-1001/avatar", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// 运行时不支持取头像时（比如纯 OneBot 部署）直接 404，不该 panic。
func TestGroupAvatarProxyWithoutCapableRuntime(t *testing.T) {
	rec := httptest.NewRecorder()
	newAvatarRouter(&stubBotRuntimeWithoutAvatar{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/groups/111/avatar", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

type stubBotRuntimeWithoutAvatar struct{ BotRuntime }

// 代理地址要能承载 Telegram 的负数群号，也要带上归属机器人。
func TestConsoleGroupAvatarURL(t *testing.T) {
	if got := consoleGroupAvatarURL("-1001", "tg-profile"); got != "/api/assistant/groups/-1001/avatar?bot_profile_id=tg-profile" {
		t.Fatalf("url = %q", got)
	}
	if got := consoleGroupAvatarURL("-1001", ""); got != "/api/assistant/groups/-1001/avatar" {
		t.Fatalf("url without profile = %q", got)
	}
	if got := consoleGroupAvatarURL("  ", "tg"); got != "" {
		t.Fatalf("empty group id should yield no url, got %q", got)
	}
}
