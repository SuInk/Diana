// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeACPAgentArg 让测试二进制扮演一个 ACP 代理（见 main_test.go 的 TestMain）。用
// 测试二进制自己而不是写脚本：ACP 是双向的 JSON-RPC，代理要一边流式发更新、一边等
// 客户端回授权请求，shell 脚本写不出来，也不想为测试引入 Node。
const fakeACPAgentArg = "__fake-acp-agent"

// fakeACPScenarioEnv 选假代理这一轮怎么演。
const fakeACPScenarioEnv = "DIANA_FAKE_ACP_SCENARIO"

// fakeACPPIDFileEnv 让假代理把自己（以及它派生的子进程）的 PID 写出来，用例好确认
// 收尾之后它们都没了。
const fakeACPPIDFileEnv = "DIANA_FAKE_ACP_PIDFILE"

func runFakeACPAgent(in io.Reader, out, errOut io.Writer) int {
	scenario := os.Getenv(fakeACPScenarioEnv)
	recordPID := func(pid int) {
		if path := os.Getenv(fakeACPPIDFileEnv); path != "" {
			file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err == nil {
				fmt.Fprintln(file, pid)
				file.Close()
			}
		}
	}
	recordPID(os.Getpid())
	var writeMu sync.Mutex
	write := func(message any) {
		body, _ := json.Marshal(message)
		writeMu.Lock()
		defer writeMu.Unlock()
		_, _ = out.Write(append(body, '\n'))
	}
	var repliesMu sync.Mutex
	replies := map[string]chan codingACPMessage{}
	var nextID atomic.Int64
	call := func(method string, params any) codingACPMessage {
		id := fmt.Sprintf("agent-%d", nextID.Add(1))
		reply := make(chan codingACPMessage, 1)
		repliesMu.Lock()
		replies[id] = reply
		repliesMu.Unlock()
		write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		return <-reply
	}
	cancelled := make(chan struct{})
	var cancelOnce sync.Once
	var authed atomic.Bool
	update := func(value map[string]any) {
		write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "fake-sess", "update": value}})
	}
	say := func(text string) {
		update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}})
	}
	tool := func(id, title, command string) {
		update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": id, "title": title, "kind": "execute", "status": "pending", "rawInput": map[string]any{"command": command}})
	}
	toolStatus := func(id, status string) {
		update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "status": status})
	}
	prompt := func() (string, bool) {
		switch scenario {
		case "permission", "harmless-permission":
			command := "git push origin main"
			if scenario == "harmless-permission" {
				command = "go test ./..."
			}
			tool("t1", command, command)
			reply := call("session/request_permission", map[string]any{
				"sessionId": "fake-sess",
				"toolCall":  map[string]any{"toolCallId": "t1"},
				"options": []any{
					map[string]any{"optionId": "yes", "name": "允许", "kind": "allow_once"},
					map[string]any{"optionId": "always", "name": "总是允许", "kind": "allow_always"},
					map[string]any{"optionId": "no", "name": "拒绝", "kind": "reject_once"},
				},
			})
			var result struct {
				Outcome struct {
					Outcome  string `json:"outcome"`
					OptionID string `json:"optionId"`
				} `json:"outcome"`
			}
			_ = json.Unmarshal(reply.Result, &result)
			toolStatus("t1", "completed")
			say("授权结果：" + firstNonEmpty(result.Outcome.OptionID, result.Outcome.Outcome))
			return "end_turn", true
		case "crash":
			say("开始干活")
			_, _ = errOut.Write([]byte("fatal: boom\n"))
			os.Exit(3)
		case "hang":
			say("干到一半")
			<-cancelled
			return "cancelled", true
		case "refusal":
			return "refusal", true
		case "fs":
			reply := call("fs/read_text_file", map[string]any{"sessionId": "fake-sess", "path": "/etc/hosts"})
			code := 0
			if reply.Error != nil {
				code = reply.Error.Code
			}
			say(fmt.Sprintf("读文件被拒：%d", code))
			return "end_turn", true
		case "orphan-pipe":
			// 像一个没跟着退出的 MCP 服务：子进程继承了代理的 stdout，一直攥着。
			child := exec.Command("sleep", "30")
			child.Stdout, child.Stderr = os.Stdout, os.Stderr
			if child.Start() == nil {
				recordPID(child.Process.Pid)
			}
			say("子进程还挂着")
			return "end_turn", true
		case "no-final-message":
			say("先说一句")
			tool("t1", "go build ./...", "go build ./...")
			toolStatus("t1", "completed")
			return "end_turn", true
		}
		say("我先跑一下")
		say("测试。")
		tool("t1", "go test ./...", "go test ./...")
		toolStatus("t1", "in_progress")
		update(map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "内心独白"}})
		toolStatus("t1", "completed")
		update(map[string]any{"sessionUpdate": "plan", "entries": []any{
			map[string]any{"content": "跑测试", "status": "completed"},
			map[string]any{"content": "提交", "status": "pending"},
		}})
		say("改好了，")
		say("测试全过。")
		return "end_turn", true
	}
	handle := func(message codingACPMessage) {
		respond := func(result any) {
			write(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": result})
		}
		fail := func(code int, text string) {
			write(map[string]any{"jsonrpc": "2.0", "id": message.ID, "error": map[string]any{"code": code, "message": text}})
		}
		switch message.Method {
		case "initialize":
			if scenario == "login-prompt" {
				// 像 Gemini CLI 等交互式登录：提示打在 stdout 上，不带换行，然后退出。
				writeMu.Lock()
				_, _ = out.Write([]byte("Opening authentication page in your browser. Do you want to continue? [Y/n]: "))
				writeMu.Unlock()
				os.Exit(1)
			}
			methods := []any{}
			if scenario == "auth" {
				methods = []any{map[string]any{"id": "oauth-personal", "name": "Google 登录"}, map[string]any{"id": "fake-api-key", "name": "API 密钥"}}
			}
			respond(map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true}, "agentInfo": map[string]any{"name": "fake-agent"}, "authMethods": methods})
		case "authenticate":
			// 会话进程不该来调这个：真实代理在这里会改写自己的配置。
			_, _ = errOut.Write([]byte("authenticate 被调用\n"))
			authed.Store(true)
			respond(map[string]any{})
		case "session/new":
			if scenario == "auth" && !authed.Load() {
				fail(codingACPErrAuthRequired, "Authentication required")
				return
			}
			respond(map[string]any{"sessionId": "fake-sess"})
		case "session/prompt":
			if stop, ok := prompt(); ok {
				respond(map[string]any{"stopReason": stop})
			}
		default:
			fail(codingACPErrMethodNotFound, "unknown method")
		}
	}
	reader := bufio.NewReader(in)
	for {
		line, err := reader.ReadBytes('\n')
		var message codingACPMessage
		if len(bytes.TrimSpace(line)) > 0 && json.Unmarshal(line, &message) == nil {
			switch {
			case message.Method == "session/cancel":
				cancelOnce.Do(func() { close(cancelled) })
			case message.Method != "":
				go handle(message)
			default:
				repliesMu.Lock()
				reply := replies[strings.Trim(string(message.ID), `"`)]
				repliesMu.Unlock()
				if reply != nil {
					reply <- message
				}
			}
		}
		if err != nil {
			return 0
		}
	}
}

