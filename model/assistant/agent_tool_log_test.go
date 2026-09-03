// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

func TestAgentRunObserverWritesCorrelatedLifecycleLogs(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := &Runtime{}
	runtime.SetAppLogWriter(logs)
	event := MessageEvent{
		Kind:      EventKindGroup,
		GroupID:   "20001",
		UserID:    "10001",
		MessageID: "30001",
	}
	observe := runtime.agentRunObserver(event)
	observe(context.Background(), agent.RunEvent{
		TraceID:      "trace-1",
		Phase:        agent.RunPhaseToolStarted,
		ModelTurn:    2,
		ToolCall:     1,
		MaxToolCalls: 8,
		Tool:         "demo.tool",
		InputKeys:    []string{"query"},
	})
	observe(context.Background(), agent.RunEvent{
		TraceID:      "trace-1",
		Phase:        agent.RunPhaseCompleted,
		ModelTurn:    3,
		ToolCall:     1,
		MaxToolCalls: 8,
		DurationMS:   42,
		FinishReason: "final",
	})

	if len(logs.entries) != 2 {
		t.Fatalf("entries = %#v", logs.entries)
	}
	started := logs.entries[0]
	if started.Action != "diana.agent_tool" || started.Message != "Agent 工具调用开始 [#-------] 1/8" || started.Target != "demo.tool" {
		t.Fatalf("tool log = %#v", started)
	}
	if started.Metadata["trace_id"] != "trace-1" || started.Metadata["message_id"] != "30001" || started.Metadata["tool_call"] != 1 || started.Metadata["progress_percent"] != 12 {
		t.Fatalf("tool metadata = %#v", started.Metadata)
	}
	completed := logs.entries[1]
	if completed.Action != "diana.agent_run" || completed.Message != "Agent 运行完成 [########] done" || completed.Metadata["finish_reason"] != "final" || completed.Metadata["progress_percent"] != 100 {
		t.Fatalf("completed log = %#v", completed)
	}
}

func TestAgentRunObserverKeepsWebSearchOperationLogsPrivate(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := &Runtime{}
	runtime.SetAppLogWriter(logs)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "private-group", UserID: "private-user", MessageID: "private-message"}
	runtime.agentRunObserver(event)(context.Background(), agent.RunEvent{
		TraceID:   "trace-search",
		Phase:     agent.RunPhaseToolCompleted,
		Tool:      agent.WebSearchToolName,
		ToolInput: map[string]any{"query": "private raw query"},
		Metadata: map[string]any{
			"status":   "no_results",
			"strategy": "bounded_query_exploration",
			"queries":  []map[string]any{{"hash": "abc123", "length": 17, "language": "en"}},
		},
	})
	entries := logs.entriesSnapshot()
	if len(entries) != 1 {
		t.Fatalf("entries = %#v", entries)
	}
	entry := entries[0]
	if entry.Actor != "" || entry.Target != agent.WebSearchToolName {
		t.Fatalf("entry identity = %#v", entry)
	}
	for _, key := range []string{"group_id", "user_id", "message_id"} {
		if _, exists := entry.Metadata[key]; exists {
			t.Fatalf("metadata retained %s: %#v", key, entry.Metadata)
		}
	}
	body, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, private := range []string{"private-group", "private-user", "private-message", "private raw query"} {
		if strings.Contains(text, private) {
			t.Fatalf("operation log leaked %q: %s", private, text)
		}
	}
	if !strings.Contains(text, "abc123") || !strings.Contains(text, "bounded_query_exploration") {
		t.Fatalf("safe search diagnostics missing: %s", text)
	}
}

