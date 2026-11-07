// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

func newVoiceCallTest(t *testing.T, cfg assistant.BotConfig) (*gin.Engine, *BotHandler, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := assistant.NewRuntime(cfg, fakeChannel{}, assistant.NewPluginManager(), nil, nil, nil, nil)
	h := NewBotHandler(context.Background(), r)
	m := NewAuthManager(&memoryAuthStore{})
	if _, err := m.Bootstrap("", "test-password"); err != nil {
		t.Fatal(err)
	}
	token, err := m.IssueSession()
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(m.Middleware())
	h.Register(router)
	return router, h, token
}

func TestVoiceCallRequiresLogin(t *testing.T) {
	router, h, token := newVoiceCallTest(t, assistant.BotConfig{ID: "bot", OwnerID: "10001"})
	for _, path := range []string{"/api/assistant/call?profile=bot", "/api/assistant/call/ws?profile=bot"} {
		if w := adminChatRequest(router, "GET", path, "", ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s 没登录也能用：%d", path, w.Code)
		}
	}
	// 没过鉴权中间件的 Cookie 不能当成管理员。
	bare := gin.New()
	h.registerVoiceCallRoutes(bare)
	if w := adminChatRequest(bare, "GET", "/api/assistant/call/ws?profile=bot", token, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("未校验的 Cookie 被信任：%d", w.Code)
	}
}

func TestVoiceCallReportsMissingSlotsBeforeUpgrade(t *testing.T) {
	router, _, token := newVoiceCallTest(t, assistant.BotConfig{ID: "bot", OwnerID: "10001"})
	w := adminChatRequest(router, "GET", "/api/assistant/call?profile=bot", token, "")
	var info assistant.VoiceCallInfo
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &info) != nil {
		t.Fatalf("info: %d %s", w.Code, w.Body.String())
	}
	if info.BotID != "bot" || info.STT || info.TTS || info.Problem == "" {
		t.Fatalf("没配语音插槽时应说明原因：%#v", info)
	}
	// 打不了电话时不升级，直接回原因，客户端不用连上再断开才知道。
	w = adminChatRequest(router, "GET", "/api/assistant/call/ws?profile=bot", token, "")
	if w.Code != http.StatusConflict {
		t.Fatalf("ws: %d %s", w.Code, w.Body.String())
	}
	w = adminChatRequest(router, "GET", "/api/assistant/call?profile=missing", token, "")
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil || info.Problem != "机器人不存在" {
		t.Fatalf("不存在的机器人：%s", w.Body.String())
	}
}

// 连接意外断开的通话先留着：重连带同一个 ID 取回原来那通；对不上机器人不算；
// 到点没人取就当挂断。
func TestVoiceCallParkingResumesOrExpires(t *testing.T) {
	parking := &voiceCallParking{calls: map[string]*parkedVoiceCall{}, grace: time.Hour}
	call := &assistant.VoiceCall{}
	parking.park("c1", "bot", call)
	if got := parking.take("c1", "other"); got != nil {
		t.Fatal("换了机器人不该取回同一通")
	}
	if got := parking.take("c1", "bot"); got != call {
		t.Fatal("重连应取回原来那通电话")
	}
	if got := parking.take("c1", "bot"); got != nil {
		t.Fatal("取走后不该还在")
	}

	parking.grace = 20 * time.Millisecond
	parking.park("c2", "bot", &assistant.VoiceCall{})
	time.Sleep(100 * time.Millisecond)
	if got := parking.take("c2", "bot"); got != nil {
		t.Fatal("保留期过了不该还能取回")
	}
}