type fakeACPRun struct {
	code     int
	log      string
	snapshot codingJobSnapshot
}

// runFakeACPSession 让会话进程的主体对着假代理跑一轮，返回退出码和解析后的日志。
func runFakeACPSession(t *testing.T, ctx context.Context, scenario, policyPath string) fakeACPRun {
	t.Helper()
	t.Setenv(fakeACPScenarioEnv, scenario)
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	var buffer bytes.Buffer
	log := &codingACPLog{w: &buffer}
	code := runCodingACPSession(ctx, codingACPSpec{
		JobID: "code-acp", Command: executable, Args: []string{fakeACPAgentArg},
		Dir: t.TempDir(), Instruction: "修一下换行 bug", PolicyPath: policyPath,
	}, log)
	path := filepath.Join(t.TempDir(), "job.log")
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	return fakeACPRun{code: code, log: buffer.String(), snapshot: parseCodingLog(path)}
}

// TestCodingACPSessionReportsConclusionAfterLastTool 钉住一轮正常的活：流式分片攒成
// 整句，结论取最后一次工具调用之后说的话，思考过程不落日志，进度里没有原始 JSON。
func TestCodingACPSessionReportsConclusionAfterLastTool(t *testing.T) {
	run := runFakeACPSession(t, context.Background(), "success", "")
	if run.code != 0 {
		t.Fatalf("exit = %d\n%s", run.code, run.log)
	}
	snapshot := run.snapshot
	if !snapshot.Done || snapshot.IsError || snapshot.Result != "改好了，测试全过。" {
		t.Fatalf("done=%v error=%v result=%q", snapshot.Done, snapshot.IsError, snapshot.Result)
	}
	if snapshot.SessionID != "fake-sess" {
		t.Fatalf("session = %q", snapshot.SessionID)
	}
	tail := strings.Join(snapshot.Tail, "\n")
	for _, want := range []string{"会话已建立", "我先跑一下测试。", "执行命令：go test ./...", "计划共 2 项，已完成 1 项"} {
		if !strings.Contains(tail, want) {
			t.Fatalf("进度里少了 %q：%q", want, tail)
		}
	}
	if strings.Count(tail, "执行命令：go test ./...") != 1 {
		t.Fatalf("同一个工具调用状态变了几次就报了几次：%q", tail)
	}
	if strings.Contains(run.log, "内心独白") || strings.Contains(tail, "{") {
		t.Fatalf("思考过程或原始 JSON 进了日志：%q", run.log)
	}
}

