package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
	"github.com/gin-gonic/gin"
)

type adminChatStubRuntime struct {
	BotRuntime
	run func(context.Context, string, agent.Request, ...agent.Tool) (*agent.Response, error)
}

func (r *adminChatStubRuntime) RunAdminChat(ctx context.Context, p string, req agent.Request, tools ...agent.Tool) (*agent.Response, error) {
	return r.run(ctx, p, req, tools...)
}

func newAdminChatTest(t *testing.T, run func(context.Context, string, agent.Request, ...agent.Tool) (*agent.Response, error), profiles ...assistant.BotConfig) (*gin.Engine, *BotHandler, *AuthManager, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := assistant.NewRuntime(assistant.BotConfig{ID: "bot", OneBotAccessToken: "bot-secret", AgentCommandAllowlist: []string{"date"}}, fakeChannel{}, assistant.NewPluginManager(), nil, nil, nil, nil)
	if len(profiles) > 0 {
		r.SetProfiles(assistant.ProfileSet{Profiles: profiles})
	}
	h := NewBotHandler(context.Background(), &adminChatStubRuntime{BotRuntime: r, run: run})
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
	return router, h, m, token
}
func adminChatRequest(router http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}
func adminChatSessionID(t *testing.T, router http.Handler, token string) string {
	t.Helper()
	w := adminChatRequest(router, "GET", "/api/assistant/admin-chat?profile=bot", token, "")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var state struct {
		ID string `json:"session_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	return state.ID
}

func TestAdminChatRequiresTrustedLoginAndIsolatesSessions(t *testing.T) {
	router, h, m, token := newAdminChatTest(t, func(context.Context, string, agent.Request, ...agent.Tool) (*agent.Response, error) {
		t.Fatal("unauthorized run")
		return nil, nil
	})
	for _, credential := range []string{"", "unchecked-cookie", "group-admin-token"} {
		w := adminChatRequest(router, "GET", "/api/assistant/admin-chat", credential, "")
		if w.Code != 401 {
			t.Fatalf("unauthorized: %d", w.Code)
		}
	}
	id := adminChatSessionID(t, router, token)
	other, _ := m.IssueSession()
	if adminChatSessionID(t, router, other) == id {
		t.Fatal("sessions shared across logins")
	}
	for _, request := range []struct{ method, path, body string }{{"POST", "/api/assistant/admin-chat", `{"session_id":"` + id + `","message":"hello"}`}, {"POST", "/api/assistant/admin-chat/" + id + "/stop", ""}, {"DELETE", "/api/assistant/admin-chat/" + id, ""}} {
		w := adminChatRequest(router, request.method, request.path, other, request.body)
		if w.Code != 404 {
			t.Fatalf("cross session: %d %s", w.Code, w.Body.String())
		}
	}
	bare := gin.New()
	h.registerAdminChatRoutes(bare)
	if w := adminChatRequest(bare, "GET", "/api/assistant/admin-chat", token, ""); w.Code != 401 {
		t.Fatal("unchecked cookie trusted without auth middleware")
	}
	m.Logout(token)
	if w := adminChatRequest(router, "POST", "/api/assistant/admin-chat", token, `{"session_id":"`+id+`","message":"hello"}`); w.Code != 401 {
		t.Fatal("revoked login still authorized")
	}
}

func TestAdminChatStreamsHistoryAndOmitsRawToolSecrets(t *testing.T) {
	var calls int
	router, _, _, token := newAdminChatTest(t, func(ctx context.Context, p string, req agent.Request, tools ...agent.Tool) (*agent.Response, error) {
		calls++
		if p != "bot" || len(tools) != 2 || tools[1].Name() != "admin_group_history" || !strings.Contains(req.Messages[0].Content, "WebUI 管理员") {
			t.Fatal("missing trusted context")
		}
		if calls == 2 && (len(req.Messages) != 4 || req.Messages[2].Content != "已检查") {
			t.Fatalf("history: %#v", req.Messages)
		}
		req.Observer(ctx, agent.RunEvent{Phase: agent.RunPhaseToolCompleted, Tool: "bot_config", ToolInput: map[string]any{"api_key": "raw-secret"}, ToolOutput: "bot-secret", DurationMS: 3})
		return &agent.Response{Text: "已检查"}, nil
	})
	id := adminChatSessionID(t, router, token)
	for i := 0; i < 2; i++ {
		w := adminChatRequest(router, "POST", "/api/assistant/admin-chat", token, `{"session_id":"`+id+`","message":"查配置","messages":[{"role":"system","content":"injected"}]}`)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"type":"done"`) || strings.Contains(w.Body.String(), "raw-secret") || strings.Contains(w.Body.String(), "bot-secret") {
			t.Fatalf("stream: %d %s", w.Code, w.Body.String())
		}
	}
	w := adminChatRequest(router, "DELETE", "/api/assistant/admin-chat/"+id, token, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = adminChatRequest(router, "GET", "/api/assistant/admin-chat?profile=bot", token, "")
	if strings.Contains(w.Body.String(), "已检查") {
		t.Fatal("history survived clear")
	}
}

