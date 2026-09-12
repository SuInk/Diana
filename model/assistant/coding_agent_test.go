// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// useTempCodingWorkspace 把工作区根挪到临时目录：工作区跟着 APP_DB_PATH 走，
// 不隔离的话测试会往真实部署的工作区里写任务记录。
func useTempCodingWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("APP_DB_PATH", filepath.Join(dir, "diana.db"))
	return dir
}

func codingSettings(values map[string]any) SettingValues {
	settings := SettingValues{
		codingAgentSettingBackend: codingBackendClaude,
	}
	for key, value := range values {
		settings[key] = value
	}
	return settings
}

func TestParseCodingWorkspacesResolvesTargetsAndRejectsBadInput(t *testing.T) {
	useTempCodingWorkspace(t)

	workspaces, err := parseCodingWorkspaces("diana=/srv/diana\n# 注释\nupstream=https://example.com/a/b.git\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(workspaces) != 2 {
		t.Fatalf("workspaces = %#v", workspaces)
	}
	if workspaces[0].Dir != "/srv/diana" || workspaces[0].RepoURL != "" {
		t.Fatalf("local workspace = %#v", workspaces[0])
	}
	// 登记为仓库地址的工作区必须落在工作区根下面，不能跑到别处去。
	if want := filepath.Join(CodingWorkspaceRoot(), "upstream"); workspaces[1].Dir != want {
		t.Fatalf("clone dir = %q want %q", workspaces[1].Dir, want)
	}

	for _, raw := range []string{
		"没有等号",
		"../escape=/tmp/x",
		"a/b=/tmp/x",
		"dup=/tmp/x\ndup=/tmp/y",
		"empty=",
	} {
		if _, err := parseCodingWorkspaces(raw); err == nil {
			t.Fatalf("parseCodingWorkspaces(%q) 应当报错", raw)
		}
	}
}

func TestBuildCodingArgsDropsFlagsWithoutValues(t *testing.T) {
	template := codingBackendPresets()[codingBackendClaude].Template

	args := buildCodingArgs(template, map[string]string{
		"instruction": "修一下 reply 的换行 bug",
		"workspace":   "/srv/diana",
	})
	joined := strings.Join(args, " ")
	// 没配模型、没有续跑会话时，孤零零的 --model / --resume 会让 CLI 直接报参数错误。
	if strings.Contains(joined, "--model") || strings.Contains(joined, "--resume") {
		t.Fatalf("args = %#v", args)
	}
	if !strings.Contains(joined, "--add-dir /srv/diana") {
		t.Fatalf("args 缺少工作目录：%#v", args)
	}
	// 指令要保持成一个 argv 元素：带空格的指令被拆开就等于把参数打散了。
	found := false
	for _, arg := range args {
		if arg == "修一下 reply 的换行 bug" {
			found = true
		}
	}
	if !found {
		t.Fatalf("指令没有作为单个参数传入：%#v", args)
	}

	resumed := buildCodingArgs(template, map[string]string{
		"instruction": "继续",
		"workspace":   "/srv/diana",
		"model":       "claude-opus-5",
		"session":     "sess-1",
	})
	joined = strings.Join(resumed, " ")
	if !strings.Contains(joined, "--model claude-opus-5") || !strings.Contains(joined, "--resume sess-1") {
		t.Fatalf("resumed args = %#v", resumed)
	}
}

func TestBuildCodingArgsKeepsShellMetacharactersLiteral(t *testing.T) {
	args := buildCodingArgs("-p {{instruction}}", map[string]string{"instruction": "rm -rf / ; echo $(whoami)"})
	if len(args) != 2 || args[1] != "rm -rf / ; echo $(whoami)" {
		t.Fatalf("args = %#v", args)
	}
}

func writeCodingLog(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "job.log")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	return path
}