// TestCodingACPSessionFallsBackToLastMessage 钉住收工前没再说话的情况：结论取最后说
// 的那句，而不是空的。
func TestCodingACPSessionFallsBackToLastMessage(t *testing.T) {
	run := runFakeACPSession(t, context.Background(), "no-final-message", "")
	if run.snapshot.Result != "先说一句" {
		t.Fatalf("result = %q", run.snapshot.Result)
	}
}

func writeACPApprovalPolicy(t *testing.T, mode string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	policy := codingApprovalPolicy{JobID: "code-acp", Mode: mode, ApprovalDir: filepath.Join(dir, "approvals"), TimeoutSeconds: 10}
	if err := os.MkdirAll(policy.ApprovalDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body, _ := json.Marshal(policy)
	path := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	return path, policy.ApprovalDir
}

// answerACPApproval 扮演 Diana 那一头：等信箱里出现询问，按 allow 写回裁决。
func answerACPApproval(t *testing.T, dir string, allow bool) <-chan codingApprovalRequest {
	t.Helper()
	asked := make(chan codingApprovalRequest, 1)
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			matches, _ := filepath.Glob(filepath.Join(dir, "*.req"))
			if len(matches) > 0 {
				var request codingApprovalRequest
				body, _ := os.ReadFile(matches[0])
				_ = json.Unmarshal(body, &request)
				response, _ := json.Marshal(codingApprovalResponse{Allow: allow, Reason: "主人回了码"})
				_ = os.WriteFile(codingApprovalResponsePath(dir, request.ID), response, 0o600)
				asked <- request
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		close(asked)
	}()
	return asked
}

// TestCodingACPSessionAsksOwnerForDangerousCommand 钉住审批映射：危险命令走同一个
// 信箱问主人；放行只选「这一次」，不把「以后都同意」交给代理去记。
func TestCodingACPSessionAsksOwnerForDangerousCommand(t *testing.T) {
	for _, tc := range []struct {
		allow bool
		want  string
	}{{true, "授权结果：yes"}, {false, "授权结果：no"}} {
		policyPath, dir := writeACPApprovalPolicy(t, codingApprovalModeDangerous)
		asked := answerACPApproval(t, dir, tc.allow)
		run := runFakeACPSession(t, context.Background(), "permission", policyPath)
		if run.snapshot.Result != tc.want {
			t.Fatalf("allow=%v result = %q\n%s", tc.allow, run.snapshot.Result, run.log)
		}
		request, ok := <-asked
		if !ok || request.Tool != "git push origin main" || request.Detail != "git push origin main" || request.Pattern != "git push" || request.JobID != "code-acp" {
			t.Fatalf("信箱里的询问不对：%#v", request)
		}
		tail := strings.Join(run.snapshot.Tail, "\n")
		if !strings.Contains(tail, "等主人确认：git push origin main") {
			t.Fatalf("进度里看不出在等确认：%q", tail)
		}
	}
}

// TestCodingACPSessionAllowsHarmlessCommandWithoutAsking 钉住「危险操作」档不打扰主人：
// 不在危险清单里的命令直接放行，信箱里不该出现询问。
func TestCodingACPSessionAllowsHarmlessCommandWithoutAsking(t *testing.T) {
	policyPath, dir := writeACPApprovalPolicy(t, codingApprovalModeDangerous)
	run := runFakeACPSession(t, context.Background(), "harmless-permission", policyPath)
	if run.snapshot.Result != "授权结果：yes" {
		t.Fatalf("result = %q", run.snapshot.Result)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "*")); len(matches) != 0 {
		t.Fatalf("不该去问主人：%v", matches)
	}
	noPolicy := runFakeACPSession(t, context.Background(), "permission", "")
	if noPolicy.snapshot.Result != "授权结果：yes" {
		t.Fatalf("审批关着时应当直接放行：%q", noPolicy.snapshot.Result)
	}
}

