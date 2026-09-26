// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func submitCodingJob(t *testing.T, tool *dianaCodingTool) dianaCodingResult {
	t.Helper()
	output, err := tool.Run(context.Background(), map[string]any{"operation": "submit", "instruction": "echo diana-cli-ok"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	var result dianaCodingResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return result
}

// 账号被封、认证失败这类错几秒内就结束。以前汇报单独推出去，比派活那一轮的
// 「已经在后台跑了」还先到。现在结果直接交给那一轮，不再另发一条。
func TestCodingSubmitHandsQuickFailureToCurrentTurn(t *testing.T) {
	useTempCodingWorkspace(t)
	cli := fakeCodingCLI(t, `echo '{"type":"result","subtype":"success","is_error":true,"result":"Your account is on hold and can'"'"'t use Claude Code.","num_turns":1,"session_id":"s1"}'`)
	rt, settings, event := codingTestRuntime(t, cli, t.TempDir())
	result := submitCodingJob(t, newDianaCodingTool(rt, event, settings))

	if result.Job == nil || result.Job.Status != codingJobStatusFailed || !strings.Contains(result.Job.Error, "on hold") {
		t.Fatalf("工具返回里应当直接是失败结果：%#v", result.Job)
	}
	if strings.Contains(result.Message, "后台启动") {
		t.Fatalf("已经结束的任务不该再说在后台跑：%q", result.Message)
	}
	if !codingJobReported(t, result.Job.ID) {
		t.Fatalf("交给这一轮的结果要记成已汇报，否则重启接回会再发一遍")
	}
	// 给守望协程留出收尾时间，确认它没有另推一条汇报。
	drainCodingJobs(t, rt)
	if sent := rt.channel.(*concurrentRecordingChannel).count(); sent != 0 {
		t.Fatalf("结果已经交给这一轮，不该再单独汇报，实际发了 %d 条", sent)
	}
}

// 等待窗口过了还没结束的任务照旧：先回任务号，跑完再单独汇报，而且只汇报一次。
func TestCodingSubmitReportsSeparatelyAfterHandOffWindow(t *testing.T) {
	useTempCodingWorkspace(t)
	cli := fakeCodingCLI(t, `sleep 0.5
echo '{"type":"result","subtype":"success","is_error":false,"result":"diana-cli-ok","num_turns":1,"session_id":"s2"}'`)
	rt, settings, event := codingTestRuntime(t, cli, t.TempDir())
	registry := rt.codingJobs()
	registry.mu.Lock()
	registry.reportTiming = codingReportTiming{handOff: 50 * time.Millisecond}
	registry.mu.Unlock()
	result := submitCodingJob(t, newDianaCodingTool(rt, event, settings))

	if result.Job == nil || result.Job.Status != codingJobStatusRunning || !strings.Contains(result.Message, "后台启动") {
		t.Fatalf("窗口内没结束应当返回在跑：%#v %q", result.Job, result.Message)
	}
	channel := rt.channel.(*concurrentRecordingChannel)
	waitForCondition(t, 10*time.Second, func() bool { return channel.count() >= 1 })
	if report := channel.messages()[0].Text; !strings.Contains(report, result.Job.ID) || !strings.Contains(report, "diana-cli-ok") {
		t.Fatalf("report = %q", report)
	}
	drainCodingJobs(t, rt)
	if sent := channel.count(); sent != 1 {
		t.Fatalf("汇报应当恰好一条，实际 %d 条", sent)
	}
	registry.mu.Lock()
	holds := len(registry.reportHolds)
	registry.mu.Unlock()
	if holds != 0 {
		t.Fatalf("等待登记没撤掉：%d", holds)
	}
}
