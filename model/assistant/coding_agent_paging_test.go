// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/SuInk/diana/model/agent"
)

// runCodingToolWithBudget 模拟 Runner 的单次上限调用编码工具，核对结果没超上限。
func runCodingToolWithBudget(t *testing.T, tool *dianaCodingTool, budget int, input map[string]any) dianaCodingResult {
	t.Helper()
	raw, err := tool.Run(agent.WithToolOutputBudget(context.Background(), budget), input)
	if err != nil {
		t.Fatalf("%v: %v", input, err)
	}
	if got := utf8.RuneCountInString(raw); got > budget {
		t.Fatalf("%v: 结果 %d 字超出单次上限 %d", input["operation"], got, budget)
	}
	var result dianaCodingResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return result
}

func savePagingCodingJob(t *testing.T, job CodingJob, log []string) {
	t.Helper()
	if err := os.MkdirAll(codingJobRecordDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	job.LogPath = codingJobLogPath(job.ID)
	if err := os.WriteFile(job.LogPath, []byte(strings.Join(log, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveCodingJob(job); err != nil {
		t.Fatal(err)
	}
}

// tail 从最新的动作往前按整行装，before_line 往前翻；status 的指令、动作、结果连起来按
// 整行分页，detail_line 续读；list 按整条装，offset 翻页。三种续读拼回来都和原文一致。
func TestCodingToolPagesTailStatusAndList(t *testing.T) {
	useTempCodingWorkspace(t)
	target := codingJobTarget{ProfileID: "bot-a", UserID: "1"}
	var actions []string
	for i := 1; i <= 300; i++ {
		actions = append(actions, fmt.Sprintf("第 %03d 步：%s", i, strings.Repeat("动作", 40)))
	}
	var result []string
	for i := 1; i <= 800; i++ {
		result = append(result, fmt.Sprintf("结论第 %03d 行", i))
	}
	instruction := strings.Repeat("把这段需求写清楚。", 300)
	savePagingCodingJob(t, CodingJob{
		ID: "code-long", Backend: codingBackendClaude, Workspace: "demo", Instruction: instruction,
		Status: codingJobStatusSucceeded, StartedAt: time.Now().Add(-time.Hour), FinishedAt: time.Now(),
		Result: strings.Join(result, "\n"), Target: target,
	}, actions)
	for i := 0; i < 40; i++ {
		savePagingCodingJob(t, CodingJob{
			ID: fmt.Sprintf("code-%02d", i), Backend: codingBackendClaude, Workspace: "demo", Instruction: strings.Repeat("旧任务", 100),
			Status: codingJobStatusSucceeded, StartedAt: time.Now().Add(-time.Duration(i+2) * time.Hour), FinishedAt: time.Now().Add(-time.Duration(i+1) * time.Hour), Target: target,
		}, nil)
	}
	rt, _ := codingOwnerRuntime(t, "bot-a")
	tool := newDianaCodingTool(rt, MessageEvent{Kind: EventKindPrivate, ProfileID: "bot-a", UserID: "1"}, codingSettings(nil))

	var tail []string
	before := 0
	for calls := 1; ; calls++ {
		if calls > 30 {
			t.Fatal("before_line 往前翻没有收敛")
		}
		input := map[string]any{"operation": "tail", "job_id": "code-long", "tail_lines": 1000}
		if before > 0 {
			input["before_line"] = before
		}
		page := runCodingToolWithBudget(t, tool, 6_000, input)
		tail = append(append([]string(nil), page.Tail...), tail...)
		if page.TailFirstLine <= 1 {
			break
		}
		if !strings.Contains(page.Message, fmt.Sprintf("before_line=%d", page.TailFirstLine)) {
			t.Fatalf("往前翻的提示：%q", page.Message)
		}
		before = page.TailFirstLine
	}
	if strings.Join(tail, "\n") != strings.Join(actions, "\n") {
		t.Fatalf("tail 翻回来的动作和日志不一致：%d / %d 行", len(tail), len(actions))
	}

	var detail []string
	line := 0
	for calls := 1; ; calls++ {
		if calls > 30 {
			t.Fatal("detail_line 续读没有收敛")
		}
		input := map[string]any{"operation": "status", "job_id": "code-long"}
		if line > 0 {
			input["detail_line"] = line
		}
		page := runCodingToolWithBudget(t, tool, 6_000, input)
		for _, part := range []string{page.Job.Instruction, page.Job.LastAction, page.Job.Result} {
			if part != "" {
				detail = append(detail, strings.Split(part, "\n")...)
			}
		}
		if page.Job.DetailNextLine == 0 {
			break
		}
		if !strings.Contains(page.Message, fmt.Sprintf("detail_line=%d", page.Job.DetailNextLine)) {
			t.Fatalf("续读提示：%q", page.Message)
		}
		line = page.Job.DetailNextLine
	}
	want := append(append([]string{instruction}, actions[len(actions)-1]), result...)
	if strings.Join(detail, "\n") != strings.Join(want, "\n") {
		t.Fatalf("status 续读拼回来的指令、动作、结果和原文不一致：%d / %d 行", len(detail), len(want))
	}

	seen := map[string]bool{}
	offset := 0
	for calls := 1; ; calls++ {
		if calls > 30 {
			t.Fatal("list 翻页没有收敛")
		}
		page := runCodingToolWithBudget(t, tool, 6_000, map[string]any{"operation": "list", "offset": offset})
		for _, job := range page.Jobs {
			if seen[job.ID] {
				t.Fatalf("任务 %s 列了两次", job.ID)
			}
			seen[job.ID] = true
		}
		if page.NextOffset == 0 {
			break
		}
		if !strings.Contains(page.Message, fmt.Sprintf("offset=%d", page.NextOffset)) {
			t.Fatalf("翻页提示：%q", page.Message)
		}
		offset = page.NextOffset
	}
	if len(seen) != 41 {
		t.Fatalf("list 翻页应列全 41 个任务，got %d", len(seen))
	}
}

// takeTailLines 从最后一行往前装，顺序不变；最后一行自己就装不下时只给开头并记下省略字数。
func TestTakeTailLines(t *testing.T) {
	lines := []string{"一", "二", "三", "四"}
	window := takeTailLines(lines, jsonStringRunes("三")+jsonStringRunes("四")+6)
	if strings.Join(window.lines, ",") != "三,四" || window.cutRunes != 0 {
		t.Fatalf("window=%#v", window)
	}
	long := takeTailLines([]string{"短", strings.Repeat("长", 100)}, 23)
	if len(long.lines) != 1 || long.cutRunes != 80 || long.lines[0] != strings.Repeat("长", 20) {
		t.Fatalf("long=%#v", long)
	}
}
