package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// hostReviewStubProvider 冒充审核模型：记下请求，按预设放行、拒绝或报错。
type hostReviewStubProvider struct {
	mu       sync.Mutex
	reply    string
	err      error
	requests []llm.GenerateRequest
}

func (p *hostReviewStubProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req)
	if p.err != nil {
		return nil, p.err
	}
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: p.reply}, nil
}

func (p *hostReviewStubProvider) lastUserContent() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) == 0 {
		return ""
	}
	messages := p.requests[len(p.requests)-1].Messages
	return messages[len(messages)-1].Content
}

func hostAccessReviewRuntime(t *testing.T, provider *hostReviewStubProvider) *Runtime {
	t.Helper()
	return NewRuntime(BotConfig{OwnerID: "100001", BotAccount: "200002", Platform: PlatformOneBotV11}, nilChannel{}, NewDefaultPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
}

func requireUnixShell(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("Unix fixture")
	}
}

func TestHostAccessReviewDeniesCommandWithoutRunningIt(t *testing.T) {
	requireUnixShell(t)
	root := t.TempDir()
	marker := filepath.Join(root, "should-not-exist")
	provider := &hostReviewStubProvider{reply: "```json\n{\"risk\":\"high\",\"user_authorization\":\"low\",\"outcome\":\"deny\",\"reason\":\"超出请求范围\"}\n```"}
	r := hostAccessReviewRuntime(t, provider)
	tool := &hostAccessTool{runtime: r, event: privateEvent("100001", "20401", "帮我看看磁盘")}
	_, err := tool.Run(context.Background(), map[string]any{"operation": "exec", "command": "/bin/sh", "args": []string{"-c", `touch "$1"`, "probe", marker}, "cwd": root, "purpose": "查看磁盘"})
	var reviewErr *hostCommandReviewError
	if !errors.As(err, &reviewErr) || !strings.Contains(err.Error(), "超出请求范围") || !strings.Contains(err.Error(), "high") || !strings.Contains(err.Error(), "不要换用其他工具") {
		t.Fatalf("expected review denial, got %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("denied command still ran")
	}
	// 审核模型要看到用户原话、执行方的解释和解析后的真实命令。
	var payload map[string]any
	content := provider.lastUserContent()
	if err := json.Unmarshal([]byte(content[strings.Index(content, "{"):]), &payload); err != nil {
		t.Fatalf("review payload: %v (%s)", err, content)
	}
	if payload["user_request"] != "帮我看看磁盘" || payload["purpose"] != "查看磁盘" || payload["command"] == "" {
		t.Fatalf("review payload missing context: %v", payload)
	}
}

func TestHostAccessReviewFailureFailsClosed(t *testing.T) {
	requireUnixShell(t)
	root := t.TempDir()
	marker := filepath.Join(root, "should-not-exist")
	for name, provider := range map[string]*hostReviewStubProvider{
		"model error": {err: errors.New("upstream down")},
		"bad json":    {reply: "放行吧"},
		"no decision": {reply: `{"risk":"low"}`},
		"bad level":   {reply: `{"risk":"weird","user_authorization":"high","outcome":"allow"}`},
	} {
		t.Run(name, func(t *testing.T) {
			tool := &hostAccessTool{runtime: hostAccessReviewRuntime(t, provider), event: privateEvent("100001", "20402", "建个文件")}
			_, err := tool.Run(context.Background(), map[string]any{"operation": "exec", "command": "/bin/sh", "args": []string{"-c", `touch "$1"`, "probe", marker}, "cwd": root, "purpose": "建文件"})
			if err == nil || !strings.Contains(err.Error(), "命令审核未能完成") || !strings.Contains(err.Error(), "不代表命令危险") {
				t.Fatalf("expected fail-closed error, got %v", err)
			}
			if _, statErr := os.Stat(marker); statErr == nil {
				t.Fatal("command ran without a review decision")
			}
			// 格式不对的结果重试一次，上游错误不在这里叠加重试。
			if want := 2; provider.err == nil && len(provider.requests) != want {
				t.Fatalf("review attempts = %d, want %d", len(provider.requests), want)
			}
		})
	}
}

func TestHostAccessExecRequiresPurpose(t *testing.T) {
	provider := &hostReviewStubProvider{reply: hostReviewAllowReply}
	tool := &hostAccessTool{runtime: hostAccessReviewRuntime(t, provider), event: privateEvent("100001", "20403", "跑个命令")}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "exec", "command": "/bin/sh", "args": []string{"-c", "true"}, "cwd": t.TempDir()}); err == nil || !strings.Contains(err.Error(), "purpose") {
		t.Fatalf("missing purpose accepted: %v", err)
	}
	if len(provider.requests) != 0 {
		t.Fatal("review model called for an invalid request")
	}
}

