// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodingHookNeedsApprovalByMode(t *testing.T) {
	dangerous := codingApprovalPolicy{Mode: codingApprovalModeDangerous, Patterns: defaultCodingApprovalPatterns()}
	for _, detail := range []string{"git push origin main", "GIT PUSH --force", "rm -rf build", "sudo apt install x"} {
		if !codingHookNeedsApproval(dangerous, "Bash", detail) {
			t.Fatalf("危险操作 %q 应当要确认", detail)
		}
	}
	// 改文件、跑测试、本地提交是派活时就默许的事，每步都问等于这功能没法用。
	for _, detail := range []string{"go test ./...", "git commit -m fix", "gofmt -w ."} {
		if codingHookNeedsApproval(dangerous, "Bash", detail) {
			t.Fatalf("日常操作 %q 不该要确认", detail)
		}
	}
	if codingHookNeedsApproval(dangerous, "Edit", "a.go") {
		t.Fatalf("危险操作档不该拦改文件")
	}

	all := codingApprovalPolicy{Mode: codingApprovalModeAllWrites}
	if !codingHookNeedsApproval(all, "Edit", "a.go") {
		t.Fatalf("全部写操作档应当拦改文件")
	}
	off := codingApprovalPolicy{Mode: codingApprovalModeOff}
	if codingHookNeedsApproval(off, "Bash", "git push") {
		t.Fatalf("关闭档不该拦任何东西")
	}
}

func TestCodingApprovalCodesAreStableAndDistinct(t *testing.T) {
	request := codingApprovalRequest{ID: "r1", JobID: "code-1", Tool: "Bash", Detail: "git push"}
	allow, deny := codingApprovalCodes(request)
	againAllow, againDeny := codingApprovalCodes(request)
	if allow != againAllow || deny != againDeny {
		t.Fatalf("同一次询问派生出了不同的码")
	}
	if allow == deny || len(allow) != codingApprovalCodeLength {
		t.Fatalf("allow=%q deny=%q", allow, deny)
	}
	other, _ := codingApprovalCodes(codingApprovalRequest{ID: "r2", JobID: "code-1", Tool: "Bash", Detail: "git push"})
	if other == allow {
		t.Fatalf("不同询问不该共用放行码")
	}
}

func TestContainsStandaloneCodeIgnoresEmbeddedMatches(t *testing.T) {
	if !containsStandaloneCode("同意 a1b2c3", "a1b2c3") {
		t.Fatalf("独立出现的码应当命中")
	}
	if !containsStandaloneCode("a1b2c3", "a1b2c3") {
		t.Fatalf("整条消息就是码时应当命中")
	}
	// 被更长的哈希串包住时不能算命中，否则日志里贴一段 sha 就可能误放行。
	if containsStandaloneCode("deadbeefa1b2c3ffff", "a1b2c3") {
		t.Fatalf("嵌在更长串里的码不该命中")
	}
}

