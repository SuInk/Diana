package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

const hostReviewDenyReply = `{"risk":"high","user_authorization":"unknown","outcome":"deny","reason":"测试拒绝"}`

type hostSessionPayload struct {
	Session agent.OwnerSessionResult `json:"session"`
}

func runHostSession(t *testing.T, tool *hostAccessTool, input map[string]any) (hostSessionPayload, error) {
	t.Helper()
	out, err := tool.Run(context.Background(), input)
	if err != nil {
		return hostSessionPayload{}, err
	}
	var payload hostSessionPayload
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("invalid output %s: %v", out, err)
	}
	return payload, nil
}

func hostSessionShell(t *testing.T) (string, string) {
	t.Helper()
	requireUnixShell(t)
	shell, err := filepath.EvalSymlinks("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return shell, cwd
}

// 启动和每一次输入都过审核：往 shell 会话里写一行就是执行一条命令，只审启动
// 等于开了个不受审核的口子。
func TestHostSessionReviewsStartAndEveryInput(t *testing.T) {
	shell, cwd := hostSessionShell(t)
	provider := &hostReviewStubProvider{reply: hostReviewAllowReply}
	r := hostAccessReviewRuntime(t, provider)
	t.Cleanup(r.closeHostSessions)
	tool := &hostAccessTool{runtime: r, event: identityCheckEvent("100001", "owner", "")}

	started, err := runHostSession(t, tool, map[string]any{"operation": "session_start", "command": shell, "cwd": cwd, "purpose": "开一个 shell 排查", "wait_ms": 0})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(provider.lastUserContent(), `"mode":"start"`) {
		t.Fatalf("start review payload = %s", provider.lastUserContent())
	}
	id := started.Session.SessionID
	sent, err := runHostSession(t, tool, map[string]any{"operation": "session_send", "session_id": id, "input": "echo session-ok", "purpose": "看看 shell 能否响应"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sent.Session.Output, "session-ok") {
		t.Fatalf("send output = %q", sent.Session.Output)
	}
	review := provider.lastUserContent()
	if !strings.Contains(review, `"mode":"send"`) || !strings.Contains(review, `"input":"echo session-ok"`) {
		t.Fatalf("send review payload = %s", review)
	}
	provider.mu.Lock()
	reviews := len(provider.requests)
	provider.mu.Unlock()

	// 读输出、列会话、结束会话不产生新动作，不审。
	if _, err := runHostSession(t, tool, map[string]any{"operation": "session_read", "session_id": id, "wait_ms": 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "session_list"}); err != nil {
		t.Fatal(err)
	}
	stopped, err := runHostSession(t, tool, map[string]any{"operation": "session_stop", "session_id": id})
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Session.Running || stopped.Session.EndReason != "stopped" {
		t.Fatalf("stop = %+v", stopped.Session)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.requests) != reviews {
		t.Fatalf("read/list/stop triggered %d extra reviews", len(provider.requests)-reviews)
	}
}

func TestHostSessionDeniedInputIsNotWritten(t *testing.T) {
	shell, cwd := hostSessionShell(t)
	provider := &hostReviewStubProvider{reply: hostReviewAllowReply}
	r := hostAccessReviewRuntime(t, provider)
	t.Cleanup(r.closeHostSessions)
	tool := &hostAccessTool{runtime: r, event: identityCheckEvent("100001", "owner", "")}
	started, err := runHostSession(t, tool, map[string]any{"operation": "session_start", "command": shell, "cwd": cwd, "purpose": "开一个 shell", "wait_ms": 0})
	if err != nil {
		t.Fatal(err)
	}

	provider.mu.Lock()
	provider.reply = hostReviewDenyReply
	provider.mu.Unlock()
	marker := filepath.Join(cwd, "should-not-exist")
	_, err = runHostSession(t, tool, map[string]any{"operation": "session_send", "session_id": started.Session.SessionID, "input": "touch " + marker, "purpose": "测试"})
	if err == nil || !strings.Contains(err.Error(), "未通过自动审核") {
		t.Fatalf("denied send err = %v", err)
	}
	// 拒绝后再让 shell 跑一条放行的命令：要是被拒那行写进去了，标记文件此时一定已经在了。
	provider.mu.Lock()
	provider.reply = hostReviewAllowReply
	provider.mu.Unlock()
	if _, err := runHostSession(t, tool, map[string]any{"operation": "session_send", "session_id": started.Session.SessionID, "input": "echo done", "purpose": "测试"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("denied input reached the shell: %v", err)
	}
}

func TestHostSessionRejectsNonOwnerAndUnknownSession(t *testing.T) {
	shell, cwd := hostSessionShell(t)
	r := hostAccessTestRuntime(t)
	t.Cleanup(r.closeHostSessions)
	member := &hostAccessTool{runtime: r, event: MessageEvent{UserID: "member"}}
	if _, err := member.Run(context.Background(), map[string]any{"operation": "session_start", "command": shell, "cwd": cwd, "purpose": "x"}); err == nil {
		t.Fatal("member started a host session")
	}
	owner := &hostAccessTool{runtime: r, event: identityCheckEvent("100001", "owner", "")}
	if _, err := owner.Run(context.Background(), map[string]any{"operation": "session_send", "session_id": "s-missing", "input": "ls", "purpose": "x"}); err == nil {
		t.Fatal("send to a missing session succeeded")
	}
}

// 机器人停止时会话全部结束，重新启动后能照常开新会话。
func TestRuntimeStopClosesHostSessions(t *testing.T) {
	shell, cwd := hostSessionShell(t)
	r := hostAccessTestRuntime(t)
	tool := &hostAccessTool{runtime: r, event: identityCheckEvent("100001", "owner", "")}
	if _, err := runHostSession(t, tool, map[string]any{"operation": "session_start", "command": shell, "args": []string{"-c", "sleep 30"}, "cwd": cwd, "purpose": "测试", "wait_ms": 0}); err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	cleared := r.hostSessions == nil
	r.mu.Unlock()
	if !cleared {
		t.Fatal("Stop() kept the session manager")
	}
	out, err := tool.Run(context.Background(), map[string]any{"operation": "session_list"})
	if err != nil || !strings.Contains(out, `"sessions":[]`) {
		t.Fatalf("session_list after stop = %s, %v", out, err)
	}
	r.closeHostSessions()
}
