// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
)

type taskLookupStore struct {
	mu       sync.Mutex
	calls    []AgentToolCallRecord
	subtasks []SessionSubtask
}

func (s *taskLookupStore) AppendMessageEvent(context.Context, string, MessageEvent) error {
	return nil
}

func (s *taskLookupStore) ListRecentMessageEvents(context.Context, string, int) ([]MessageEvent, error) {
	return nil, nil
}

func (s *taskLookupStore) RecordAgentToolCall(_ context.Context, record AgentToolCallRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record.ID = int64(len(s.calls) + 1)
	s.calls = append(s.calls, record)
	return nil
}

// ListAgentToolCalls 和 SQLite 实现同口径：同会话同机器人、ID 倒序、before 之前。
func (s *taskLookupStore) ListAgentToolCalls(_ context.Context, query AgentToolCallQuery) ([]AgentToolCallRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []AgentToolCallRecord
	for index := len(s.calls) - 1; index >= 0 && len(out) < query.Limit; index-- {
		record := s.calls[index]
		if record.Session != query.Session || record.ProfileID != query.ProfileID || (query.Tool != "" && record.Tool != query.Tool) || (query.BeforeID > 0 && record.ID >= query.BeforeID) {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

func (s *taskLookupStore) ListSessionSubtasks(_ context.Context, query SessionSubtaskQuery) ([]SessionSubtask, error) {
	var out []SessionSubtask
	for index := len(s.subtasks) - 1; index >= 0 && len(out) < query.Limit; index-- {
		if item := s.subtasks[index]; query.BeforeSeq == 0 || item.Seq < query.BeforeSeq {
			out = append(out, item)
		}
	}
	return out, nil
}

func listTasks(t *testing.T, runtime *Runtime, event MessageEvent, input map[string]any) dianaTasksResult {
	t.Helper()
	input["operation"] = "list"
	raw, err := newDianaTasksTool(runtime, event).Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	var result dianaTasksResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// 工具调用不跨轮，下一轮要靠 tasks 查上一轮实际调过什么：参数照记，send、tools_execute
// 这类协议层的不记，codemode 只记里面的子调用。
func TestAgentToolCallsAreRecordedAndListedPerSession(t *testing.T) {
	store := &taskLookupStore{}
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	group := MessageEvent{Kind: EventKindGroup, GroupID: "10001", UserID: "20001", MessageID: "m1"}
	other := MessageEvent{Kind: EventKindGroup, GroupID: "10002", UserID: "20001", MessageID: "m2"}
	completed := func(tool string, input map[string]any, err string) agent.RunEvent {
		return agent.RunEvent{Phase: agent.RunPhaseToolCompleted, Tool: tool, ToolInput: input, Error: err, DurationMS: 5}
	}
	ctx := context.Background()
	runtime.recordAgentToolCall(ctx, group, completed(dianaImageToolName, map[string]any{"operation": "edit", "prompt": "腿画肉感一点"}, ""), map[string]any{"operation": "edit", "prompt": "腿画肉感一点"}, "")
	runtime.recordAgentToolCall(ctx, group, completed(agent.FinalizeToolName, nil, ""), nil, "")
	runtime.recordAgentToolCall(ctx, group, completed(agent.CodeModeToolName, map[string]any{"code": "x"}, ""), map[string]any{"code": "x"}, "")
	runtime.recordAgentToolCall(ctx, group, agent.RunEvent{Phase: agent.RunPhaseToolStarted, Tool: agent.WebSearchToolName}, nil, "")
	runtime.recordAgentToolCall(ctx, group, completed(agent.WebSearchToolName, map[string]any{"query": "ti-84 evo"}, "timeout"), map[string]any{"query": "ti-84 evo"}, "timeout")
	runtime.recordAgentToolCall(ctx, other, completed(agent.WebSearchToolName, map[string]any{"query": "别的群"}, ""), map[string]any{"query": "别的群"}, "")

	result := listTasks(t, runtime, group, map[string]any{"kind": "tool_calls"})
	if len(result.ToolCalls) != 2 || result.Items != nil || result.Background != nil {
		t.Fatalf("result=%#v", result)
	}
	search, image := result.ToolCalls[0], result.ToolCalls[1]
	if search.Tool != agent.WebSearchToolName || !search.Failed || search.Error != "timeout" || !strings.Contains(string(search.Input), "ti-84 evo") {
		t.Fatalf("search=%#v", search)
	}
	if image.Tool != dianaImageToolName || image.Failed || image.MessageID != "m1" || !strings.Contains(string(image.Input), "腿画肉感一点") {
		t.Fatalf("image=%#v", image)
	}

	page := listTasks(t, runtime, group, map[string]any{"kind": "tool_calls", "limit": 1})
	if len(page.ToolCalls) != 1 || page.ToolCalls[0].Tool != agent.WebSearchToolName || page.ToolCallsBefore != page.ToolCalls[0].ID {
		t.Fatalf("first page=%#v", page)
	}
	page = listTasks(t, runtime, group, map[string]any{"kind": "tool_calls", "limit": 1, "before": page.ToolCallsBefore})
	if len(page.ToolCalls) != 1 || page.ToolCalls[0].Tool != dianaImageToolName || page.ToolCallsBefore != 0 {
		t.Fatalf("second page=%#v", page)
	}
	filtered := listTasks(t, runtime, group, map[string]any{"kind": "tool_calls", "tool": dianaImageToolName})
	if len(filtered.ToolCalls) != 1 || filtered.ToolCalls[0].Tool != dianaImageToolName {
		t.Fatalf("filtered=%#v", filtered)
	}
}

// 主人在群里跑过的命令、写过的文件，群友查得到调过、查不到参数。
func TestToolCallInputHiddenFromSpeakersWhoCannotUseTheTool(t *testing.T) {
	store := &taskLookupStore{}
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	owner := MessageEvent{Kind: EventKindGroup, GroupID: "10001", UserID: "owner", MessageID: "m1"}
	member := MessageEvent{Kind: EventKindGroup, GroupID: "10001", UserID: "20001", MessageID: "m2"}
	input := map[string]any{"path": "notes/secret.md", "content": "私人笔记"}
	runtime.recordAgentToolCall(context.Background(), owner, agent.RunEvent{Phase: agent.RunPhaseToolCompleted, Tool: "write_file", ToolInput: input}, input, "")

	seen := listTasks(t, runtime, member, map[string]any{"kind": "tool_calls"})
	if len(seen.ToolCalls) != 1 || !seen.ToolCalls[0].InputHidden || seen.ToolCalls[0].Input != nil {
		t.Fatalf("member view=%#v", seen.ToolCalls)
	}
	seen = listTasks(t, runtime, owner, map[string]any{"kind": "tool_calls"})
	if len(seen.ToolCalls) != 1 || seen.ToolCalls[0].InputHidden || !strings.Contains(string(seen.ToolCalls[0].Input), "私人笔记") {
		t.Fatalf("owner view=%#v", seen.ToolCalls)
	}
}

// 排队中的任务还没落库，从运行时取；落了库还在跑的以运行时的阶段为准；库里没结束、
// 运行时里也没有的，是中断留下的，不标 active。
func TestTasksListsRunningAndFinishedBackgroundTasks(t *testing.T) {
	now := time.Now()
	finished := now.Add(-time.Minute)
	store := &taskLookupStore{subtasks: []SessionSubtask{
		{Seq: 1, MessageID: "m1", InboundEventSubtask: InboundEventSubtask{TaskID: "img-old", Kind: "image", Name: "图片编辑", Request: "edit：踩脚袜", Phase: "completed", StartedAt: now.Add(-2 * time.Minute), FinishedAt: &finished}},
		{Seq: 2, MessageID: "m2", InboundEventSubtask: InboundEventSubtask{TaskID: "img-stale", Kind: "image", Name: "图片编辑", Request: "edit：加腿环", Phase: "running", StartedAt: now.Add(-90 * time.Second)}},
		{Seq: 3, MessageID: "m3", InboundEventSubtask: InboundEventSubtask{TaskID: "img-run", Kind: "image", Name: "图片编辑", Request: "edit：腿肉一点", Phase: "running", StartedAt: now.Add(-30 * time.Second)}},
	}}
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "10001", UserID: "20001", MessageID: "m4"}
	prefix := sessionKey(event) + "\x00"
	runtime.subagentMu.Lock()
	runtime.subagentTasks[prefix+"run"] = activeSubagentTask{status: SubagentTaskStatus{ID: "img-run", Kind: "image", Name: "图片编辑", Phase: "running", Completed: 1, Total: 2, StartedAt: now.Add(-30 * time.Second)}}
	runtime.subagentTasks[prefix+"queued"] = activeSubagentTask{status: SubagentTaskStatus{ID: "img-new", Kind: "image", Name: "图片生成", Request: "generate：橘猫", Phase: "queued", StartedAt: now}}
	runtime.subagentTasks["group:10002\x00elsewhere"] = activeSubagentTask{status: SubagentTaskStatus{ID: "img-elsewhere", Phase: "running", StartedAt: now}}
	runtime.subagentMu.Unlock()

	result := listTasks(t, runtime, event, map[string]any{"kind": "background"})
	got := make([]string, 0, len(result.Background))
	for _, item := range result.Background {
		got = append(got, item.ID+":"+item.Phase)
	}
	if strings.Join(got, ",") != "img-new:queued,img-run:running,img-stale:running,img-old:completed" {
		t.Fatalf("background=%v", got)
	}
	queued, running, stale := result.Background[0], result.Background[1], result.Background[2]
	if !queued.Active || queued.Request != "generate：橘猫" {
		t.Fatalf("queued=%#v", queued)
	}
	if !running.Active || running.Completed != 1 || running.Request != "edit：腿肉一点" || running.MessageID != "m3" {
		t.Fatalf("running=%#v", running)
	}
	if stale.Active {
		t.Fatalf("stale=%#v", stale)
	}

	page := listTasks(t, runtime, event, map[string]any{"kind": "background", "limit": 2})
	if page.BackgroundBefore != 2 {
		t.Fatalf("page=%#v", page)
	}
	page = listTasks(t, runtime, event, map[string]any{"kind": "background", "before": page.BackgroundBefore})
	if len(page.Background) != 1 || page.Background[0].ID != "img-old" {
		t.Fatalf("next page=%#v", page.Background)
	}
}

func TestTasksAllKindSkipsSchedulesWithoutReminderStore(t *testing.T) {
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(&taskLookupStore{})
	event := MessageEvent{Kind: EventKindGroup, GroupID: "10001", UserID: "20001"}
	if result := listTasks(t, runtime, event, map[string]any{}); !result.OK {
		t.Fatalf("result=%#v", result)
	}
	if _, err := newDianaTasksTool(runtime, event).Run(context.Background(), map[string]any{"kind": "schedule"}); err == nil {
		t.Fatal("schedule without reminder store should fail")
	}
	if _, err := newDianaTasksTool(runtime, event).Run(context.Background(), map[string]any{"before": 3}); err == nil {
		t.Fatal("before without a paged kind should fail")
	}
}
