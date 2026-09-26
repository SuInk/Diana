// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestVRChatStatusEndpoint(t *testing.T) {
	runtime := assistant.NewRuntime(assistant.DefaultBotConfig(), fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	router := botTestRouter(NewBotHandler(context.Background(), runtime))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/assistant/plugins/vrchat/status", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d %s", response.Code, response.Body.String())
	}
	var status assistant.VRChatStatus
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	// 插件默认关闭，桥不该在跑。
	if status.Enabled || status.Listening {
		t.Fatalf("status = %+v", status)
	}
}