// TestCodingACPSessionExplainsLoginWithoutAuthenticating 钉住认证：代理要求先登录时
// 直接失败并列出登录方式，不替主人调 authenticate。真实的 Gemini CLI 在 authenticate
// 时会改写 ~/.gemini/settings.json，换方式还会清掉已缓存的登录凭据。
func TestCodingACPSessionExplainsLoginWithoutAuthenticating(t *testing.T) {
	run := runFakeACPSession(t, context.Background(), "auth", "")
	if run.code != 1 || !run.snapshot.IsError {
		t.Fatalf("exit=%d result=%q", run.code, run.snapshot.Result)
	}
	for _, want := range []string{"需要先登录", "Google 登录", "API 密钥"} {
		if !strings.Contains(run.snapshot.Result, want) {
			t.Fatalf("错误说明里少了 %q：%q", want, run.snapshot.Result)
		}
	}
	if strings.Contains(run.snapshot.Result, "authenticate 被调用") {
		t.Fatalf("不该替主人调 authenticate：%q", run.snapshot.Result)
	}
}

// TestCodingACPSessionShowsAgentPrompt 钉住代理卡在交互式登录的情况：它打在 stdout
// 上的提示要出现在错误说明里，不然只看得到「没有完成握手」。
func TestCodingACPSessionShowsAgentPrompt(t *testing.T) {
	run := runFakeACPSession(t, context.Background(), "login-prompt", "")
	if run.code != 1 || !strings.Contains(run.snapshot.Result, "Opening authentication page") {
		t.Fatalf("exit=%d result=%q", run.code, run.snapshot.Result)
	}
}

// TestCodingACPSessionReportsAgentCrash 钉住代理半路挂掉：按失败收尾，错误里带上它
// stderr 最后说的话。
func TestCodingACPSessionReportsAgentCrash(t *testing.T) {
	run := runFakeACPSession(t, context.Background(), "crash", "")
	if run.code != 1 || !run.snapshot.Done || !run.snapshot.IsError || !strings.Contains(run.snapshot.Result, "fatal: boom") {
		t.Fatalf("exit=%d result=%q", run.code, run.snapshot.Result)
	}
}