func TestParseCodingLogReadsClaudeStreamJSON(t *testing.T) {
	path := writeCodingLog(t,
		`{"type":"system","subtype":"init","session_id":"sess-9","cwd":"/srv/diana"}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"测试通过了"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"改完了并且测试通过","total_cost_usd":0.42,"num_turns":6,"session_id":"sess-9"}`,
	)
	snapshot := parseCodingLog(path)
	if snapshot.SessionID != "sess-9" {
		t.Fatalf("session = %q", snapshot.SessionID)
	}
	if !snapshot.Done || snapshot.IsError {
		t.Fatalf("done=%v isError=%v", snapshot.Done, snapshot.IsError)
	}
	if snapshot.Result != "改完了并且测试通过" {
		t.Fatalf("result = %q", snapshot.Result)
	}
	if snapshot.Turns != 6 || snapshot.CostUSD != 0.42 {
		t.Fatalf("turns=%d cost=%v", snapshot.Turns, snapshot.CostUSD)
	}
	// 工具调用要能被认出在动什么，工具结果回填不该刷进进度里。
	if !strings.Contains(strings.Join(snapshot.Tail, "\n"), "Bash：go test ./...") {
		t.Fatalf("tail = %#v", snapshot.Tail)
	}
	for _, line := range snapshot.Tail {
		if strings.Contains(line, "tool_result") {
			t.Fatalf("工具结果回填进了进度：%#v", snapshot.Tail)
		}
	}
}

func TestParseCodingLogFallsBackToTailForPlainOutput(t *testing.T) {
	path := writeCodingLog(t, "building...", "3 files changed", "done")
	snapshot := parseCodingLog(path)
	if snapshot.Done {
		t.Fatalf("纯文本日志不该被判定为已完成")
	}
	if !strings.HasSuffix(snapshot.Result, "done") {
		t.Fatalf("result = %q", snapshot.Result)
	}
}

func TestParseCodingLogCapsOverlongLines(t *testing.T) {
	path := writeCodingLog(t, strings.Repeat("x", codingJobMaxLineBytes*3))
	snapshot := parseCodingLog(path)
	if len(snapshot.Tail) != 1 || len(snapshot.Tail[0]) > codingJobMaxLineBytes {
		t.Fatalf("超长行没有被截断：%d", len(snapshot.Tail[0]))
	}
}

// fakeCodingCLI 写一个假的编码 CLI 出来。真的 claude 要网络和凭据，这里只关心
// 「进程被正确启动、输出被落进日志、结束后按日志汇报」这条链路。
func fakeCodingCLI(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("假 CLI 用 shell 脚本写的，Windows 上另说")
	}
	path := filepath.Join(t.TempDir(), "fake-cli")
	body := "#!/bin/sh\n" + script + "\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatalf("write fake cli: %v", err)
	}
	return path
}

func codingTestRuntime(t *testing.T, cli string, workspace string) (*Runtime, SettingValues, MessageEvent) {
	t.Helper()
	channel := &concurrentRecordingChannel{}
	rt := NewRuntime(BotConfig{BotAccount: "42", OwnerID: "1"}, channel, NewPluginManager(NewCodingAgentPlugin()), nil, nil, nil, nil)
	settings := codingSettings(map[string]any{
		codingAgentSettingCommand:    cli,
		codingAgentSettingWorkspaces: "demo=" + workspace,
	})
	event := MessageEvent{Kind: EventKindPrivate, UserID: "1", MessageID: "m1"}
	return rt, settings, event
}