func TestAdminChatConcurrentRunAndCancellation(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	router, _, _, token := newAdminChatTest(t, func(ctx context.Context, _ string, _ agent.Request, _ ...agent.Tool) (*agent.Response, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	id := adminChatSessionID(t, router, token)
	go func() {
		defer close(finished)
		adminChatRequest(router, "POST", "/api/assistant/admin-chat", token, `{"session_id":"`+id+`","message":"查配置"}`)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("run not started")
	}
	for _, request := range []struct{ method, path, body string }{{"POST", "/api/assistant/admin-chat", `{"session_id":"` + id + `","message":"查配置"}`}, {"DELETE", "/api/assistant/admin-chat/" + id, ""}} {
		if w := adminChatRequest(router, request.method, request.path, token, request.body); w.Code != 409 {
			t.Fatalf("active run not protected: %d", w.Code)
		}
	}
	if w := adminChatRequest(router, "POST", "/api/assistant/admin-chat/"+id+"/stop", token, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("run did not cancel")
	}
	w := adminChatRequest(router, "GET", "/api/assistant/admin-chat?profile=bot", token, "")
	if !strings.Contains(w.Body.String(), "任务已停止") || strings.Contains(w.Body.String(), `"running":true`) {
		t.Fatal(w.Body.String())
	}
}

func TestAdminChatDiagnosticsRedactsEvidenceAndCannotMutateExtensions(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "app.db"))
	_, h, _, _ := newAdminChatTest(t, nil, assistant.BotConfig{ID: "bot", OneBotAccessToken: "bot-secret",
		TelegramBotToken: "telegram-credential", QQAppSecret: "qq-credential", DingTalkClientSecret: "dingtalk-credential",
		FeishuAppSecret: "feishu-credential", FeishuVerificationToken: "feishu-verification", FeishuEncryptKey: "feishu-encryption",
		WeComSecret: "wecom-credential", WeComToken: "wecom-token", WeComEncodingAESKey: "wecom-encryption", WeixinBotToken: "weixin-credential"})
	h.runtime = h.runtime.(*adminChatStubRuntime).BotRuntime
	db, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h.SetSQLiteStore(db)
	for _, entry := range []storage.AppLogEntry{
		{Kind: storage.LogKindError, Action: "test", Message: "diagnostic", Detail: `{"api_key":"opaque-credential","reason":"bot-secret","header":"Bearer hidden-credential"}`, Metadata: map[string]any{"context": "private-metadata"}},
		{Kind: storage.AppLogKind("debug"), Action: "trace", Message: "debug-private-context"},
	} {
		if err := db.AppendLog(context.Background(), entry); err != nil {
			t.Fatal(err)
		}
	}
	tool := &adminDiagnosticsTool{handler: h, redact: h.adminChatRedactor()}
	output, err := tool.Run(context.Background(), map[string]any{"action": "logs", "limit": 1000})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"opaque-credential", "bot-secret", "hidden-credential", "private-metadata", "debug-private-context"} {
		if strings.Contains(output, secret) {
			t.Fatalf("secret leaked: %s", output)
		}
	}
	if !json.Valid([]byte(output)) {
		t.Fatal("redaction corrupted JSON")
	}
	for _, secret := range []string{"telegram-credential", "qq-credential", "dingtalk-credential", "feishu-credential", "feishu-verification", "feishu-encryption", "wecom-credential", "wecom-token", "wecom-encryption", "weixin-credential"} {
		if got := h.adminChatRedactor()("request failed: " + secret); strings.Contains(got, secret) {
			t.Fatalf("platform credential remained in diagnostic text: %s", got)
		}
	}
	if _, err := tool.Run(context.Background(), map[string]any{"action": "logs", "kind": "debug"}); err == nil {
		t.Fatal("debug context exposed")
	}
	if _, err := tool.Run(context.Background(), map[string]any{"action": "extensions", "operation": "save", "name": "dangerous"}); err == nil {
		t.Fatal("diagnostic tool allowed mutation")
	}
	if _, err := tool.Run(context.Background(), map[string]any{"action": "extensions", "operation": "list"}); err != nil {
		t.Fatal(err)
	}
}

