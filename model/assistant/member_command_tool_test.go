// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

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

func memberCommandTestRuntime(t *testing.T, enabled bool) (*Runtime, MessageEvent) {
	t.Helper()
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "app.db"))
	cfg := BotConfig{ID: "bot-a", OwnerID: "owner", Platform: PlatformTelegram, AgentEnabled: true, AgentMode: AgentModeStandard, AgentMemberCommandEnabled: enabled}
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	return r, MessageEvent{ProfileID: "bot-a", Platform: PlatformTelegram, Kind: EventKindGroup, GroupID: "group-a", UserID: "alice"}
}

func stubMemberCommandAvailable(t *testing.T, available bool) {
	t.Helper()
	restore := memberCommandAvailable
	memberCommandAvailable = func() bool { return available }
	t.Cleanup(func() { memberCommandAvailable = restore })
}

// 只有开关打开、群聊、非安全模式、成员沙盒可用时，成员才拿得到 run_command。
func TestMemberCommandExposure(t *testing.T) {
	stubMemberCommandAvailable(t, true)
	r, event := memberCommandTestRuntime(t, true)
	if !r.allowedAgentToolNamesForEvent(event, RelationshipPolicy{})["run_command"] {
		t.Fatal("member run_command not allowed when enabled")
	}
	registry, err := r.newReplyAgentRegistry(context.Background(), r.effectiveConfigForEvent(event), event, RelationshipPolicy{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := registry.Get("run_command")
	registry.Close()
	if !ok {
		t.Fatal("member run_command missing from registry")
	}
	if _, member := tool.(*dianaMemberCommandTool); !member {
		t.Fatalf("member got %T, want the member sandbox tool", tool)
	}

	private := event
	private.Kind = EventKindPrivate
	if r.allowedAgentToolNamesForEvent(private, RelationshipPolicy{})["run_command"] {
		t.Fatal("private chat exposed member run_command")
	}
	off, _ := memberCommandTestRuntime(t, false)
	if off.allowedAgentToolNamesForEvent(event, RelationshipPolicy{})["run_command"] {
		t.Fatal("run_command exposed while switch is off")
	}
	cfg := r.effectiveConfigForEvent(event)
	cfg.AgentMode = AgentModeSafe
	safe := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if safe.allowedAgentToolNamesForEvent(event, RelationshipPolicy{})["run_command"] {
		t.Fatal("safe mode exposed member run_command")
	}
	stubMemberCommandAvailable(t, false)
	if r.allowedAgentToolNamesForEvent(event, RelationshipPolicy{})["run_command"] {
		t.Fatal("run_command exposed without a member sandbox")
	}
	if _, err := newDianaMemberCommandTool(r, event).Run(context.Background(), map[string]any{"command": "python3"}); err == nil {
		t.Fatal("member command ran without a sandbox")
	}
}

// 产物登记本群凭据：本群的 send_file path 读得到，别的群读不到；每人各一个目录。
func TestMemberCommandGrantsProducedFilesToGroup(t *testing.T) {
	stubMemberCommandAvailable(t, true)
	r, event := memberCommandTestRuntime(t, true)
	tool := newDianaMemberCommandTool(r, event)
	var gotDir string
	tool.run = func(_ context.Context, req agent.MemberCommandRequest) (agent.MemberCommandResult, error) {
		gotDir = req.Dir
		if err := os.MkdirAll(filepath.Join(req.Dir, "out"), 0o700); err != nil {
			return agent.MemberCommandResult{}, err
		}
		if err := os.WriteFile(filepath.Join(req.Dir, "out", "notes.txt"), []byte("bad apple"), 0o600); err != nil {
			return agent.MemberCommandResult{}, err
		}
		if req.Command != "python3" || len(req.Args) != 2 || req.Args[1] != "  indented\n" {
			t.Errorf("request = %#v", req)
		}
		return agent.MemberCommandResult{Command: req.Command, Sandbox: "fake", Files: []agent.MemberCommandFile{{Name: "out/notes.txt", Size: 9, Data: []byte("bad apple")}}}, nil
	}
	body, err := tool.Run(context.Background(), map[string]any{"command": "python3", "args": []any{"-c", "  indented\n"}})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Files []memberCommandFileOutput `json:"files"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("files = %s", body)
	}
	path := result.Files[0].Path
	if !strings.HasPrefix(path, memberCommandRunsDir+"/"+r.workspaceGroupScope(event)+"/") || strings.Contains(path, "alice") {
		t.Fatalf("path = %s", path)
	}
	if gotDir != filepath.Join(AgentWorkspaceDir(), filepath.FromSlash(strings.TrimSuffix(path, "/out/notes.txt"))) {
		t.Fatalf("dir = %s, path = %s", gotDir, path)
	}
	content, err := toolTextSource(r, event, false, map[string]any{"path": path}, 1<<20)
	if err != nil || content != "bad apple" {
		t.Fatalf("group member could not use produced file: %q %v", content, err)
	}
	other := event
	other.GroupID = "group-b"
	if _, err := toolTextSource(r, other, false, map[string]any{"path": path}, 1<<20); err == nil {
		t.Fatal("another group read the produced file")
	}
	bob := event
	bob.UserID = "bob"
	bobDir, _ := r.memberCommandDir(bob)
	aliceDir, _ := r.memberCommandDir(event)
	if bobDir == aliceDir {
		t.Fatal("members share one command dir")
	}
}

// 真沙盒端到端：成员用 python 生成二进制文件，拿返回的 path 就能用；本机没有成员沙盒或
// python3 时跳过。
func TestMemberCommandPythonEndToEnd(t *testing.T) {
	if kind, reason := agent.MemberCommandAvailable(); kind == "" {
		t.Skipf("member sandbox unavailable: %s", reason)
	}
	r, event := memberCommandTestRuntime(t, true)
	body, err := newDianaMemberCommandTool(r, event).Run(context.Background(), map[string]any{
		"command": "python3",
		"args":    []any{"-c", "open('song.mid','wb').write(b'MThd\\x00\\x00\\x00\\x06\\xff')"},
	})
	if err != nil {
		if strings.Contains(err.Error(), "没有 python3") {
			t.Skip("python3 not installed")
		}
		t.Fatal(err)
	}
	var result struct {
		Files []memberCommandFileOutput `json:"files"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil || len(result.Files) != 1 {
		t.Fatalf("result = %s (%v)", body, err)
	}
	// .mid 不是文本，send_file 走 toolFileSource 照样能按本群凭据读出来。
	data, err := toolFileSource(r, event, false, map[string]any{"path": result.Files[0].Path}, 1<<20)
	if err != nil || string(data) != "MThd\x00\x00\x00\x06\xff" {
		t.Fatalf("data = %q err = %v", data, err)
	}
}