func TestAgentRunObserverRedactsOneBotV11DebugPayload(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := NewRuntime(BotConfig{DebugModeEnabled: true}, nilChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(logs)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "owner", MessageID: "message-1"}
	ctx := runtime.withDebugTraceContext(context.Background(), event)
	runtime.agentRunObserver(event)(ctx, agent.RunEvent{
		Phase:      agent.RunPhaseToolCompleted,
		Tool:       dianaOneBotV11ToolName,
		InputKeys:  []string{"action", "params"},
		ToolInput:  map[string]any{"action": "get_credentials", "params": map[string]any{"domain": "secret.example"}},
		ToolOutput: `{"ok":true,"data":{"cookies":"owner-secret"}}`,
		Error:      "adapter rejected owner-secret for secret.example",
	})
	entries := logs.entriesSnapshot()
	if len(entries) != 2 {
		t.Fatalf("entries = %#v", entries)
	}
	debug := entries[1]
	if strings.Contains(entries[0].Detail, "owner-secret") || strings.Contains(entries[0].Detail, "secret.example") {
		t.Fatalf("operation log leaked OneBot error: %#v", entries[0])
	}
	input, _ := debug.Metadata["tool_input"].(map[string]any)
	if input["action"] != "get_credentials" || strings.Contains(debug.Metadata["tool_output"].(string), "owner-secret") {
		t.Fatalf("debug payload = %#v", debug.Metadata)
	}
	if strings.Contains(strings.TrimSpace(debug.Metadata["tool_output"].(string)), "secret.example") {
		t.Fatalf("debug payload leaked parameter value: %#v", debug.Metadata)
	}
	if strings.Contains(debug.Metadata["error"].(string), "owner-secret") || strings.Contains(debug.Metadata["error"].(string), "secret.example") {
		t.Fatalf("debug payload leaked OneBot error: %#v", debug.Metadata)
	}
}

func TestAgentRunObserverRedactsRepositoryIssueRequestPayload(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := NewRuntime(BotConfig{DebugModeEnabled: true}, nilChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(logs)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "owner", MessageID: "message-2"}
	ctx := runtime.withDebugTraceContext(context.Background(), event)
	runtime.agentRunObserver(event)(ctx, agent.RunEvent{
		Phase:      agent.RunPhaseToolCompleted,
		Tool:       dianaRepositoryIssuesToolName,
		ToolInput:  map[string]any{"operation": "create", "repository": "acme/demo", "title": "private title", "body": "token=owner-secret"},
		ToolOutput: `{"ok":true,"issue":{"number":12,"title":"private title"}}`,
		Error:      "GitHub rejected owner-secret",
	})
	entries := logs.entriesSnapshot()
	if len(entries) != 2 {
		t.Fatalf("entries = %#v", entries)
	}
	operation, debug := entries[0], entries[1]
	// 常规运行日志一直在写，跟「调试追踪」开关无关，它必须一个字都不带。
	if encoded := fmt.Sprintf("%#v", operation); strings.Contains(encoded, "private title") || strings.Contains(encoded, "owner-secret") {
		t.Fatalf("常规运行日志泄漏了内容：%#v", operation)
	}
	// 调试追踪里工具结果会给摘要（标题在内），但请求参数仍然只留键名：正文和凭据
	// 都在参数里，那份不能出去。
	if encoded := fmt.Sprintf("%#v", debug.Metadata["tool_input"]); strings.Contains(encoded, "private title") || strings.Contains(encoded, "owner-secret") {
		t.Fatalf("调试追踪泄漏了请求参数值：%#v", debug.Metadata["tool_input"])
	}
	if strings.Contains(fmt.Sprintf("%#v", debug), "owner-secret") {
		t.Fatalf("调试追踪泄漏了凭据：%#v", debug)
	}
	if !strings.Contains(debug.Metadata["tool_output"].(string), "private title") {
		t.Fatalf("工具结果摘要里应该看得到标题：%#v", debug.Metadata["tool_output"])
	}
	if operation.Detail != "[repository issue tool error omitted]" {
		t.Fatalf("operation detail = %q", operation.Detail)
	}
	input, _ := debug.Metadata["tool_input"].(map[string]any)
	if input["operation"] != "create" || input["repository"] != "acme/demo" {
		t.Fatalf("debug input = %#v", input)
	}
	if debug.Metadata["error"] != "[repository issue tool error omitted]" {
		t.Fatalf("debug metadata = %#v", debug.Metadata)
	}
	// 状态字段要透出来（没有它们就没法排查写操作为什么被拒），Issue 标题和
	// 正文仍然一个字都不能出现——上面的泄漏检查已经覆盖了这一点。
	output, _ := debug.Metadata["tool_output"].(string)
	if !strings.Contains(output, `"ok":true`) {
		t.Fatalf("tool_output 里没有状态字段：%q", output)
	}
	if !strings.Contains(output, `"number":12`) {
		t.Fatalf("tool_output 里没有条目摘要：%q", output)
	}
}

