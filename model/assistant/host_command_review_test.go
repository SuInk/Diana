package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
	provider := &hostReviewStubProvider{reply: "```json\n{\"allow\":false,\"risk\":\"high\",\"reason\":\"超出请求范围\"}\n```"}
	r := hostAccessReviewRuntime(t, provider)
	tool := &hostAccessTool{runtime: r, event: privateEvent("100001", "20401", "帮我看看磁盘")}
	_, err := tool.Run(context.Background(), map[string]any{"operation": "exec", "command": "/bin/sh", "args": []string{"-c", `touch "$1"`, "probe", marker}, "cwd": root, "purpose": "查看磁盘"})
	var reviewErr *hostCommandReviewError
	if !errors.As(err, &reviewErr) || !strings.Contains(err.Error(), "超出请求范围") || !strings.Contains(err.Error(), "high") {
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
	} {
		t.Run(name, func(t *testing.T) {
			tool := &hostAccessTool{runtime: hostAccessReviewRuntime(t, provider), event: privateEvent("100001", "20402", "建个文件")}
			_, err := tool.Run(context.Background(), map[string]any{"operation": "exec", "command": "/bin/sh", "args": []string{"-c", `touch "$1"`, "probe", marker}, "cwd": root, "purpose": "建文件"})
			if err == nil || !strings.Contains(err.Error(), "命令审核不可用") {
				t.Fatalf("expected fail-closed error, got %v", err)
			}
			if _, statErr := os.Stat(marker); statErr == nil {
				t.Fatal("command ran without a review decision")
			}
		})
	}
}

func TestHostAccessExecRequiresPurpose(t *testing.T) {
	provider := &hostReviewStubProvider{reply: `{"allow":true,"risk":"low","reason":"ok"}`}
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

func TestParseHostCommandReviewToleratesProse(t *testing.T) {
	decision, err := parseHostCommandReview("结论如下：{\"allow\":true,\"risk\":\"LOW\",\"reason\":\"常规查询\"} 以上")
	if err != nil || !decision.Allow || decision.Risk != "low" {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	decision, err = parseHostCommandReview(`{"allow":false,"risk":"weird","reason":""}`)
	if err != nil || decision.Allow || decision.Risk != "unknown" {
		t.Fatalf("decision=%+v err=%v", decision, err)
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
