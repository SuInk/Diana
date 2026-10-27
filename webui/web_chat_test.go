// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestWebChatRequiresLogin(t *testing.T) {
	router, _, _ := newVoiceCallTest(t, assistant.BotConfig{ID: "bot", OwnerID: "10001"})
	if w := adminChatRequest(router, "GET", "/api/assistant/chat?profile=bot", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("没登录也能读私聊记录：%d", w.Code)
	}
	if w := adminChatRequest(router, "POST", "/api/assistant/chat", "", `{"profile":"bot","text":"hi"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("没登录也能发消息：%d", w.Code)
	}
}

func TestWebChatHistoryAndProblems(t *testing.T) {
	router, _, token := newVoiceCallTest(t, assistant.BotConfig{ID: "bot", OwnerID: "10001"})
	w := adminChatRequest(router, "GET", "/api/assistant/chat?profile=bot", token, "")
	var body struct {
		Info     assistant.WebChatInfo      `json:"info"`
		Messages []assistant.WebChatMessage `json:"messages"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatalf("history: %d %s", w.Code, w.Body.String())
	}
	if body.Info.BotID != "bot" || body.Info.Problem != "" || body.Messages == nil {
		t.Fatalf("history = %#v", body)
	}
	if w := adminChatRequest(router, "POST", "/api/assistant/chat", token, `{"profile":"bot","text":"  "}`); w.Code != http.StatusBadRequest {
		t.Fatalf("空消息：%d", w.Code)
	}

	// 聊不了时不开流，直接回原因。
	if w := adminChatRequest(router, "POST", "/api/assistant/chat", token, `{"profile":"missing","text":"hi"}`); w.Code != http.StatusConflict {
		t.Fatalf("不存在的机器人发消息：%d %s", w.Code, w.Body.String())
	}
}