func TestPrepareCodingApprovalWritesHookSettings(t *testing.T) {
	useTempCodingWorkspace(t)
	if err := os.MkdirAll(codingJobRecordDir(), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg, err := codingAgentConfigFromSettings(codingSettings(map[string]any{
		codingAgentSettingWorkspaces: "demo=" + t.TempDir(),
	}))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if cfg.ApprovalMode != codingApprovalModeDangerous {
		t.Fatalf("默认应当是危险操作要确认，实际 %q", cfg.ApprovalMode)
	}
	path, err := prepareCodingApproval(cfg, "code-x")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var settings struct {
		Hooks struct {
			PreToolUse []struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Command string `json:"command"`
					Timeout int    `json:"timeout"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(body, &settings); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if len(settings.Hooks.PreToolUse) != 1 || len(settings.Hooks.PreToolUse[0].Hooks) != 1 {
		t.Fatalf("settings = %s", body)
	}
	entry := settings.Hooks.PreToolUse[0]
	if entry.Matcher != "Bash" {
		t.Fatalf("matcher = %q", entry.Matcher)
	}
	if !strings.Contains(entry.Hooks[0].Command, CodingApprovalHookCommand) {
		t.Fatalf("command = %q", entry.Hooks[0].Command)
	}
	// hook 自己的超时必须比等主人的时间长，否则 CLI 会在主人还没回复时把它掐掉。
	if entry.Hooks[0].Timeout <= int(cfg.ApprovalTimeout/time.Second) {
		t.Fatalf("hook timeout %d 不大于等待时间 %v", entry.Hooks[0].Timeout, cfg.ApprovalTimeout)
	}

	// 策略文件要能被 hook 进程读回来。
	policy, err := loadCodingApprovalPolicy(codingApprovalPolicyPath("code-x"))
	if err != nil {
		t.Fatalf("load policy: %v", err)
	}
	if policy.JobID != "code-x" || policy.Mode != codingApprovalModeDangerous || policy.ApprovalDir == "" {
		t.Fatalf("policy = %#v", policy)
	}
}

func TestPrepareCodingApprovalRejectsNonClaudeBackend(t *testing.T) {
	useTempCodingWorkspace(t)
	cfg, err := codingAgentConfigFromSettings(codingSettings(map[string]any{
		codingAgentSettingBackend:    codingBackendCodex,
		codingAgentSettingWorkspaces: "demo=" + t.TempDir(),
	}))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	// 安静地不审批等于骗人：审批开着但后端做不到，就要当场说清楚。
	if _, err := prepareCodingApproval(cfg, "code-y"); err == nil {
		t.Fatalf("非 Claude 后端开审批应当报错")
	}

	cfg.ApprovalMode = codingApprovalModeOff
	if path, err := prepareCodingApproval(cfg, "code-y"); err != nil || path != "" {
		t.Fatalf("关闭审批时应当无声通过，path=%q err=%v", path, err)
	}
}

func TestCodingApprovalHookAllowsAndDenies(t *testing.T) {
	useTempCodingWorkspace(t)
	dir := codingApprovalDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	policyPath := filepath.Join(t.TempDir(), "policy.json")
	policy := codingApprovalPolicy{
		JobID: "code-hook", Mode: codingApprovalModeDangerous,
		Patterns: defaultCodingApprovalPatterns(), ApprovalDir: dir, TimeoutSeconds: 10,
	}
	body, _ := json.Marshal(policy)
	if err := os.WriteFile(policyPath, body, 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}

	// 不危险的操作不该打扰任何人，直接放行。
	var stdout, stderr bytes.Buffer
	payload := `{"tool_name":"Bash","tool_input":{"command":"go test ./..."}}`
	if code := RunCodingApprovalHook(policyPath, strings.NewReader(payload), &stdout, &stderr); code != 0 {
		t.Fatalf("日常命令应当直接放行，exit=%d", code)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("日常命令不该往信箱里投请求：%d 个文件", len(entries))
	}

	for _, tc := range []struct {
		name     string
		allow    bool
		wantExit int
	}{{"allow", true, 0}, {"deny", false, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			answered := make(chan struct{})
			go func() {
				defer close(answered)
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					entries, _ := os.ReadDir(dir)
					for _, entry := range entries {
						if !strings.HasSuffix(entry.Name(), ".req") {
							continue
						}
						id := strings.TrimSuffix(entry.Name(), ".req")
						raw, err := os.ReadFile(codingApprovalRequestPath(dir, id))
						if err != nil {
							continue
						}
						var request codingApprovalRequest
						if json.Unmarshal(raw, &request) != nil || request.JobID != "code-hook" {
							continue
						}
						writeCodingApprovalResponse(request, codingApprovalResponse{Allow: tc.allow, Reason: "测试裁决"})
						return
					}
					time.Sleep(50 * time.Millisecond)
				}
			}()
			var out, errBuf bytes.Buffer
			risky := `{"tool_name":"Bash","tool_input":{"command":"git push origin main"}}`
			exit := RunCodingApprovalHook(policyPath, strings.NewReader(risky), &out, &errBuf)
			<-answered
			if exit != tc.wantExit {
				t.Fatalf("exit = %d want %d (stderr=%q)", exit, tc.wantExit, errBuf.String())
			}
			want := "allow"
			if !tc.allow {
				want = "deny"
			}
			if !strings.Contains(out.String(), `"permissionDecision":"`+want+`"`) {
				t.Fatalf("stdout = %q", out.String())
			}
			// 裁决拿到之后信箱要清空，不然下一次询问会看到上一轮的残留。
			entries, _ := os.ReadDir(dir)
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".req") || strings.HasSuffix(entry.Name(), ".res") {
					t.Fatalf("信箱没清干净：%s", entry.Name())
				}
			}
		})
	}
}

func TestCodingApprovalHookDeniesOnTimeout(t *testing.T) {
	useTempCodingWorkspace(t)
	dir := codingApprovalDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	policyPath := filepath.Join(t.TempDir(), "policy.json")
	body, _ := json.Marshal(codingApprovalPolicy{
		JobID: "code-timeout", Mode: codingApprovalModeAllWrites,
		ApprovalDir: dir, TimeoutSeconds: 1,
	})
	if err := os.WriteFile(policyPath, body, 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	var stdout, stderr bytes.Buffer
	// 没人看见就默认放行一个危险操作，比让任务失败糟得多。
	exit := RunCodingApprovalHook(policyPath, strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"git push"}}`), &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("超时应当拦下来，exit=%d", exit)
	}
	if !strings.Contains(stderr.String(), "超时") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestCodingApprovalHookAllowsWhenPolicyUnreadable(t *testing.T) {
	var stdout, stderr bytes.Buffer
	// 审批链路自身坏了不该把任务卡死，Diana 侧的日志里会留痕。
	if code := RunCodingApprovalHook("/nonexistent/policy.json", strings.NewReader("{}"), &stdout, &stderr); code != 0 {
		t.Fatalf("读不到策略时应当放行，exit=%d", code)
	}
}

// TestCodingApprovalAsksOwnerAndHonorsReply 跑的是 Diana 这一侧的完整回路：
// 信箱里出现请求 → 发消息问主人 → 主人回码 → 裁决写回信箱。
func TestCodingApprovalAsksOwnerAndHonorsReply(t *testing.T) {
	useTempCodingWorkspace(t)
	channel := &concurrentRecordingChannel{}
	rt := NewRuntime(BotConfig{BotAccount: "42", OwnerID: "1"}, channel, NewPluginManager(NewCodingAgentPlugin()), nil, nil, nil, nil)
	dir := codingApprovalDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	job := CodingJob{
		ID: "code-ask", Workspace: "demo", Status: codingJobStatusRunning,
		StartedAt: time.Now(), ApprovalMode: codingApprovalModeDangerous,
		Target: codingJobTarget{UserID: "1"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rt.watchCodingApprovals(ctx, job, 30*time.Second)

	request := codingApprovalRequest{
		ID: "req1", JobID: job.ID, Tool: "Bash",
		Detail: "git push origin main", CreatedAt: time.Now(),
	}
	body, _ := json.Marshal(request)
	if err := os.WriteFile(codingApprovalRequestPath(dir, request.ID), body, 0o600); err != nil {
		t.Fatalf("write request: %v", err)
	}

	waitForCondition(t, 10*time.Second, func() bool { return channel.count() >= 1 })
	asked := channel.messages()[0].Text
	allowCode, denyCode := codingApprovalCodes(request)
	if !strings.Contains(asked, "git push origin main") || !strings.Contains(asked, allowCode) || !strings.Contains(asked, denyCode) {
		t.Fatalf("询问消息 = %q", asked)
	}

	// 群里别人拿到码也不算数：确认只认主人自己那条消息。
	if _, handled := rt.handleOwnerCommand(MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "999"}, allowCode); handled {
		t.Fatalf("非主人回码不该被受理")
	}

	reply, handled := rt.handleOwnerCommand(MessageEvent{Kind: EventKindPrivate, UserID: "1"}, "同意 "+allowCode)
	if !handled || !strings.Contains(reply, job.ID) {
		t.Fatalf("handled=%v reply=%q", handled, reply)
	}
	waitForCondition(t, 10*time.Second, func() bool {
		_, err := os.Stat(codingApprovalResponsePath(dir, request.ID))
		return err == nil
	})
	raw, err := os.ReadFile(codingApprovalResponsePath(dir, request.ID))
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var response codingApprovalResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !response.Allow {
		t.Fatalf("response = %#v", response)
	}
	// 码用过就作废，重放同一条消息不该再放行一次。
	if _, handled := rt.handleOwnerCommand(MessageEvent{Kind: EventKindPrivate, UserID: "1"}, allowCode); handled {
		t.Fatalf("用过的码不该再被受理")
	}
}

func TestCodingApprovalDenyReplyWritesRefusal(t *testing.T) {
	useTempCodingWorkspace(t)
	channel := &concurrentRecordingChannel{}
	rt := NewRuntime(BotConfig{BotAccount: "42", OwnerID: "1"}, channel, NewPluginManager(NewCodingAgentPlugin()), nil, nil, nil, nil)
	dir := codingApprovalDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	job := CodingJob{
		ID: "code-deny", Workspace: "demo", Status: codingJobStatusRunning,
		StartedAt: time.Now(), ApprovalMode: codingApprovalModeDangerous,
		Target: codingJobTarget{UserID: "1"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rt.watchCodingApprovals(ctx, job, 30*time.Second)

	request := codingApprovalRequest{ID: "req2", JobID: job.ID, Tool: "Bash", Detail: "rm -rf /", CreatedAt: time.Now()}
	body, _ := json.Marshal(request)
	if err := os.WriteFile(codingApprovalRequestPath(dir, request.ID), body, 0o600); err != nil {
		t.Fatalf("write request: %v", err)
	}
	waitForCondition(t, 10*time.Second, func() bool { return channel.count() >= 1 })

	_, denyCode := codingApprovalCodes(request)
	if _, handled := rt.handleOwnerCommand(MessageEvent{Kind: EventKindPrivate, UserID: "1"}, denyCode); !handled {
		t.Fatalf("拒绝码应当被受理")
	}
	waitForCondition(t, 10*time.Second, func() bool {
		raw, err := os.ReadFile(codingApprovalResponsePath(dir, request.ID))
		if err != nil {
			return false
		}
		var response codingApprovalResponse
		return json.Unmarshal(raw, &response) == nil && !response.Allow && response.Reason != ""
	})
}

func TestCodingToolStatusShowsPendingApproval(t *testing.T) {
	useTempCodingWorkspace(t)
	workspace := t.TempDir()
	rt, settings, event := codingTestRuntime(t, "/bin/true", workspace)
	if err := os.MkdirAll(codingJobRecordDir(), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	job := CodingJob{
		ID: "code-pending", Workspace: "demo", Status: codingJobStatusRunning,
		StartedAt: time.Now(), LogPath: codingJobLogPath("code-pending"),
		ApprovalMode: codingApprovalModeDangerous, Target: codingJobTarget{UserID: "1"},
	}
	if err := saveCodingJob(job); err != nil {
		t.Fatalf("save: %v", err)
	}
	request := codingApprovalRequest{ID: "req3", JobID: job.ID, Tool: "Bash", Detail: "git push origin main"}
	allow, deny := codingApprovalCodes(request)
	rt.codingJobs().registerApproval(&codingApprovalWait{
		request: request, allowCode: allow, denyCode: deny,
		decided: make(chan codingApprovalResponse, 1),
	})

	tool := newDianaCodingTool(rt, event, settings)
	output, err := tool.Run(context.Background(), map[string]any{"operation": "status", "job_id": job.ID})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var result dianaCodingResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Job == nil || !strings.Contains(result.Job.AwaitingApproval, "git push origin main") {
		t.Fatalf("job = %#v", result.Job)
	}
	if result.Job.ApprovalAllowCode != allow || result.Job.ApprovalDenyCode != deny {
		t.Fatalf("码没带出来：%#v", result.Job)
	}
	if !strings.Contains(result.Message, "点头") {
		t.Fatalf("message = %q", result.Message)
	}
}