func TestRepositoryIssueDebugOutcomeSurfacesFailureCode(t *testing.T) {
	// 写操作被闸门拒绝时，追踪里必须看得出是哪一道闸——此前一律只有
	// 「output omitted」，排查时完全是黑箱。
	output := repositoryIssueDebugOutcome(`{"ok":false,"operation":"create","failure_code":"explicit_fields_required","message":"创建 Issue 时，当前用户消息必须包含要发布的 title 内容。"}`)
	for _, want := range []string{`"ok":false`, "explicit_fields_required", "必须包含要发布的 title 内容"} {
		if !strings.Contains(output, want) {
			t.Errorf("追踪里缺少 %q：%s", want, output)
		}
	}
}

// 只报「共找到 2 条草稿」等于没记：看得见有两条，看不出是哪两条。摘要要能认出条目，
// 正文截断但标出总字数。
func TestRepositoryIssueDebugOutcomeSummarizesItemsAndDrafts(t *testing.T) {
	body := strings.Repeat("正文", 200)
	output := repositoryIssueDebugOutcome(`{"ok":true,"operation":"list_drafts","outcome":"listed","message":"共找到 1 条 Issue 草稿。","drafts":[{"id":"draft-1","title":"分条把反问粘上来了","status":"pending","body":"` + body + `"}]}`)
	for _, want := range []string{"draft-1", "分条把反问粘上来了", "pending", `"body_chars":400`} {
		if !strings.Contains(output, want) {
			t.Errorf("摘要里缺少 %q：%s", want, output)
		}
	}
	// 正文截到 120 字，不是整篇抄进日志。
	if strings.Contains(output, body) {
		t.Fatalf("正文没有截断：%s", output)
	}

	issues := repositoryIssueDebugOutcome(`{"ok":true,"operation":"list","outcome":"listed","items":[{"number":236,"title":"关系图不用浏览器也能画","state":"open","url":"https://example.invalid/236"}]}`)
	for _, want := range []string{`"number":236`, "关系图不用浏览器也能画", "open", "example.invalid/236"} {
		if !strings.Contains(issues, want) {
			t.Errorf("摘要里缺少 %q：%s", want, issues)
		}
	}
}

// 条数多的时候只列前几条，剩下的报个数——一次列几十条 Issue 全抄进去，这一步的
// 记录就没法读了。
func TestRepositoryIssueDebugOutcomeCapsListedItems(t *testing.T) {
	items := make([]string, 0, 9)
	for index := 1; index <= 9; index++ {
		items = append(items, fmt.Sprintf(`{"number":%d,"title":"第 %d 条","state":"open"}`, index, index))
	}
	output := repositoryIssueDebugOutcome(`{"ok":true,"operation":"list","outcome":"listed","items":[` + strings.Join(items, ",") + `]}`)
	if !strings.Contains(output, `"items_not_listed":4`) {
		t.Fatalf("没有报出被省略的条数：%s", output)
	}
	if strings.Contains(output, "第 6 条") {
		t.Fatalf("超出上限的条目仍被列出：%s", output)
	}
}

func TestRepositoryIssueDebugOutcomeFallsBackOnUnparsableOutput(t *testing.T) {
	if got := repositoryIssueDebugOutcome("not json at all，可能含正文"); got != "[repository issue tool output omitted]" {
		t.Fatalf("解析不了的输出必须整体挡掉，实际 %q", got)
	}
}

// 调用开始和调用完成是同一次调用的两条记录，开始那条的工具输出必然是空的。脱敏
// 层此前不分青红皂白地写上「输出已省略」，界面上就多出一个「工具结果」框，看起来
// 像结果被挡掉了——实际是还没有结果。
func TestDebugToolCallSanitizersLeaveMissingOutputEmpty(t *testing.T) {
	if got := repositoryIssueDebugOutcome(""); got != "" {
		t.Fatalf("没有输出时不该给占位串：%q", got)
	}
	if got := repositoryIssueDebugOutcome("   "); got != "" {
		t.Fatalf("空白输出不该给占位串：%q", got)
	}
	if _, got := sanitizeOneBotV11DebugToolCall(map[string]any{"action": "send_msg"}, ""); got != "" {
		t.Fatalf("没有输出时不该给占位串：%q", got)
	}
	// 真的有输出时照旧挡住。
	if _, got := sanitizeOneBotV11DebugToolCall(map[string]any{"action": "send_msg"}, `{"ok":true}`); got == "" {
		t.Fatal("有输出时必须挡住")
	}
}
