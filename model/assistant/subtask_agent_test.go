// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type subtaskAgentCall struct {
	finish  bool
	request string
}

// 子 agent 并行跑完后再跑一步收尾：收尾拿到每个子 agent 的任务、完整结果和存盘路径，
// 它写的话就是发回会话的回复。
func TestSubtaskAgentBatchHandsResultsToFinish(t *testing.T) {
	var mu sync.Mutex
	var calls []subtaskAgentCall
	saved := map[string]string{}
	batch := subtaskAgentBatch{
		tasks:       []subtaskRequest{{ID: "p1", Objective: "译第 1–2 页"}, {ID: "p2", Objective: "译第 3–4 页"}},
		then:        "按顺序合并成 out.md 发出",
		concurrency: 2,
		run: func(_ context.Context, _ MessageEvent, finish bool, request string) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, subtaskAgentCall{finish, request})
			switch {
			case finish:
				return "合并好发出去了", nil
			case strings.Contains(request, "第 1–2 页"):
				return "第一部分译文<diana-br/>第二行", nil
			}
			return "第二部分译文", nil
		},
		save: func(name string, data []byte) string {
			mu.Lock()
			defer mu.Unlock()
			saved[name] = string(data)
			return "outputs/subtask-x/" + name
		},
	}
	var progress []PluginTaskProgress
	result, err := runSubtaskAgentBatch(context.Background(), batch, func(p PluginTaskProgress) {
		mu.Lock()
		defer mu.Unlock()
		progress = append(progress, p)
	})
	if err != nil || result.Reply != "合并好发出去了" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if len(calls) != 3 || !calls[2].finish {
		t.Fatalf("calls=%#v", calls)
	}
	finish := calls[2].request
	for _, want := range []string{"按顺序合并成 out.md 发出", "译第 1–2 页", "第一部分译文\n第二行", "outputs/subtask-x/01-p1.md", "第二部分译文", "outputs/subtask-x/02-p2.md"} {
		if !strings.Contains(finish, want) {
			t.Fatalf("finish request misses %q:\n%s", want, finish)
		}
	}
	if strings.Index(finish, "第一部分译文") > strings.Index(finish, "第二部分译文") {
		t.Fatalf("results out of order:\n%s", finish)
	}
	if saved["01-p1.md"] != "第一部分译文\n第二行\n" || saved["02-p2.md"] != "第二部分译文\n" {
		t.Fatalf("saved=%#v", saved)
	}
	if len(progress) != 2 || progress[1].Completed != 2 || progress[1].Total != 2 {
		t.Fatalf("progress=%#v", progress)
	}
}

// 结果合起来超过单次上限时不掐头去尾：正文一律不贴，只给路径和字数。
func TestSubtaskAgentResultsOverBudgetGivePathsOnly(t *testing.T) {
	batch := subtaskAgentBatch{tasks: []subtaskRequest{{ID: "a", Objective: "甲"}, {ID: "b", Objective: "乙"}}, then: "合并"}
	outcomes := []subtaskAgentOutcome{
		{output: strings.Repeat("长", 60), path: "outputs/s/01-a.md"},
		{err: errors.New("超时"), output: "只做了一半", path: ""},
	}
	rendered := renderSubtaskAgentResults(batch, outcomes, 50)
	if strings.Contains(rendered, strings.Repeat("长", 60)) || !strings.Contains(rendered, "outputs/s/01-a.md（60 字）") {
		t.Fatalf("rendered:\n%s", rendered)
	}
	// 没存成文件的结果没有别处可读，照样贴出来；失败原因要带上。
	if !strings.Contains(rendered, "只做了一半") || !strings.Contains(rendered, "没做成") {
		t.Fatalf("rendered:\n%s", rendered)
	}
}