func TestLaunchCodingJobReportsResultAfterProcessExits(t *testing.T) {
	useTempCodingWorkspace(t)
	workspace := t.TempDir()
	cli := fakeCodingCLI(t, `
echo '{"type":"system","subtype":"init","session_id":"sess-42"}'
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"a.go"}}]}}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"已修好并通过测试","total_cost_usd":0.1,"num_turns":3,"session_id":"sess-42"}'
`)
	rt, settings, event := codingTestRuntime(t, cli, workspace)
	cfg, err := codingAgentConfigFromSettings(settings)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	ws, ok := cfg.workspace("demo")
	if !ok {
		t.Fatalf("工作区没登记上")
	}
	job, err := rt.launchCodingJob(context.Background(), event, cfg, ws, "修 reply 的换行", "")
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if job.PID <= 0 || job.Status != codingJobStatusRunning {
		t.Fatalf("job = %#v", job)
	}

	waitForCondition(t, 10*time.Second, func() bool {
		saved, err := loadCodingJob(job.ID)
		return err == nil && saved.finished()
	})
	saved, err := loadCodingJob(job.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if saved.Status != codingJobStatusSucceeded {
		t.Fatalf("status = %q error = %q", saved.Status, saved.Error)
	}
	if saved.SessionID != "sess-42" {
		t.Fatalf("session = %q", saved.SessionID)
	}
	if saved.Result != "已修好并通过测试" || saved.Turns != 3 {
		t.Fatalf("result = %#v", saved)
	}

	channel := rt.channel.(*concurrentRecordingChannel)
	waitForCondition(t, 5*time.Second, func() bool { return channel.count() >= 1 })
	report := channel.messages()[0].Text
	if !strings.Contains(report, job.ID) || !strings.Contains(report, "已修好并通过测试") {
		t.Fatalf("report = %q", report)
	}
	// 汇报过就要落标记，否则重启接回会把同一个结果再发一遍。
	if !saved.Reported {
		waitForCondition(t, 5*time.Second, func() bool {
			again, err := loadCodingJob(job.ID)
			return err == nil && again.Reported
		})
	}
}

func TestLaunchCodingJobFailsWhenCLIExitsNonZero(t *testing.T) {
	useTempCodingWorkspace(t)
	workspace := t.TempDir()
	cli := fakeCodingCLI(t, "echo 'boom' >&2\nexit 3")
	rt, settings, event := codingTestRuntime(t, cli, workspace)
	cfg, _ := codingAgentConfigFromSettings(settings)
	ws, _ := cfg.workspace("demo")

	job, err := rt.launchCodingJob(context.Background(), event, cfg, ws, "随便干点什么", "")
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	waitForCondition(t, 10*time.Second, func() bool {
		saved, err := loadCodingJob(job.ID)
		return err == nil && saved.finished()
	})
	saved, _ := loadCodingJob(job.ID)
	if saved.Status != codingJobStatusFailed || !strings.Contains(saved.Error, "退出码 3") {
		t.Fatalf("saved = %#v", saved)
	}
}

func TestCodingWorkspaceRunsOneJobAtATime(t *testing.T) {
	useTempCodingWorkspace(t)
	workspace := t.TempDir()
	cli := fakeCodingCLI(t, "sleep 30")
	rt, settings, event := codingTestRuntime(t, cli, workspace)
	cfg, _ := codingAgentConfigFromSettings(settings)
	ws, _ := cfg.workspace("demo")

	job, err := rt.launchCodingJob(context.Background(), event, cfg, ws, "第一件活", "")
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	t.Cleanup(func() { killCodingProcess(job.PID) })

	// 同一份检出同时跑两个代理会互相覆盖，第二次派活必须被挡下来。
	if _, err := rt.launchCodingJob(context.Background(), event, cfg, ws, "第二件活", ""); err == nil {
		t.Fatalf("同一个工作区的第二个任务应当被拒绝")
	}

	cancelled, err := rt.cancelCodingJob(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.Status != codingJobStatusCancelled {
		t.Fatalf("cancelled = %#v", cancelled)
	}
	waitForCondition(t, 10*time.Second, func() bool { return !codingProcessAlive(job.PID) })

	// 取消后工作区要立刻放开，不然一次误派活就把工作区锁到进程退出为止。
	if _, err := rt.launchCodingJob(context.Background(), event, cfg, ws, "第三件活", ""); err != nil {
		t.Fatalf("取消之后应当能重新派活：%v", err)
	}
}

func TestCancelledCodingJobIsNotReportedTwice(t *testing.T) {
	useTempCodingWorkspace(t)
	workspace := t.TempDir()
	cli := fakeCodingCLI(t, "sleep 30")
	rt, settings, event := codingTestRuntime(t, cli, workspace)
	cfg, _ := codingAgentConfigFromSettings(settings)
	ws, _ := cfg.workspace("demo")

	job, _ := rt.launchCodingJob(context.Background(), event, cfg, ws, "会被取消的活", "")
	if _, err := rt.cancelCodingJob(context.Background(), job.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	waitForCondition(t, 10*time.Second, func() bool {
		saved, err := loadCodingJob(job.ID)
		return err == nil && !saved.FinishedAt.IsZero()
	})
	// 取消的回执是工具返回值，watch 那头收尾时不该再改成 failed，也不该推一条汇报。
	saved, _ := loadCodingJob(job.ID)
	if saved.Status != codingJobStatusCancelled {
		t.Fatalf("status = %q", saved.Status)
	}
	channel := rt.channel.(*concurrentRecordingChannel)
	time.Sleep(300 * time.Millisecond)
	if channel.count() != 0 {
		t.Fatalf("取消不该推汇报，却发了 %d 条", channel.count())
	}
}

func TestResumeCodingJobsFinalizesDeadJobAndReportsOnce(t *testing.T) {
	useTempCodingWorkspace(t)
	channel := &concurrentRecordingChannel{}
	rt := NewRuntime(BotConfig{BotAccount: "42", OwnerID: "1"}, channel, NewPluginManager(NewCodingAgentPlugin()), nil, nil, nil, nil)

	if err := os.MkdirAll(codingJobRecordDir(), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	job := CodingJob{
		ID:          "code-dead",
		Backend:     codingBackendClaude,
		Workspace:   "demo",
		Instruction: "上次没跑完的活",
		// 进程已经不在了：这个 PID 不可能存在。
		PID:       0,
		LogPath:   codingJobLogPath("code-dead"),
		Status:    codingJobStatusRunning,
		StartedAt: time.Now().Add(-time.Hour),
		Target:    codingJobTarget{UserID: "1"},
	}
	if err := os.WriteFile(job.LogPath, []byte(`{"type":"system","subtype":"init","session_id":"sess-old"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if err := saveCodingJob(job); err != nil {
		t.Fatalf("save: %v", err)
	}

	rt.ResumeCodingJobs(context.Background())
	saved, err := loadCodingJob(job.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if saved.Status != codingJobStatusInterrupted {
		t.Fatalf("status = %q", saved.Status)
	}
	if saved.SessionID != "sess-old" {
		t.Fatalf("接回时应当从日志里捞回会话 ID：%q", saved.SessionID)
	}
	waitForCondition(t, 5*time.Second, func() bool { return channel.count() >= 1 })
	if !strings.Contains(channel.messages()[0].Text, "被中断") {
		t.Fatalf("report = %q", channel.messages()[0].Text)
	}

	// 再接回一次不能把同一个结果又汇报一遍。
	before := channel.count()
	rt.ResumeCodingJobs(context.Background())
	time.Sleep(300 * time.Millisecond)
	if channel.count() != before {
		t.Fatalf("重复汇报了：%d -> %d", before, channel.count())
	}
}

func TestCodingToolRefusesNonOwner(t *testing.T) {
	useTempCodingWorkspace(t)
	workspace := t.TempDir()
	rt, settings, _ := codingTestRuntime(t, "/bin/true", workspace)
	tool := newDianaCodingTool(rt, MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "999"}, settings)

	_, err := tool.Run(context.Background(), map[string]any{"operation": "list"})
	if err == nil || !strings.Contains(err.Error(), "主人") {
		t.Fatalf("非主人调用应当被拒绝，err = %v", err)
	}
}

func TestCodingToolRejectsUnregisteredWorkspace(t *testing.T) {
	useTempCodingWorkspace(t)
	workspace := t.TempDir()
	rt, settings, event := codingTestRuntime(t, "/bin/true", workspace)
	tool := newDianaCodingTool(rt, event, settings)

	_, err := tool.Run(context.Background(), map[string]any{
		"operation":   "submit",
		"workspace":   "别的仓库",
		"instruction": "改点东西",
	})
	if err == nil || !strings.Contains(err.Error(), "登记过") {
		t.Fatalf("未登记工作区应当被拒绝，err = %v", err)
	}
}

func TestCodingToolStatusHidesPartialResultWhileRunning(t *testing.T) {
	useTempCodingWorkspace(t)
	workspace := t.TempDir()
	cli := fakeCodingCLI(t, "sleep 30")
	rt, settings, event := codingTestRuntime(t, cli, workspace)
	cfg, _ := codingAgentConfigFromSettings(settings)
	ws, _ := cfg.workspace("demo")
	job, err := rt.launchCodingJob(context.Background(), event, cfg, ws, "在跑的活", "")
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	t.Cleanup(func() { killCodingProcess(job.PID) })

	tool := newDianaCodingTool(rt, event, settings)
	output, err := tool.Run(context.Background(), map[string]any{"operation": "status"})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var result dianaCodingResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Job == nil || result.Job.ID != job.ID || result.Job.Status != codingJobStatusRunning {
		t.Fatalf("job = %#v", result.Job)
	}
	// 运行中「目前说了什么」不是结论，塞进 result 会被模型当成最终答案念出去。
	if result.Job.Result != "" {
		t.Fatalf("运行中不该给出结果：%q", result.Job.Result)
	}
	if result.Job.CanFollowUp {
		t.Fatalf("运行中的任务不该被标成可追加")
	}
}

func TestCodingToolFollowUpNeedsFinishedSession(t *testing.T) {
	useTempCodingWorkspace(t)
	workspace := t.TempDir()
	rt, settings, event := codingTestRuntime(t, "/bin/true", workspace)
	if err := os.MkdirAll(codingJobRecordDir(), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	job := CodingJob{
		ID: "code-old", Backend: codingBackendClaude, Workspace: "demo",
		Status: codingJobStatusSucceeded, StartedAt: time.Now().Add(-time.Minute),
		FinishedAt: time.Now(), LogPath: codingJobLogPath("code-old"),
		Target: codingJobTarget{UserID: "1"},
	}
	if err := saveCodingJob(job); err != nil {
		t.Fatalf("save: %v", err)
	}
	tool := newDianaCodingTool(rt, event, settings)

	// 没有会话 ID 就没法续跑，要说清楚改用 submit，而不是默默起一个新会话。
	_, err := tool.Run(context.Background(), map[string]any{
		"operation": "followup", "job_id": job.ID, "instruction": "再改一点",
	})
	if err == nil || !strings.Contains(err.Error(), "submit") {
		t.Fatalf("err = %v", err)
	}
}

func TestCodingAgentConfigRejectsTemplateWithoutInstruction(t *testing.T) {
	useTempCodingWorkspace(t)
	_, err := codingAgentConfigFromSettings(codingSettings(map[string]any{
		codingAgentSettingBackend:  codingBackendCustom,
		codingAgentSettingCommand:  "/bin/true",
		codingAgentSettingTemplate: "--do-something",
	}))
	if err == nil || !strings.Contains(err.Error(), "{{instruction}}") {
		t.Fatalf("err = %v", err)
	}
}

func TestCodingAgentConfigClampsRuntimeAndConcurrency(t *testing.T) {
	useTempCodingWorkspace(t)
	cfg, err := codingAgentConfigFromSettings(codingSettings(map[string]any{
		codingAgentSettingMaxRuntime:  99999,
		codingAgentSettingConcurrency: 99,
	}))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if cfg.MaxRuntime != maxCodingMaxRuntimeMinutes*time.Minute {
		t.Fatalf("max runtime = %v", cfg.MaxRuntime)
	}
	if cfg.Concurrency != maxCodingConcurrency {
		t.Fatalf("concurrency = %d", cfg.Concurrency)
	}
}

func TestCodingAgentToolStaysOwnerOnly(t *testing.T) {
	// 非主人的可用工具是一份显式白名单。编码代理不在里面，这条测试把它钉住：
	// 有人往白名单里加东西时不会顺手把它一起放进去。
	allowed := RelationshipPolicy{Tier: RelationshipTrusted}.allowedAgentToolNames()
	if allowed[dianaCodingToolName] {
		t.Fatalf("编码代理不能出现在非主人白名单里")
	}
}

func TestCodingToolIsDroppedFromNonOwnerRegistry(t *testing.T) {
	useTempCodingWorkspace(t)
	rt := &Runtime{plugins: NewPluginManager(NewCodingAgentPlugin())}
	cfg := DefaultBotConfig()
	cfg.AgentMCPConfigPath = filepath.Join(t.TempDir(), "missing-mcp.json")
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "someone"}
	tool := newDianaCodingTool(rt, event, codingSettings(nil))

	// 非主人的工具表是一份显式白名单，Retain 会把没收录的工具摘掉。这条测
	// 试钉住的是「工具表这一层就挡住了」，不依赖工具自己再校验身份。
	guest, err := rt.newAgentRegistry(context.Background(), cfg.WithDefaults(), event, RelationshipPolicy{Tier: RelationshipTrusted}, tool)
	if err != nil {
		t.Fatal(err)
	}
	defer guest.Close()
	if _, ok := guest.Get(dianaCodingToolName); ok {
		t.Fatalf("非主人的工具表里出现了 %s", dianaCodingToolName)
	}

	owner, err := rt.newAgentRegistry(context.Background(), cfg.WithDefaults(), MessageEvent{Kind: EventKindPrivate, UserID: "owner"}, RelationshipPolicy{Owner: true}, tool)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if _, ok := owner.Get(dianaCodingToolName); !ok {
		t.Fatalf("主人的工具表里缺少 %s", dianaCodingToolName)
	}
}
