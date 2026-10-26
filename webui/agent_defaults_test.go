// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/SuInk/diana/model/assistant"
)

func serveAgentDefaults(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/assistant/agent-defaults", (&BotHandler{}).agentDefaults)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/assistant/agent-defaults", nil))
	return recorder
}

// 推荐默认值必须和 DefaultBotConfig 是同一份，不能是接口里另抄的一份——
// 抄一份迟早对不上，而对不上的表现是用户照着填完仍然不生效。
func TestAgentDefaultsMirrorsDefaultBotConfig(t *testing.T) {
	recorder := serveAgentDefaults(t)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var payload struct {
		Allowlist  []string `json:"agent_command_allowlist"`
		WriteEnabl bool     `json:"agent_file_write_enabled"`
		Sandbox    string   `json:"agent_command_sandbox"`
		MaxTasks   int      `json:"subtask_max_tasks"`
		MaxWorkers int      `json:"subtask_max_concurrency"`
		TimeoutMS  int      `json:"agent_command_timeout_ms"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	defaults := assistant.DefaultBotConfig()
	if payload.Allowlist != nil {
		t.Fatalf("removed command allowlist still served: %#v", payload.Allowlist)
	}
	if payload.WriteEnabl != defaults.AgentFileWriteEnabled {
		t.Fatalf("file write = %v, want %v", payload.WriteEnabl, defaults.AgentFileWriteEnabled)
	}
	if payload.Sandbox != defaults.AgentCommandSandbox {
		t.Fatalf("sandbox = %q, want %q", payload.Sandbox, defaults.AgentCommandSandbox)
	}
	if payload.TimeoutMS != defaults.AgentCommandTimeoutMS {
		t.Fatalf("timeout = %d", payload.TimeoutMS)
	}

	if payload.MaxTasks != defaults.SubtaskMaxTasks || payload.MaxWorkers != defaults.SubtaskMaxConcurrency {
		t.Fatalf("subtask tasks/concurrency = %d/%d, want %d/%d",
			payload.MaxTasks, payload.MaxWorkers, defaults.SubtaskMaxTasks, defaults.SubtaskMaxConcurrency)
	}
}

// 这个接口只读，不能顺手把配置改了：真正的授权动作发生在用户点保存的那一刻。
func TestAgentDefaultsDoesNotMutateAnything(t *testing.T) {
	before := assistant.DefaultBotConfig()
	serveAgentDefaults(t)
	after := assistant.DefaultBotConfig()
	if before.AgentCommandSandbox != after.AgentCommandSandbox || before.AgentFileWriteEnabled != after.AgentFileWriteEnabled {
		t.Fatal("读一次默认值把默认值改了")
	}
}