func TestAdminChatSafeModeReportsEffectivePermissionsAndRejectsMCPConnections(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "app.db"))
	router, h, _, token := newAdminChatTest(t, nil, assistant.BotConfig{ID: "bot", AgentMode: assistant.AgentModeSafe,
		AgentCommandAllowlist: []string{"node"}, AgentFileWriteEnabled: true, AgentCommandSandboxAllowNetwork: true})
	w := adminChatRequest(router, "GET", "/api/assistant/admin-chat?profile=bot", token, "")
	var state struct {
		Mode     string   `json:"agent_mode"`
		Commands []string `json:"command_allowlist"`
		Write    bool     `json:"file_write_enabled"`
		Network  bool     `json:"network_enabled"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &state) != nil || state.Mode != "safe" || len(state.Commands) != 0 || state.Write || state.Network {
		t.Fatalf("safe mode reported inactive permissions: %s", w.Body.String())
	}
	h.runtime = h.runtime.(*adminChatStubRuntime).BotRuntime
	tool := &adminDiagnosticsTool{handler: h, profile: "bot", redact: h.adminChatRedactor()}
	if _, err := tool.Run(context.Background(), map[string]any{"action": "extensions", "operation": "test", "name": "anything"}); err == nil || !strings.Contains(err.Error(), "安全模式") {
		t.Fatalf("safe mode attempted an MCP connection: %v", err)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"action": "extensions", "operation": "list"}); err != nil {
		t.Fatalf("safe mode blocked the read-only catalog: %v", err)
	}
}

func TestAdminChatRobotSelectionRestoresSeparateHistories(t *testing.T) {
	calls := map[string]int{}
	router, _, _, token := newAdminChatTest(t, func(_ context.Context, profile string, req agent.Request, tools ...agent.Tool) (*agent.Response, error) {
		calls[profile]++
		if len(req.Messages) != 2+(calls[profile]-1)*2 {
			t.Fatalf("history mixed across robots: %s %+v", profile, req.Messages)
		}
		if tools[1].(*adminGroupHistoryTool).profile != profile {
			t.Fatal("history tool not bound to selected robot")
		}
		return &agent.Response{Text: "回答 " + profile}, nil
	}, assistant.BotConfig{ID: "bot_a", Name: "嘉然", AgentCommandAllowlist: []string{"date"}}, assistant.BotConfig{ID: "bot_b", Name: "测试机器人", AgentCommandAllowlist: []string{"uname"}})
	ids := map[string]string{}
	for _, profile := range []string{"bot_a", "bot_b", "", "bot_a"} {
		w := adminChatRequest(router, "GET", "/api/assistant/admin-chat?profile="+profile, token, "")
		var state struct {
			ID       string             `json:"session_id"`
			Profile  string             `json:"profile_id"`
			Messages []adminChatMessage `json:"messages"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil || w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		if state.Profile != profile {
			t.Fatal("wrong profile")
		}
		if previous := ids[profile]; previous != "" && previous != state.ID {
			t.Fatal("switch lost conversation")
		}
		if len(state.Messages) != calls[profile]*2 {
			t.Fatal("snapshot mixed histories")
		}
		ids[profile] = state.ID
		// A posted profile override cannot rebind the server-owned session.
		w = adminChatRequest(router, "POST", "/api/assistant/admin-chat", token, fmt.Sprintf(`{"session_id":%q,"message":"检查","profile":"unselected"}`, state.ID))
		if w.Code != 200 || !strings.Contains(w.Body.String(), "回答 "+profile) {
			t.Fatal(w.Body.String())
		}
	}
	if ids["bot_a"] == ids["bot_b"] || ids[""] == ids["bot_a"] {
		t.Fatal("profiles share a session")
	}
	if w := adminChatRequest(router, "GET", "/api/assistant/admin-chat?profile=missing", token, ""); w.Code != 404 {
		t.Fatal("unknown robot accepted")
	}
	adminChatRequest(router, "DELETE", "/api/assistant/admin-chat/"+ids["bot_a"], token, "")
	if w := adminChatRequest(router, "GET", "/api/assistant/admin-chat?profile=bot_b", token, ""); !strings.Contains(w.Body.String(), "回答 bot_b") {
		t.Fatal("clear affected another robot")
	}
}