func TestAdminChatReviewUsesLastUserMessage(t *testing.T) {
	ctx := withHostCommandRequest(context.Background(), lastUserMessageText([]llm.Message{
		{Role: llm.RoleUser, Content: "旧问题"},
		{Role: llm.RoleAssistant, Content: "回答"},
		{Role: llm.RoleUser, Content: "清一下 /tmp 里的构建产物"},
	}))
	if got := hostCommandRequestFromContext(ctx, MessageEvent{}); got != "清一下 /tmp 里的构建产物" {
		t.Fatalf("request = %q", got)
	}
}

const hostReviewAllowReply = `{"risk":"low","user_authorization":"high","outcome":"allow","reason":"测试放行"}`

func TestParseHostCommandReviewToleratesProse(t *testing.T) {
	decision, err := parseHostCommandReview("结论如下：{\"risk\":\"LOW\",\"user_authorization\":\"Medium\",\"outcome\":\"allow\",\"reason\":\"常规查询\"} 以上")
	if err != nil || !decision.Allow || decision.Risk != "low" || decision.Authorization != "medium" {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
}

// 阈值在代码里兜底：模型给了 allow 也越不过 critical 和「high 且授权不足」。
func TestParseHostCommandReviewEnforcesThresholds(t *testing.T) {
	for _, test := range []struct {
		risk, auth, outcome string
		allow               bool
	}{
		{"low", "unknown", "allow", true},
		{"medium", "low", "allow", true},
		{"low", "high", "deny", false}, // 低风险也可能因恶意注入被拒，模型的 deny 照样生效
		{"high", "medium", "allow", true},
		{"high", "low", "allow", false},
		{"high", "unknown", "allow", false},
		{"critical", "high", "allow", false},
	} {
		raw := fmt.Sprintf(`{"risk":%q,"user_authorization":%q,"outcome":%q,"reason":"r"}`, test.risk, test.auth, test.outcome)
		decision, err := parseHostCommandReview(raw)
		if err != nil || decision.Allow != test.allow {
			t.Fatalf("%s/%s/%s: allow=%v err=%v", test.risk, test.auth, test.outcome, decision.Allow, err)
		}
	}
}

func TestHostReviewBreakerStopsRetriesAfterRepeatedDenials(t *testing.T) {
	requireUnixShell(t)
	root := t.TempDir()
	provider := &hostReviewStubProvider{reply: `{"risk":"high","user_authorization":"unknown","outcome":"deny","reason":"与任务无关"}`}
	r := hostAccessReviewRuntime(t, provider)
	tool := &hostAccessTool{runtime: r, event: privateEvent("100001", "20411", "看看日志")}
	run := func() error {
		_, err := tool.Run(context.Background(), map[string]any{"operation": "exec", "command": "/bin/sh", "args": []string{"-c", "true"}, "cwd": root, "purpose": "看日志"})
		return err
	}
	for i := 1; i <= hostReviewMaxConsecutiveDenials; i++ {
		err := run()
		if err == nil || !strings.Contains(err.Error(), "未通过自动审核") {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if tripped := strings.Contains(err.Error(), "审核拒绝上限"); tripped != (i == hostReviewMaxConsecutiveDenials) {
			t.Fatalf("attempt %d tripped=%v", i, tripped)
		}
	}
	err := run()
	if err == nil || !strings.Contains(err.Error(), "已停止执行后续命令") {
		t.Fatalf("breaker did not stop: %v", err)
	}
	if len(provider.requests) != hostReviewMaxConsecutiveDenials {
		t.Fatalf("reviewer called %d times after breaker opened", len(provider.requests))
	}
	// 主人发新消息是新的一轮，熔断不延续。
	provider.reply = hostReviewAllowReply
	tool.event = privateEvent("100001", "20412", "那就看看磁盘")
	if err := run(); err != nil {
		t.Fatalf("new turn still blocked: %v", err)
	}
}

func TestHostReviewBreakerCountsRecentDenials(t *testing.T) {
	var breaker hostReviewBreaker
	now := time.Now()
	for i := 0; i < hostReviewMaxRecentDenials-1; i++ {
		if breaker.record("k", true, now) && i < hostReviewMaxConsecutiveDenials-1 {
			t.Fatal("opened too early")
		}
		breaker.record("k", false, now)
	}
	if breaker.isOpen("k") {
		t.Fatal("alternating denials below the window limit opened the breaker")
	}
	if !breaker.record("k", true, now) {
		t.Fatal("10 denials in the window did not open the breaker")
	}
}

func TestHostReviewIncludesRecentConversationWithTrust(t *testing.T) {
	requireUnixShell(t)
	provider := &hostReviewStubProvider{reply: hostReviewAllowReply}
	r := hostAccessReviewRuntime(t, provider)
	current := privateEvent("100001", "20415", "确认，删吧")
	current.SelfID = "200002"
	r.mu.Lock()
	r.history[sessionKey(current)] = []MessageEvent{
		privateEvent("100001", "20413", "把 /tmp/diana-build 删掉"),
		{Kind: EventKindPrivate, UserID: "200002", MessageID: "20414", RawMessage: "这会删除整个构建目录，确认吗？"},
		current,
	}
	r.mu.Unlock()
	tool := &hostAccessTool{runtime: r, event: current}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "exec", "command": "/bin/sh", "args": []string{"-c", "true"}, "cwd": t.TempDir(), "purpose": "删除构建目录"}); err != nil {
		t.Fatal(err)
	}
	content := provider.lastUserContent()
	var payload struct {
		Request string           `json:"user_request"`
		Turns   []hostReviewTurn `json:"recent_conversation"`
	}
	if err := json.Unmarshal([]byte(content[strings.Index(content, "{"):]), &payload); err != nil {
		t.Fatal(err)
	}
	want := []hostReviewTurn{
		{Speaker: "owner", Trusted: true, Text: "把 /tmp/diana-build 删掉"},
		{Speaker: "assistant", Text: "这会删除整个构建目录，确认吗？"},
	}
	if payload.Request != "确认，删吧" || !reflect.DeepEqual(payload.Turns, want) {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestAdminChatReviewTranscriptMarksTrust(t *testing.T) {
	turns := adminChatReviewTranscript([]llm.Message{
		{Role: llm.RoleUser, Content: "把旧日志清掉"},
		{Role: llm.RoleAssistant, Content: "会删除 30 天前的日志，确认吗？"},
		{Role: llm.RoleUser, Content: "确认"},
	})
	want := []hostReviewTurn{
		{Speaker: "owner", Trusted: true, Text: "把旧日志清掉"},
		{Speaker: "assistant", Text: "会删除 30 天前的日志，确认吗？"},
	}
	if !reflect.DeepEqual(turns, want) {
		t.Fatalf("turns = %+v", turns)
	}
}

func TestHostAccessCleanupReportsDianaStorage(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	tool := &hostAccessTool{runtime: hostAccessTestRuntime(t), event: privateEvent("100001", "20409", "清理一下存储")}
	out, err := tool.Run(context.Background(), map[string]any{"operation": "cleanup"})
	if err != nil {
		t.Fatal(err)
	}
	var report StorageCleanupReport
	if err := json.Unmarshal([]byte(out), &report); err != nil || report.Freed == "" || report.Steps["temp_dirs"] == nil {
		t.Fatalf("report=%s err=%v", out, err)
	}
}
