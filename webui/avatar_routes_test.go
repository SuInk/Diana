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

type memberAvatarStubRuntime struct {
	BotRuntime
	avatar     assistant.GroupAvatar
	found      bool
	gotProfile string
	gotUser    string
}

func (r *memberAvatarStubRuntime) Status() assistant.RuntimeStatus { return assistant.RuntimeStatus{} }

func (r *memberAvatarStubRuntime) CallOneBotAPI(context.Context, string, map[string]any) (map[string]any, error) {
	return map[string]any{"data": []any{}}, nil
}

func (r *memberAvatarStubRuntime) MemberAvatarForProfile(_ context.Context, profileID, userID string) (assistant.GroupAvatar, bool) {
	r.gotProfile = profileID
	r.gotUser = userID
	return r.avatar, r.found
}

func TestAvatarEndpointServesTelegramMemberAvatar(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3, 4}
	runtime := &memberAvatarStubRuntime{avatar: assistant.GroupAvatar{Data: png, ContentType: "image/png"}, found: true}
	router, _ := newAvatarEndpointRouter(runtime)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/avatars/member/12345?bot_profile_id=tg", nil))
	if rec.Code != http.StatusOK || rec.Body.Len() != len(png) {
		t.Fatalf("status=%d body=%d", rec.Code, rec.Body.Len())
	}
	if runtime.gotProfile != "tg" || runtime.gotUser != "12345" {
		t.Fatalf("profile=%q user=%q", runtime.gotProfile, runtime.gotUser)
	}
	if strings.Contains(rec.Body.String(), "bot-token") {
		t.Fatal("response leaked Telegram credentials")
	}
}

type profileAvatarStatusRuntime struct {
	BotRuntime
	status assistant.RuntimeStatus
}

func (r *profileAvatarStatusRuntime) Status() assistant.RuntimeStatus { return r.status }

func TestConfigPayloadGetsLocalTelegramAvatarURL(t *testing.T) {
	runtime := &profileAvatarStatusRuntime{status: assistant.RuntimeStatus{Channels: []assistant.ChannelStatus{{ProfileID: "tg", Platform: assistant.PlatformTelegram, SelfID: "12345"}}}}
	handler := &BotHandler{runtime: runtime}
	payload := assistant.ConfigPayload{
		ID:       "tg",
		Platform: assistant.PlatformTelegram,
		Profiles: []assistant.ConfigPayload{
			{ID: "qq", Platform: assistant.PlatformOneBotV11, BotAccount: "10001"},
			{ID: "custom", Platform: assistant.PlatformTelegram, BotAccount: "22222", AvatarURL: "/custom-avatar.png"},
			{ID: "tg", Platform: assistant.PlatformTelegram},
		},
	}
	got := handler.withConsoleProfileAvatars(payload)
	if got.AvatarURL != "/api/assistant/avatars/member/12345?bot_profile_id=tg" {
		t.Fatalf("focused avatar URL = %q", got.AvatarURL)
	}
	if got.Profiles[0].AvatarURL != "/api/assistant/avatars/member/10001?bot_profile_id=qq" {
		t.Fatalf("OneBot avatar URL = %q", got.Profiles[0].AvatarURL)
	}
	if got.Profiles[1].AvatarURL != "/custom-avatar.png" {
		t.Fatalf("configured avatar URL was replaced: %q", got.Profiles[1].AvatarURL)
	}
	if got.Profiles[2].AvatarURL != "/api/assistant/avatars/member/12345?bot_profile_id=tg" {
		t.Fatalf("Telegram avatar URL = %q", got.Profiles[2].AvatarURL)
	}
	for _, avatarURL := range []string{got.AvatarURL, got.Profiles[0].AvatarURL, got.Profiles[1].AvatarURL, got.Profiles[2].AvatarURL} {
		if strings.Contains(avatarURL, "api.telegram.org") || strings.Contains(avatarURL, "bot-token") {
			t.Fatalf("avatar URL leaked upstream credentials: %q", avatarURL)
		}
	}
}