// TestCodingACPSessionCancelsThroughProtocol 钉住取消：收到信号先按协议 session/cancel，
// 代理停下后写结果行再退出，赶在 Diana 的 SIGKILL 之前。
func TestCodingACPSessionCancelsThroughProtocol(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	started := time.Now()
	run := runFakeACPSession(t, ctx, "hang", "")
	if run.code != codingACPExitCancelled {
		t.Fatalf("exit = %d\n%s", run.code, run.log)
	}
	if elapsed := time.Since(started); elapsed > codingACPCancelGrace {
		t.Fatalf("代理停下来之后还等满了宽限期：%s", elapsed)
	}
	if !strings.Contains(run.log, `"stop_reason":"cancelled"`) || run.snapshot.Result != "干到一半" {
		t.Fatalf("取消后没交代做到哪了：%q", run.log)
	}
}

func fakeACPPIDs(t *testing.T, path string) []int {
	t.Helper()
	body, _ := os.ReadFile(path)
	var pids []int
	for _, line := range strings.Fields(string(body)) {
		if pid, err := strconv.Atoi(line); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// TestCodingACPSessionExitsWhenAgentChildHoldsPipe 钉住收尾不被卡住：代理派生的进程
// 攥着输出管道不放时，会话进程写完结果照样按时退出，并把那些进程一起收掉。以前这里
// 会一直等 EOF，Diana 那边要到单次最长运行时间才收尾，还按超时记成失败。
func TestCodingACPSessionExitsWhenAgentChildHoldsPipe(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	t.Setenv(fakeACPPIDFileEnv, pidFile)
	started := time.Now()
	run := runFakeACPSession(t, context.Background(), "orphan-pipe", "")
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("会话进程被攥着的管道卡了 %s", elapsed)
	}
	if run.code != 0 || run.snapshot.Result != "子进程还挂着" {
		t.Fatalf("exit=%d result=%q\n%s", run.code, run.snapshot.Result, run.log)
	}
	pids := fakeACPPIDs(t, pidFile)
	if len(pids) != 2 {
		t.Fatalf("pids = %v", pids)
	}
	waitForCondition(t, 5*time.Second, func() bool { return !codingProcessAlive(pids[0]) && !codingProcessAlive(pids[1]) })
}

// TestCodingACPSessionCancelsPendingPermission 钉住取消时悬着的授权：协议要求回
// cancelled，不能回成拒绝。
func TestCodingACPSessionCancelsPendingPermission(t *testing.T) {
	policyPath, _ := writeACPApprovalPolicy(t, codingApprovalModeDangerous)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	run := runFakeACPSession(t, ctx, "permission", policyPath)
	if run.code != codingACPExitCancelled || run.snapshot.Result != "授权结果：cancelled" {
		t.Fatalf("exit=%d result=%q\n%s", run.code, run.snapshot.Result, run.log)
	}
}

// TestCodingACPSessionRejectsClientMethods 钉住握手时没声明的能力：代理来要读文件，
// 按「没有这个方法」回，让它用自己的工具。
func TestCodingACPSessionRejectsClientMethods(t *testing.T) {
	run := runFakeACPSession(t, context.Background(), "fs", "")
	if run.snapshot.Result != fmt.Sprintf("读文件被拒：%d", codingACPErrMethodNotFound) {
		t.Fatalf("result = %q", run.snapshot.Result)
	}
}

// TestCodingACPSessionReportsRefusal 钉住没做完的停止原因：退出码照常，汇报按失败说。
func TestCodingACPSessionReportsRefusal(t *testing.T) {
	run := runFakeACPSession(t, context.Background(), "refusal", "")
	if run.code != 0 || !run.snapshot.IsError || !strings.Contains(run.snapshot.Result, "拒绝") {
		t.Fatalf("exit=%d error=%v result=%q", run.code, run.snapshot.IsError, run.snapshot.Result)
	}
}

// TestLaunchCodingJobRunsACPAgent 走一遍真实链路：Diana 把会话进程当编码 CLI 拉起来，
// 会话进程再拉起代理，跑完照常按日志收尾并汇报。
func TestLaunchCodingJobRunsACPAgent(t *testing.T) {
	useTempCodingWorkspace(t)
	t.Setenv(fakeACPScenarioEnv, "success")
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	workspace := t.TempDir()
	rt, settings, event := codingTestRuntime(t, executable, workspace)
	settings[codingAgentSettingBackend] = codingBackendACP
	settings[codingAgentSettingTemplate] = fakeACPAgentArg
	settings[codingAgentSettingApprovalMode] = codingApprovalModeOff
	cfg, err := codingAgentConfigFromSettings(settings)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	ws, _ := cfg.workspace("demo")

	job, err := rt.launchCodingJob(context.Background(), event, cfg, ws, "修一下换行 bug", "")
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	waitForCondition(t, 20*time.Second, func() bool { return codingJobReported(t, job.ID) })
	saved, _ := loadCodingJob(job.ID)
	if saved.Status != codingJobStatusSucceeded || saved.Result != "改好了，测试全过。" || saved.SessionID != "fake-sess" {
		t.Fatalf("saved = %#v", saved)
	}
	messages := rt.channel.(*concurrentRecordingChannel).messages()
	if len(messages) != 1 || !strings.Contains(messages[0].Text, "改好了，测试全过。") {
		t.Fatalf("汇报 = %#v", messages)
	}
	if _, err := os.Stat(codingACPSpecPath(job.ID)); err != nil {
		t.Fatalf("会话参数应当留在任务目录里：%v", err)
	}
}

// TestCodingACPConfigValidation 钉住 ACP 后端的配置规则：必须填代理命令，参数模板可以
// 为空、但不能写 {{instruction}}；聊天审批可用，密钥环境变量不适用。
func TestCodingACPConfigValidation(t *testing.T) {
	useTempCodingWorkspace(t)
	base := func(values map[string]any) SettingValues {
		settings := codingSettings(map[string]any{codingAgentSettingBackend: codingBackendACP, codingAgentSettingWorkspaces: "demo=" + t.TempDir()})
		for key, value := range values {
			settings[key] = value
		}
		return settings
	}
	if _, err := codingAgentConfigFromSettings(base(nil)); err == nil || !strings.Contains(err.Error(), "ACP 后端必须配置代理的可执行文件") {
		t.Fatalf("没填代理命令应当报错：%v", err)
	}
	if _, err := codingAgentConfigFromSettings(base(map[string]any{codingAgentSettingCommand: "gemini", codingAgentSettingTemplate: "--acp {{instruction}}"})); err == nil {
		t.Fatalf("参数模板里写了 {{instruction}} 应当报错")
	}
	cfg, err := codingAgentConfigFromSettings(base(map[string]any{codingAgentSettingCommand: "opencode", codingAgentSettingTemplate: "acp"}))
	if err != nil || cfg.Backend != codingBackendACP || cfg.ApprovalMode != codingApprovalModeDangerous {
		t.Fatalf("cfg=%#v err=%v", cfg, err)
	}
	if _, err := normalizeCodingAgentProfiles([]any{map[string]any{
		"id": "gemini-main", "backend": codingBackendACP, "command": "gemini", "command_template": "--acp", "model": "", "api_key_env": "", "approval_mode": codingApprovalModeDangerous, "default": false,
	}}); err != nil {
		t.Fatalf("ACP 代理应当能开聊天审批：%v", err)
	}
	if _, err := normalizeCodingAgentProfiles([]any{map[string]any{
		"id": "gemini-main", "backend": codingBackendACP, "command": "gemini", "command_template": "--acp", "model": "", "api_key_env": "DIANA_GEMINI_KEY", "approval_mode": codingApprovalModeOff, "default": false,
	}}); err == nil {
		t.Fatalf("ACP 代理的凭据走代理自己的环境变量，不该接受密钥环境变量名")
	}
}

// TestLaunchCodingJobACPApprovalGoesThroughChat 走一遍 ACP 审批的整条链路：代理要推
// 代码，会话进程投进信箱，Diana 在聊天里问主人，主人回放行码，代理拿到「这一次允许」。
func TestLaunchCodingJobACPApprovalGoesThroughChat(t *testing.T) {
	useTempCodingWorkspace(t)
	t.Setenv(fakeACPScenarioEnv, "permission")
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	rt, settings, event := codingTestRuntime(t, executable, t.TempDir())
	settings[codingAgentSettingBackend] = codingBackendACP
	settings[codingAgentSettingTemplate] = fakeACPAgentArg
	settings[codingAgentSettingApprovalMode] = codingApprovalModeDangerous
	cfg, err := codingAgentConfigFromSettings(settings)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	ws, _ := cfg.workspace("demo")
	job, err := rt.launchCodingJob(context.Background(), event, cfg, ws, "把改动推上去", "")
	if err != nil {
		t.Fatalf("launch: %v", err)
	}

	channel := rt.channel.(*concurrentRecordingChannel)
	waitForCondition(t, 20*time.Second, func() bool { return channel.count() >= 1 })
	matches, _ := filepath.Glob(filepath.Join(codingApprovalDir(), "*.req"))
	if len(matches) != 1 {
		t.Fatalf("信箱里应当正好有一条询问：%v", matches)
	}
	var request codingApprovalRequest
	body, _ := os.ReadFile(matches[0])
	_ = json.Unmarshal(body, &request)
	allowCode, _, _ := codingApprovalCodes(request)
	if asked := channel.messages()[0].Text; !strings.Contains(asked, "git push origin main") || !strings.Contains(asked, allowCode) {
		t.Fatalf("询问消息 = %q", asked)
	}
	if _, handled := rt.handleOwnerCommand(MessageEvent{Kind: EventKindPrivate, UserID: "1"}, "同意 "+allowCode); !handled {
		t.Fatalf("主人的放行码没被受理")
	}
	waitForCondition(t, 20*time.Second, func() bool { return codingJobReported(t, job.ID) })
	saved, _ := loadCodingJob(job.ID)
	if saved.Status != codingJobStatusSucceeded || saved.Result != "授权结果：yes" {
		t.Fatalf("saved = %#v", saved)
	}
}

// TestCancelACPCodingJobStopsSessionAndAgent 钉住从 Diana 取消 ACP 任务：取消对整个进
// 程组发 SIGTERM，会话进程和代理都得收住，看护照常收尾，记录保持「已取消」。
func TestCancelACPCodingJobStopsSessionAndAgent(t *testing.T) {
	useTempCodingWorkspace(t)
	t.Setenv(fakeACPScenarioEnv, "hang")
	pidFile := filepath.Join(t.TempDir(), "pids")
	t.Setenv(fakeACPPIDFileEnv, pidFile)
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	rt, settings, event := codingTestRuntime(t, executable, t.TempDir())
	settings[codingAgentSettingBackend] = codingBackendACP
	settings[codingAgentSettingTemplate] = fakeACPAgentArg
	settings[codingAgentSettingApprovalMode] = codingApprovalModeOff
	cfg, err := codingAgentConfigFromSettings(settings)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	ws, _ := cfg.workspace("demo")
	job, err := rt.launchCodingJob(context.Background(), event, cfg, ws, "慢慢干", "")
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	waitForCondition(t, 10*time.Second, func() bool {
		return strings.Contains(strings.Join(parseCodingLog(job.LogPath).Tail, "\n"), "会话已建立")
	})
	if _, err := rt.cancelCodingJob(context.Background(), job.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	waitCodingWatcherDone(t, rt, job.ID)
	if codingProcessAlive(job.PID) {
		t.Fatalf("会话进程 %d 取消后还活着", job.PID)
	}
	if saved, _ := loadCodingJob(job.ID); saved.Status != codingJobStatusCancelled {
		t.Fatalf("status = %q", saved.Status)
	}
	// 代理单独一个进程组，Diana 的 SIGTERM 打不到它：它是按 session/cancel 停下来的，
	// 所以做到哪还交代得出来。
	if snapshot := parseCodingLog(job.LogPath); snapshot.Result != "干到一半" {
		t.Fatalf("取消后没交代做到哪了：%q", snapshot.Result)
	}
	for _, pid := range fakeACPPIDs(t, pidFile) {
		waitForCondition(t, 5*time.Second, func() bool { return !codingProcessAlive(pid) })
	}
}