// 收尾那一步出错时，子 agent 的结果不能白做：回复里照实说，并给出各部分存在哪。
func TestSubtaskAgentBatchReportsWhenFinishFails(t *testing.T) {
	batch := subtaskAgentBatch{
		tasks: []subtaskRequest{{ID: "p1", Objective: "甲"}, {ID: "p2", Objective: "乙"}},
		then:  "合并",
		run: func(_ context.Context, _ MessageEvent, finish bool, request string) (string, error) {
			switch {
			case finish:
				return "", errors.New("上游超时")
			case strings.Contains(request, "乙"):
				return "", errors.New("读不到文件")
			}
			return "甲的结果", nil
		},
		save: func(name string, _ []byte) string { return "outputs/s/" + name },
	}
	result, err := runSubtaskAgentBatch(context.Background(), batch, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"没做成", "p1：结果在 outputs/s/01-p1.md", "p2：没做成"} {
		if !strings.Contains(result.Reply, want) {
			t.Fatalf("reply misses %q: %s", want, result.Reply)
		}
	}
}

func TestSubtaskToolStartsAgentsInBackground(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	runtime := NewRuntime(BotConfig{BotAccount: "42", AgentEnabled: true}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1"}
	tool := newDianaSubtaskTool(runtime, event)
	finished := make(chan string, 1)
	tool.agentRun = func(_ context.Context, _ MessageEvent, finish bool, request string) (string, error) {
		if finish {
			finished <- request
			return "都做完了", nil
		}
		return "部分结果", nil
	}
	task := map[string]any{"id": "p1", "mode": "agent", "objective": "读 message_id=m1 的 PDF 第 1–20 页并译成中文"}
	if _, err := tool.Run(context.Background(), map[string]any{"tasks": []any{task}, "then": strings.Repeat("长", subtaskAgentThenRunes+1)}); err == nil {
		t.Fatal("oversized then accepted")
	}
	out, err := tool.Run(context.Background(), map[string]any{"tasks": []any{task}, "then": "把译文发成一个 md 文件"})
	if err != nil || !strings.Contains(out, `"status":"started"`) {
		t.Fatalf("out=%s err=%v", out, err)
	}
	select {
	case request := <-finished:
		if !strings.Contains(request, "把译文发成一个 md 文件") || !strings.Contains(request, "部分结果") {
			t.Fatalf("finish request:\n%s", request)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("batch never reached finish")
	}
	// 子 agent 的结果存进了工作目录。
	matches, _ := filepath.Glob(filepath.Join(AgentWorkspaceDir(), "outputs", "subtask-*", "01-p1.md"))
	if len(matches) != 1 {
		t.Fatalf("saved outputs = %v", matches)
	}
	if data, _ := os.ReadFile(matches[0]); string(data) != "部分结果\n" {
		t.Fatalf("saved = %q", data)
	}
	// 只有一层：子 agent 里再派会被拒。
	nested := newDianaSubtaskTool(runtime, event)
	out, err = nested.Run(context.WithValue(context.Background(), subtaskAgentRunKey{}, true), map[string]any{"tasks": []any{task}, "then": "合并"})
	if err != nil || !strings.Contains(out, `"status":"failed"`) || !strings.Contains(out, "不能再派子 agent") {
		t.Fatalf("nested out=%s err=%v", out, err)
	}
}

// 同一会话在跑的批次有上限，结束后名额归还。
func TestSubtaskAgentBatchLimitPerSession(t *testing.T) {
	runtime, _ := subtaskRuntime("unused")
	for i := 0; i < subtaskAgentPerSession; i++ {
		if !runtime.reserveSubtaskAgentBatch("s") {
			t.Fatalf("reservation %d refused", i)
		}
	}
	if runtime.reserveSubtaskAgentBatch("s") {
		t.Fatal("limit not enforced")
	}
	if !runtime.reserveSubtaskAgentBatch("other") {
		t.Fatal("other session blocked")
	}
	runtime.releaseSubtaskAgentBatch("s")
	if !runtime.reserveSubtaskAgentBatch("s") {
		t.Fatal("slot not returned")
	}
}
