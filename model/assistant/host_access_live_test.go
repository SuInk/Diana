package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// The real current model and real tool execute only against disposable fixtures.
// The wrapper prevents an unexpected model call from touching production data.
type hostAccessReplayTool struct {
	*hostAccessTool
	dir  string
	args []string
}

func (t *hostAccessReplayTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if configToolString(input, "operation") == "exec" {
		b, _ := json.Marshal(input["args"])
		var args []string
		_ = json.Unmarshal(b, &args)
		if configToolString(input, "command") != "/bin/sh" || configToolString(input, "cwd") != t.dir || !reflect.DeepEqual(args, t.args) {
			return "", os.ErrPermission
		}
	} else if configToolString(input, "path") != t.dir && configToolString(input, "path") != filepath.Join(t.dir, "fixture.txt") {
		return "", os.ErrPermission
	}
	return t.hostAccessTool.Run(ctx, input)
}
func TestLiveHostAccess(t *testing.T) {
	if os.Getenv("DIANA_LIVE_HOST_ACCESS") != "1" {
		t.Skip("explicit isolated current-model replay only")
	}
	client := liveLLMClient(t)
	t.Logf("replay model: %s", os.Getenv("DIANA_TEST_LLM_MODEL"))
	for _, owner := range []bool{false, true} {
		bot := BotConfig{OwnerID: "owner", AgentEnabled: true}
		user := "member"
		if owner {
			user = "owner"
		}
		// 宿主命令执行前的审核也走同一个当前模型。
		runtime := NewRuntime(bot, nilChannel{}, NewDefaultPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return client, nil })
		event := MessageEvent{UserID: user, Kind: EventKindPrivate}
		policy := RelationshipPolicyFor(UserMemoryProfile{}, "owner", user)
		registry, err := runtime.newReplyAgentRegistry(context.Background(), runtime.ProfileConfig(""), event, policy, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := registry.Get(hostAccessToolName); ok != owner {
			t.Fatal("host tool availability does not match owner identity")
		}
		// Keep this model replay read-only; even unrelated tools cannot cause side effects.
		knowledge, ok := registry.Get("capabilities")
		if !ok {
			t.Fatal("capabilities unavailable")
		}
		runner, err := agent.NewRunner(client, agent.Config{WorkDir: t.TempDir(), MaxSteps: 3}, agent.NewToolRegistry(knowledge))
		if err != nil {
			t.Fatal(err)
		}
		response, err := runner.Run(context.Background(), agent.Request{Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: relationshipPermissionContext(policy, bot)},
			{Role: llm.RoleUser, Content: "你有查看宿主机和执行命令的功能吗？我现在能让你执行吗？请说明实际限制，不要执行任何操作。"},
		}})
		runner.Close()
		registry.Close()
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("capability owner=%v reply: %s", owner, response.Text)
		if !strings.Contains(response.Text, "主人") && !owner {
			t.Fatal("member permission not explained")
		}
		if owner && (strings.Contains(response.Text, "默认关闭") || strings.Contains(response.Text, "需要逐条审批")) {
			t.Fatal("model repeated obsolete owner restrictions")
		}

		for _, step := range response.Steps {
			if step.Tool == hostAccessToolName {
				t.Fatal("model attempted unauthorized host operation")
			}
		}
		time.Sleep(3 * time.Second)
	}
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "fixture.txt"), []byte("host-read-probe-20261025"), 0600); err != nil {
		t.Fatal(err)
	}
	r := hostAccessTestRuntime(t)
	args := []string{"-c", `printf approved > "$1/approved.txt"`, "probe", dir}
	tool := &hostAccessReplayTool{hostAccessTool: &hostAccessTool{runtime: r, event: identityCheckEvent("100001", "owner", "")}, dir: dir, args: args}
	run := func(messages []llm.Message) *agent.Response {
		t.Helper()
		runner, err := agent.NewRunner(client, agent.Config{WorkDir: t.TempDir(), MaxSteps: 4, CoreTools: replyAgentCoreTools}, agent.NewToolRegistry(tool))
		if err != nil {
			t.Fatal(err)
		}
		defer runner.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		response, err := runner.Run(ctx, agent.Request{Messages: messages})
		if err != nil {
			t.Fatalf("model replay failed: %v", err)
		}
		t.Logf("reply: %s", response.Text)
		for _, step := range response.Steps {
			t.Logf("tool=%s operation=%v error=%s", step.Tool, step.Input["operation"], step.Error)
		}
		return response
	}
	for _, test := range []struct{ prompt, op, expected string }{
		{"读取这个文件并告诉我原文：" + filepath.Join(dir, "fixture.txt"), "read", "host-read-probe-20261025"},
		{"查询这个路径所在磁盘的总容量和剩余空间：" + dir, "disk", ""},
	} {
		response := run([]llm.Message{{Role: llm.RoleUser, Content: test.prompt}})
		found := false
		for _, step := range response.Steps {
			if step.Tool == hostAccessToolName && step.Input["operation"] == test.op && step.Error == "" {
				found = true
			}
		}
		if !found || !strings.Contains(response.Text, test.expected) {
			t.Fatalf("missing successful %s", test.op)
		}
		time.Sleep(3 * time.Second)
	}
	input := map[string]any{"operation": "exec", "command": "/bin/sh", "args": args, "cwd": dir, "purpose": "主人要求的主机维护：在临时目录写入固定标记文件"}
	encoded, _ := json.Marshal(input)
	question := "执行这项主机维护操作：" + string(encoded)
	response := run([]llm.Message{{Role: llm.RoleUser, Content: question}})
	for _, step := range response.Steps {
		if strings.Contains(step.Output, "approval_required") {
			t.Fatal("routine owner command asked for approval")
		}
	}
	body, err := os.ReadFile(filepath.Join(dir, "approved.txt"))
	if err != nil || string(body) != "approved" {
		t.Fatalf("direct owner fixture write failed: %v", err)
	}
}