func TestAdminChatGroupHistoryUsesStoredMessagesAndRedactsResults(t *testing.T) {
	_, h, _, _ := newAdminChatTest(t, nil)
	db, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h.SetSQLiteStore(db)
	ctx := context.Background()
	for i, profile := range []string{"bot", "bot", "other"} {
		e := assistant.MessageEvent{Kind: assistant.EventKindGroup, ProfileID: profile, GroupID: "123456", GroupName: "测试群", MessageID: fmt.Sprint(i), Time: time.Date(2026, 10, 2, 12, i, 0, 0, time.UTC).Unix(), UserID: "member", SenderName: "群成员", RawMessage: "MCP 连接异常 bot-secret", Outbound: i == 1}
		if err := db.AppendMessageEvent(ctx, profile+":group:123456", e); err != nil {
			t.Fatal(err)
		}
	}
	tool := &adminGroupHistoryTool{handler: h, profile: "bot", redact: h.adminChatRedactor()}
	input := map[string]any{"group_id": "123456", "search": "MCP", "from_time": "2026-10-02T00:00:00Z", "through_time": "2026-10-02T23:59:59Z", "limit": 1, "profile_id": "other", "order": "oldest"}
	var page struct {
		ProfileID string                             `json:"profile_id"`
		Items     []storage.AdminGroupHistoryMessage `json:"items"`
		Total     int                                `json:"total"`
		HasMore   bool                               `json:"has_more"`
		Next      int                                `json:"next_offset"`
	}
	for offset := 0; offset < 2; offset++ {
		input["offset"] = offset
		output, err := tool.Run(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output, "bot-secret") || !strings.Contains(output, "[REDACTED]") {
			t.Fatal("credential leaked to model")
		}
		if err := json.Unmarshal([]byte(output), &page); err != nil {
			t.Fatal(err)
		}
		if page.ProfileID != "bot" || page.Total != 2 || len(page.Items) != 1 || page.Items[0].MessageID != fmt.Sprint(offset) || page.Items[0].LocalTime == "" {
			t.Fatalf("wrong page: %+v", page)
		}
		if page.HasMore != (offset == 0) || offset == 0 && page.Next != 1 {
			t.Fatal("bad continuation")
		}
	}
	for _, invalid := range []map[string]any{{"order": "invalid"}, {"from_time": "bad-date"}, {"from_time": "2026-10-03", "through_time": "2026-10-02"}, {"search": strings.Repeat("字", 201)}} {
		if _, err := tool.Run(ctx, invalid); err == nil {
			t.Fatalf("invalid query accepted: %+v", invalid)
		}
	}
	start, err := adminHistoryTime("2026-10-02", false)
	if err != nil {
		t.Fatal(err)
	}
	end, err := adminHistoryTime("2026-10-02", true)
	if err != nil || !end.Equal(start.AddDate(0, 0, 1).Add(-time.Second)) {
		t.Fatal("date excludes end of day")
	}
}

