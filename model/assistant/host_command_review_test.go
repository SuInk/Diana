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

func TestOwnerShellCommandIsReviewedAndOwnerOnly(t *testing.T) {
	requireUnixShell(t)
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	provider := &hostReviewStubProvider{reply: `{"allow":true,"risk":"low","reason":"按原文执行"}`}
	r := hostAccessReviewRuntime(t, provider)
	text := "执行命令 printf owner-shell-ok"
	reply, handled := r.handleOwnerCommand(privateEvent("100001", "20404", text), text)
	if !handled || !strings.Contains(reply, "退出码 0") || !strings.Contains(reply, "owner-shell-ok") {
		t.Fatalf("handled=%v reply=%q", handled, reply)
	}
	if !strings.Contains(provider.lastUserContent(), "执行命令 printf owner-shell-ok") {
		t.Fatal("review did not see the owner's command text")
	}
	if _, handled := r.handleOwnerCommand(privateEvent("300003", "20405", text), text); handled {
		t.Fatal("non-owner ran a shell command")
	}
	provider.reply = `{"allow":false,"risk":"critical","reason":"会删除家目录"}`
	reply, _ = r.handleOwnerCommand(privateEvent("100001", "20406", "执行命令 rm -rf ~"), "执行命令 rm -rf ~")
	if !strings.Contains(reply, "未通过自动审核") || !strings.Contains(reply, "会删除家目录") {
		t.Fatalf("denied reply = %q", reply)
	}
	// 「执行 一下」这种自然语言不是命令。
	if r.wouldHandleOwnerCommand(privateEvent("100001", "20407", "执行 一下刚才的任务"), "执行 一下刚才的任务") {
		t.Fatal("natural language treated as a shell command")
	}
}

func TestOwnerShellCommandHonorsSafeMode(t *testing.T) {
	provider := &hostReviewStubProvider{reply: `{"allow":true,"risk":"low","reason":"ok"}`}
	r := NewRuntime(BotConfig{OwnerID: "100001", AgentMode: AgentModeSafe}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	for _, text := range []string{"执行命令 true", "清理 存储"} {
		reply, handled := r.handleOwnerCommand(privateEvent("100001", "20408", text), text)
		if !handled || !strings.Contains(reply, "安全模式") {
			t.Fatalf("%s: handled=%v reply=%q", text, handled, reply)
		}
	}
	if len(provider.requests) != 0 {
		t.Fatal("safe mode still called the review model")
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
	reply, handled := hostAccessTestRuntime(t).handleOwnerCommand(privateEvent("100001", "20410", "清理 存储"), "清理 存储")
	if !handled || !strings.Contains(reply, "已清理 Diana 自身存储") {
		t.Fatalf("handled=%v reply=%q", handled, reply)
	}
}
