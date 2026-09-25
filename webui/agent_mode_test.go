// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

// 切换 Agent 模式要单独留一条操作日志，写明谁、从哪个模式切到哪个；只改别的设置
// 不该多出这一条。
func TestSavingAgentModeChangeWritesAuditLog(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logStore, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logStore.Close() }()

	existing := assistant.DefaultBotConfig()
	existing.AgentMode = assistant.AgentModeStandard
	runtime := assistant.NewRuntime(existing, fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewBotHandlerWithFactory(ctx, runtime, func(assistant.BotConfig) assistant.Channel { return fakeChannel{} })
	handler.SetSQLiteStore(logStore)
	router := botTestRouter(handler)
	current := handler.profiles.Profiles().Profiles[0]

	save := func(mutate func(*assistant.ConfigPayload)) {
		t.Helper()
		payload := assistant.PayloadFromConfig(current)
		mutate(&payload)
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/assistant/config", bytes.NewReader(raw))
		req.Header.Set("X-Diana-Actor", "admin")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("save status=%d body=%s", rec.Code, rec.Body.String())
		}
		current, _ = handler.profiles.Profiles().ConfigForProfile(current.ID)
	}

	save(func(p *assistant.ConfigPayload) { p.Name = "renamed" })
	save(func(p *assistant.ConfigPayload) { p.AgentMode = assistant.AgentModeSafe })
	if current.AgentMode != assistant.AgentModeSafe {
		t.Fatalf("保存后模式 = %q", current.AgentMode)
	}

	operations, err := logStore.ListLogs(ctx, storage.AppLogFilter{Kind: storage.LogKindOperation, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	var changes []storage.AppLogEntry
	for _, entry := range operations {
		if entry.Action == "agent_mode_change" {
			changes = append(changes, entry)
		}
	}
	if len(changes) != 1 {
		t.Fatalf("模式变更日志 %d 条，want 1: %#v", len(changes), operations)
	}
	entry := changes[0]
	if entry.Actor != "admin" || entry.Target != current.ID || !strings.Contains(entry.Message, "标准模式") || !strings.Contains(entry.Message, "安全模式") {
		t.Fatalf("模式变更日志 = %#v", entry)
	}
	if entry.Metadata["agent_mode_from"] != assistant.AgentModeStandard || entry.Metadata["agent_mode_to"] != assistant.AgentModeSafe {
		t.Fatalf("模式变更日志元数据 = %#v", entry.Metadata)
	}
}

// 界面的安全模式说明来自后端规则表，推荐默认值接口要把整张表带出去；影响查询接口
// 在拿不到编码任务时按 0 返回，不报错。
func TestAgentDefaultsCarrySafeModeCatalog(t *testing.T) {
	runtime := assistant.NewRuntime(assistant.DefaultBotConfig(), fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	router := botTestRouter(NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel { return fakeChannel{} }))

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/agent-defaults", nil))
	var defaults struct {
		SafeMode []assistant.AgentSafeModeCatalogCategory `json:"agent_safe_mode"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &defaults); err != nil {
		t.Fatal(err)
	}
	if len(defaults.SafeMode) != len(assistant.AgentSafeModeCategories) {
		t.Fatalf("安全模式目录 = %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/agent-mode/impact?profile=missing", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"running_coding_jobs":0`) {
		t.Fatalf("impact status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// 演示站没有后端，安全模式目录是前端仓库里的一份 JSON，必须和规则表逐字相同。
// 规则表一变这个测试就失败，按提示重新生成：
//
//	DIANA_UPDATE_DEMO_CATALOG=1 go test ./webui -run TestDemoAgentSafeModeCatalogInSync
func TestDemoAgentSafeModeCatalogInSync(t *testing.T) {
	path := filepath.Join("..", "frontend-next", "src", "demo-agent-safe-mode.json")
	want, err := json.MarshalIndent(assistant.AgentSafeModeCatalog(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if os.Getenv("DIANA_UPDATE_DEMO_CATALOG") == "1" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s 和安全模式规则表不一致，运行 DIANA_UPDATE_DEMO_CATALOG=1 go test ./webui -run TestDemoAgentSafeModeCatalogInSync 重新生成", path)
	}
}
