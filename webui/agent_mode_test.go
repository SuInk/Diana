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
	// ListLogs 新的在前；下面新建机器人还会再记一条，先把这条留着比对。
	entry := changes[0]
	if entry.Actor != "admin" || entry.Target != current.ID || !strings.Contains(entry.Message, "标准模式") || !strings.Contains(entry.Message, "安全模式") {
		t.Fatalf("模式变更日志 = %#v", entry)
	}
	if entry.Metadata["agent_mode_from"] != assistant.AgentModeStandard || entry.Metadata["agent_mode_to"] != assistant.AgentModeSafe {
		t.Fatalf("模式变更日志元数据 = %#v", entry.Metadata)
	}

	// 旧版前端新建机器人只带 agent_enabled=true：按默认的标准模式建，并同样记一条日志。
	raw := []byte(`{"name":"legacy-create","platform":"onebot-v11","onebot_reverse_ws_endpoint":"ws://127.0.0.1:18081/onebot/v11/ws","agent_enabled":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/assistant/config/new", bytes.NewReader(raw))
	req.Header.Set("X-Diana-Actor", "admin")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	profiles := handler.profiles.Profiles().Profiles
	created := profiles[len(profiles)-1]
	if created.Name != "legacy-create" || created.AgentMode != assistant.AgentModeStandard {
		t.Fatalf("旧前端新建的机器人 name=%q mode=%q", created.Name, created.AgentMode)
	}
	operations, err = logStore.ListLogs(ctx, storage.AppLogFilter{Kind: storage.LogKindOperation, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range operations {
		if entry.Action == "agent_mode_change" && entry.Target == created.ID && strings.Contains(entry.Message, "新建机器人") && entry.Metadata["agent_mode_to"] == assistant.AgentModeStandard {
			found = true
		}
	}
	if !found {
		t.Fatalf("新建机器人没有记模式日志: %#v", operations)
	}

	// 写错的模式值直接 400，不悄悄换成某一档。
	payload := assistant.PayloadFromConfig(current)
	payload.AgentMode = "Standrd"
	raw, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/assistant/config", bytes.NewReader(raw)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "agent_mode") {
		t.Fatalf("写错的模式 status=%d body=%s", rec.Code, rec.Body.String())
	}
	// 新建时写错同样 400，不会按默认的标准模式建出来。
	before := len(handler.profiles.Profiles().Profiles)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/assistant/config/new", bytes.NewReader([]byte(`{"name":"typo-create","platform":"onebot-v11","onebot_reverse_ws_endpoint":"ws://127.0.0.1:18082/onebot/v11/ws","agent_mode":"unsafe"}`))))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "agent_mode") {
		t.Fatalf("新建写错的模式 status=%d body=%s", rec.Code, rec.Body.String())
	}
	if after := len(handler.profiles.Profiles().Profiles); after != before {
		t.Fatalf("写错模式的新建请求仍建出了机器人：%d → %d", before, after)
	}

	// 复制出的机器人沿用源机器人的模式，也记一条。
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/assistant/config/clone", bytes.NewReader([]byte(`{"id":"`+current.ID+`"}`))))
	if rec.Code != http.StatusOK {
		t.Fatalf("clone status=%d body=%s", rec.Code, rec.Body.String())
	}
	operations, err = logStore.ListLogs(ctx, storage.AppLogFilter{Kind: storage.LogKindOperation, Limit: 30})
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, entry := range operations {
		if entry.Action == "agent_mode_change" && strings.Contains(entry.Message, "复制出的机器人") && entry.Metadata["agent_mode_to"] == assistant.AgentModeSafe {
			found = true
		}
	}
	if !found {
		t.Fatalf("复制机器人没有记模式日志: %#v", operations)
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