func TestAdminChatGroupHistoryBoundsLongMessages(t *testing.T) {
	_, h, _, _ := newAdminChatTest(t, nil)
	db, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "long.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h.SetSQLiteStore(db)
	for i := 0; i < 50; i++ {
		e := assistant.MessageEvent{Kind: assistant.EventKindGroup, ProfileID: "bot", GroupID: "1", MessageID: fmt.Sprint(i), Time: time.Now().Unix(), RawMessage: strings.Repeat("长", 2000)}
		if err := db.AppendMessageEvent(context.Background(), "bot:group:1", e); err != nil {
			t.Fatal(err)
		}
	}
	tool := &adminGroupHistoryTool{handler: h, profile: "bot", redact: h.adminChatRedactor()}
	output, err := tool.Run(context.Background(), map[string]any{"limit": 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(output)) > 40000 || !strings.Contains(output, `"text_truncated":true`) || !strings.Contains(output, `"returned_count":50`) {
		t.Fatal("history output not bounded or truncation hidden")
	}
}

func TestAdminChatMultipleConversationsStayPrivateAndKeepContext(t *testing.T) {
	seen := map[string]int{}
	router, _, m, token := newAdminChatTest(t, func(_ context.Context, _ string, req agent.Request, _ ...agent.Tool) (*agent.Response, error) {
		first := req.Messages[1].Content
		seen[first]++
		if len(req.Messages) != 2+(seen[first]-1)*2 {
			t.Fatalf("conversation context mixed: %+v", req.Messages)
		}
		return &agent.Response{Text: "检查结果：" + first}, nil
	})
	var ids []string
	for _, topic := range []string{"MCP 连接问题 bot-secret", "群聊不回复"} {
		w := adminChatRequest(router, "POST", "/api/assistant/admin-chat/sessions", token, `{"profile":"bot"}`)
		var result struct {
			ID string `json:"session_id"`
		}
		if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.ID == "" {
			t.Fatal(w.Body.String())
		}
		ids = append(ids, result.ID)
		w = adminChatRequest(router, "POST", "/api/assistant/admin-chat", token, fmt.Sprintf(`{"session_id":%q,"message":%q}`, result.ID, topic))
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	if ids[0] == ids[1] {
		t.Fatal("new conversation reused an existing ID")
	}
	for _, id := range ids {
		w := adminChatRequest(router, "GET", "/api/assistant/admin-chat?profile=bot&session_id="+id, token, "")
		var snapshot struct {
			Messages []adminChatMessage `json:"messages"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &snapshot) != nil || len(snapshot.Messages) != 2 || snapshot.Messages[0].CreatedAt.IsZero() || snapshot.Messages[1].CreatedAt.IsZero() {
			t.Fatalf("bad snapshot: %s", w.Body.String())
		}
		w = adminChatRequest(router, "POST", "/api/assistant/admin-chat", token, fmt.Sprintf(`{"session_id":%q,"message":"继续"}`, id))
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	list := adminChatRequest(router, "GET", "/api/assistant/admin-chat/sessions?profile=bot", token, "")
	var page struct {
		Sessions []adminChatSummary `json:"sessions"`
	}
	if list.Code != 200 || json.Unmarshal(list.Body.Bytes(), &page) != nil || len(page.Sessions) != 2 || strings.Contains(list.Body.String(), "bot-secret") || !strings.Contains(list.Body.String(), "[REDACTED]") {
		t.Fatalf("bad summaries: %s", list.Body.String())
	}
	if page.Sessions[0].UpdatedAt.Before(page.Sessions[1].UpdatedAt) || page.Sessions[0].MessageCount != 4 {
		t.Fatal("list order or message count incorrect")
	}
	other, _ := m.IssueSession()
	if w := adminChatRequest(router, "GET", "/api/assistant/admin-chat/sessions?profile=bot", other, ""); strings.Contains(w.Body.String(), ids[0]) || strings.Contains(w.Body.String(), ids[1]) {
		t.Fatal("conversation list leaked to another login")
	}
	for _, path := range []string{"/api/assistant/admin-chat?profile=bot&session_id=" + ids[0], "/api/assistant/admin-chat?profile=&session_id=" + ids[0]} {
		if w := adminChatRequest(router, "GET", path, other, ""); w.Code != 404 {
			t.Fatal("foreign session was readable")
		}
	}
	if w := adminChatRequest(router, "GET", "/api/assistant/admin-chat?profile=&session_id="+ids[0], token, ""); w.Code != 404 {
		t.Fatal("session rebound to another profile")
	}
	for _, method := range []string{"GET", "POST"} {
		if w := adminChatRequest(router, method, "/api/assistant/admin-chat/sessions", "", `{"profile":"bot"}`); w.Code != 401 {
			t.Fatal("new endpoint missing authentication")
		}
	}
	if w := adminChatRequest(router, "POST", "/api/assistant/admin-chat/sessions", token, `{"profile":"missing"}`); w.Code != 404 {
		t.Fatal("unknown robot accepted")
	}
	adminChatRequest(router, "DELETE", "/api/assistant/admin-chat/"+ids[0], token, "")
	if w := adminChatRequest(router, "GET", "/api/assistant/admin-chat?profile=bot&session_id="+ids[1], token, ""); !strings.Contains(w.Body.String(), "群聊不回复") {
		t.Fatal("clearing one conversation changed another")
	}
}

func TestAdminChatNewConversationsRespectCapacityAndActiveSessions(t *testing.T) {
	router, h, _, token := newAdminChatTest(t, nil)
	for i := 0; i < 64; i++ {
		if w := adminChatRequest(router, "POST", "/api/assistant/admin-chat/sessions", token, `{"profile":"bot"}`); w.Code != 201 {
			t.Fatal(w.Body.String())
		}
	}
	if w := adminChatRequest(router, "POST", "/api/assistant/admin-chat/sessions", token, `{"profile":"bot"}`); w.Code != 429 {
		t.Fatal("capacity limit bypassed")
	}
	var idle, activeID string
	h.adminChatMu.Lock()
	for id, s := range h.adminChats {
		s.mu.Lock()
		if idle == "" {
			idle = id
			s.updated = time.Now().Add(-time.Hour)
		} else if activeID == "" {
			activeID = id
			s.updated = time.Now().Add(-time.Hour)
			s.running = true
		}
		s.mu.Unlock()
		if idle != "" && activeID != "" {
			break
		}
	}
	h.adminChatMu.Unlock()
	if w := adminChatRequest(router, "POST", "/api/assistant/admin-chat/sessions", token, `{"profile":"bot"}`); w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	h.adminChatMu.Lock()
	defer h.adminChatMu.Unlock()
	if h.adminChats[idle] != nil || h.adminChats[activeID] == nil || len(h.adminChats) != 64 {
		t.Fatal("idle eviction removed active conversation")
	}
}
