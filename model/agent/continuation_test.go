// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func continuationTestMessages(current string) []llm.Message {
	return []llm.Message{
		{Role: llm.RoleSystem, Content: "人设"},
		{Role: llm.RoleUser, Content: "[历史] 甲: 早"},
		{Role: llm.RoleSystem, Content: "本轮尾部"},
		{Role: llm.RoleUser, Content: current},
	}
}

// 第二轮续接时，发出的请求必须以第一轮的记录逐条开头，后面才是新内容，当前消息最后。
func TestRunnerContinuationIsPureAppend(t *testing.T) {
	client := &silentFinalizeClient{arguments: map[string]any{"text": "好"}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 2, SharedTools: map[string]bool{}}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	first, err := runner.Run(context.Background(), Request{Messages: continuationTestMessages("第一问")})
	if err != nil {
		t.Fatal(err)
	}
	if first.Continued || len(first.Transcript) < 2 || first.ConversationKey == "" {
		t.Fatalf("first = %#v", first)
	}
	call, result := first.Transcript[len(first.Transcript)-2], first.Transcript[len(first.Transcript)-1]
	if call.Role != llm.RoleAssistant || len(call.ToolCalls) != 1 || sendText(call.ToolCalls[0]) != "好" ||
		result.Role != llm.RoleTool || result.ToolCallID != call.ToolCalls[0].ID || result.Content != historyReplyResult {
		t.Fatalf("a send-only finish must stay a send call with its result: %#v / %#v", call, result)
	}
	second, err := runner.Run(context.Background(), Request{
		Messages: continuationTestMessages("第二问"),
		Continuation: &Continuation{
			Prefix: first.Transcript,
			Append: []llm.Message{{Role: llm.RoleUser, Content: "[历史] 乙: 在吗"}, {Role: llm.RoleUser, Content: "第二问"}},
			Key:    first.ConversationKey,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Continued {
		t.Fatal("matching key must continue")
	}
	sent := client.requests[1].Messages
	if len(sent) <= len(first.Transcript) {
		t.Fatalf("second request did not grow: %d", len(sent))
	}
	for index, message := range first.Transcript {
		got := sent[index]
		got.CacheBreakpoint = false
		if !reflect.DeepEqual(got, message) {
			t.Fatalf("prefix differs at %d:\n got %#v\nwant %#v", index, got, message)
		}
	}
	if sent[len(sent)-1].Content != "第二问" {
		t.Fatalf("current message is not last: %#v", sent[len(sent)-1])
	}
	environments := 0
	for _, message := range sent {
		if message.Role == llm.RoleSystem && strings.HasPrefix(message.Content, speakerToolPromptMarker+"\n") {
			environments++
		}
	}
	if environments != 1 {
		t.Fatalf("unchanged environment repeated: %d blocks", environments)
	}
}

// 指纹对不上（系统提示、契约或工具定义变了）就退回完整组装。
func TestRunnerContinuationFallsBackOnKeyMismatch(t *testing.T) {
	client := &silentFinalizeClient{arguments: map[string]any{"text": "好"}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 2}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{
		Messages: continuationTestMessages("问题"),
		Continuation: &Continuation{
			Prefix: []llm.Message{{Role: llm.RoleSystem, Content: "旧的"}},
			Append: []llm.Message{{Role: llm.RoleUser, Content: "问题"}},
			Key:    "stale",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Continued {
		t.Fatal("stale key must not continue")
	}
	for _, message := range client.requests[0].Messages {
		if message.Content == "旧的" {
			t.Fatal("stale prefix leaked into the request")
		}
	}
}

// 只调 send 的收尾原样留成 send 调用加结果，和重新组装时历史里自己的发言同一个形态；
// 不说话的一轮什么都不留。
func TestTranscriptAfterKeepsSendOnlyFinishAsCalls(t *testing.T) {
	request := llm.GenerateRequest{Messages: continuationTestMessages("问题")}
	cases := []struct {
		name  string
		calls []llm.ToolCall
		want  []string
	}{
		{"one send", []llm.ToolCall{{ID: "s1", Name: finalizeToolName, Arguments: map[string]any{"text": "好"}}}, []string{"好"}},
		{"two sends", []llm.ToolCall{
			{ID: "s1", Name: finalizeToolName, Arguments: map[string]any{"text": "第一条"}},
			{ID: "s2", Name: finalizeToolName, Arguments: map[string]any{"text": "第二条"}},
		}, []string{"第一条", "第二条"}},
		{"silent", []llm.ToolCall{{ID: "s1", Name: finalizeToolName, Arguments: map[string]any{"text": "", "silent": true}}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			transcript := transcriptAfter(request.Messages, nil, &llm.GenerateResponse{ToolCalls: tc.calls, ResponsesOutput: []json.RawMessage{json.RawMessage(`{"type":"function_call"}`)}}, "")
			added := transcript[len(request.Messages):]
			if tc.want == nil {
				if len(added) != 0 {
					t.Fatalf("silent finish left messages behind: %#v", added)
				}
				return
			}
			if len(added) != 1+len(tc.calls) || len(added[0].ToolCalls) != len(tc.calls) || len(added[0].ResponsesOutput) == 0 {
				t.Fatalf("send-only finish must keep its native calls: %#v", added)
			}
			var got []string
			for index, call := range added[0].ToolCalls {
				got = append(got, sendText(call))
				if result := added[1+index]; result.Role != llm.RoleTool || result.ToolCallID != call.ID || result.Content != historyReplyResult {
					t.Fatalf("result %d not paired: %#v", index, result)
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// 和 send 同一步的别的工具调用不进下一轮前缀，那一步的原生输出带着这些调用，一起丢掉。
func TestTranscriptAfterDropsNonSendCallsInFinishStep(t *testing.T) {
	request := llm.GenerateRequest{Messages: continuationTestMessages("问题")}
	calls := []llm.ToolCall{
		{ID: "s1", Name: finalizeToolName, Arguments: map[string]any{"text": "我去查一下"}},
		{ID: "t1", Name: "web_search", Arguments: map[string]any{"query": "x"}},
	}
	added := transcriptAfter(request.Messages, nil, &llm.GenerateResponse{ToolCalls: calls, ResponsesOutput: []json.RawMessage{json.RawMessage(`{"type":"function_call"}`)}}, "")[len(request.Messages):]
	if len(added) != 2 || len(added[0].ToolCalls) != 1 || added[0].ToolCalls[0].ID != "s1" || len(added[0].ResponsesOutput) != 0 || added[1].ToolCallID != "s1" {
		t.Fatalf("only the send call may stay: %#v", added)
	}
}

// 上一轮调过工具，下一轮的前缀里是这一轮开始时的请求、执行成功的调用加占位结果、收尾的
// send。结果不留：11-22 续接把上一轮 relationship 的调用和结果留在前缀里，下一轮被问
// 「详细记录」时照抄旧结果没再查。调用要留：只剩 send 时模型照着历史只说不做，11-23 线上
// 5 次回「正在画」却没调 image。
func TestRunnerTranscriptKeepsToolCallsWithoutResults(t *testing.T) {
	search := &recordingSearchTool{output: `{"results":[{"title":"旧结果"}]}`}
	client := &mixedReplyClient{responses: []*llm.GenerateResponse{
		{ToolCalls: []llm.ToolCall{{ID: "w1", Name: WebSearchToolName, Arguments: map[string]any{"query": "x"}}}},
		{ToolCalls: []llm.ToolCall{{ID: "s1", Name: finalizeToolName, Arguments: map[string]any{"text": "查到了"}}}},
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 4, SharedTools: map[string]bool{}}, NewToolRegistry(search))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: continuationTestMessages("查一下")})
	if err != nil {
		t.Fatal(err)
	}
	if search.calls != 1 || len(client.requests) != 2 {
		t.Fatalf("calls=%d requests=%d", search.calls, len(client.requests))
	}
	first := client.requests[0].Messages
	if len(resp.Transcript) != len(first)+4 {
		t.Fatalf("transcript = %#v", resp.Transcript)
	}
	for index, message := range first {
		got, want := resp.Transcript[index], message
		got.CacheBreakpoint, want.CacheBreakpoint = false, false
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("prefix differs from the first request at %d:\n got %#v\nwant %#v", index, got, want)
		}
	}
	for _, message := range resp.Transcript {
		if strings.Contains(message.Content, "旧结果") {
			t.Fatalf("tool result leaked into the next prefix: %#v", message)
		}
	}
	kept, placeholder := resp.Transcript[len(first)], resp.Transcript[len(first)+1]
	if !llm.IsHistoryToolMessage(kept) || len(kept.ToolCalls) != 1 || kept.ToolCalls[0].Name != WebSearchToolName || kept.ToolCalls[0].Arguments["query"] != "x" {
		t.Fatalf("executed call missing from the next prefix: %#v", kept)
	}
	if !llm.IsHistoryToolMessage(placeholder) || placeholder.ToolCallID != kept.ToolCalls[0].ID || placeholder.Content != historyToolResult {
		t.Fatalf("placeholder result not paired: %#v", placeholder)
	}
	call := resp.Transcript[len(resp.Transcript)-2]
	if len(call.ToolCalls) != 1 || sendText(call.ToolCalls[0]) != "查到了" || resp.Transcript[len(resp.Transcript)-1].Content != historyReplyResult {
		t.Fatalf("final send missing: %#v", resp.Transcript[len(resp.Transcript)-2:])
	}
}

func TestContinuationVolatileOnlyOmitsLatestUnchangedEnvironment(t *testing.T) {
	env := func(text string) llm.Message {
		return llm.Message{Role: llm.RoleSystem, Content: speakerToolPromptMarker + "\n" + text}
	}
	a, b := env("A"), env("B")
	clock := llm.Message{Role: llm.RoleSystem, Content: RuntimeClockMarker + "now"}
	for _, tc := range []struct {
		name                             string
		prefix, appended, volatile, want []llm.Message
	}{
		{"same", []llm.Message{a}, nil, []llm.Message{a, clock}, []llm.Message{clock}},
		{"changed", []llm.Message{a}, nil, []llm.Message{b, clock}, []llm.Message{b, clock}},
		{"restore", []llm.Message{a, b}, nil, []llm.Message{a}, []llm.Message{a}},
		{"appended supersedes", []llm.Message{a}, []llm.Message{b}, []llm.Message{a}, []llm.Message{a}},
		{"clock not deduped", []llm.Message{clock}, nil, []llm.Message{clock}, []llm.Message{clock}},
		{"user cannot suppress", []llm.Message{{Role: llm.RoleUser, Content: a.Content}}, nil, []llm.Message{a}, []llm.Message{a}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := json.Marshal(tc.prefix)
			got := continuationVolatile(tc.prefix, tc.appended, tc.volatile)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
			after, _ := json.Marshal(tc.prefix)
			if string(before) != string(after) {
				t.Fatal("historical prefix mutated")
			}
		})
	}
}

func contractTestRunner(t *testing.T, client llm.LLMClient, names ...string) *Runner {
	t.Helper()
	tools := []Tool{&countingTool{name: "common"}}
	for _, name := range names {
		tools = append(tools, &countingTool{name: name})
	}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 2, CoreTools: []string{"common"}}, NewToolRegistry(tools...))
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func contractMessagesIn(messages []llm.Message) (blocks []string) {
	for _, message := range messages {
		if strings.HasPrefix(message.Content, systemNoteHeader) && strings.Contains(message.Content, sharedContractsLead) {
			blocks = append(blocks, message.Content)
		}
	}
	return blocks
}

// 群里又加载了一个工具时，续接照旧：历史之前的契约块一个字不改，新契约接在尾部，
// 再下一轮不重复发。以前契约算在续接指纹里，多加载一个工具就整条重新组装，契约块
// 排在历史之前，它一变后面的缓存全部作废。
func TestRunnerContinuationAppendsNewlyLoadedContract(t *testing.T) {
	client := &silentFinalizeClient{arguments: map[string]any{"text": "好"}}
	runner := contractTestRunner(t, client, "platform", "bot_config")
	first, err := runner.Run(context.Background(), Request{Messages: continuationTestMessages("第一问"), LoadedTools: []string{"platform"}})
	if err != nil {
		t.Fatal(err)
	}
	if blocks := contractMessagesIn(first.Transcript); len(blocks) != 1 || !strings.Contains(blocks[0], `"name":"platform"`) || strings.Contains(blocks[0], "bot_config") {
		t.Fatalf("first contract blocks = %q", blocks)
	}
	next := func(prefix []llm.Message, key, current string) *Response {
		t.Helper()
		resp, err := runner.Run(context.Background(), Request{
			Messages:     continuationTestMessages(current),
			LoadedTools:  []string{"platform", "bot_config"},
			Continuation: &Continuation{Prefix: prefix, Append: []llm.Message{{Role: llm.RoleUser, Content: current}}, Key: key},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !resp.Continued {
			t.Fatalf("loading one more tool must not break the continuation (key %s -> %s)", key, resp.ConversationKey)
		}
		return resp
	}
	second := next(first.Transcript, first.ConversationKey, "第二问")
	sent := client.requests[1].Messages
	for index, message := range first.Transcript {
		got := sent[index]
		got.CacheBreakpoint = false
		if !reflect.DeepEqual(got, message) {
			t.Fatalf("prefix changed at %d:\n got %#v\nwant %#v", index, got, message)
		}
	}
	blocks := contractMessagesIn(sent)
	if len(blocks) != 2 || !strings.Contains(blocks[1], `"name":"bot_config"`) || strings.Contains(blocks[1], `"name":"platform"`) {
		t.Fatalf("the new contract must be appended alone: %q", blocks)
	}
	if sent[len(sent)-1].Content != "第二问" {
		t.Fatalf("current message is not last: %#v", sent[len(sent)-1])
	}
	next(second.Transcript, second.ConversationKey, "第三问")
	if blocks := contractMessagesIn(client.requests[2].Messages); len(blocks) != 2 {
		t.Fatalf("a delivered contract was sent again: %d blocks", len(blocks))
	}
}

// 前缀里有、当前发言者却用不了的契约留在原处，补一句说明；说明只在变化时发，换回
// 能用的人也要明说。
func TestContractDeltaReportsAvailabilityOnlyWhenItChanges(t *testing.T) {
	platform := loadedContract{name: "platform", raw: `{"name":"platform","description":"群管","inputSchema":{}}`}
	config := loadedContract{name: "bot_config", raw: `{"name":"bot_config","description":"配置","inputSchema":{}}`}
	prefix := []llm.Message{
		{Role: llm.RoleSystem, Content: "人设"},
		{Role: llm.RoleUser, Content: sharedContractsMessage(renderLoadedContracts([]loadedContract{platform, config})) + "\n\n## Skills"},
	}
	if delta := contractDelta([]loadedContract{platform, config}, prefix, nil); len(delta) != 0 {
		t.Fatalf("nothing changed, nothing to append: %#v", delta)
	}
	// 普通成员：bot_config 用不了。
	delta := contractDelta([]loadedContract{platform}, prefix, nil)
	if len(delta) != 1 || delta[0].Role != llm.RoleSystem || !strings.Contains(delta[0].Content, "用不了：bot_config。") {
		t.Fatalf("blocked contract notice = %#v", delta)
	}
	prefix = append(prefix, delta...)
	if delta := contractDelta([]loadedContract{platform}, prefix, nil); len(delta) != 0 {
		t.Fatalf("the same notice was repeated: %#v", delta)
	}
	// 换回管理员：要明说都能用，否则模型沿用上一句。
	delta = contractDelta([]loadedContract{platform, config}, prefix, nil)
	if len(delta) != 1 || !strings.Contains(delta[0].Content, "都能用") {
		t.Fatalf("restored availability notice = %#v", delta)
	}
	// 定义变了的契约按新的再发一次。
	changed := loadedContract{name: "platform", raw: `{"name":"platform","description":"群管（新）","inputSchema":{}}`}
	delta = contractDelta([]loadedContract{changed, config}, prefix[:2], nil)
	if len(delta) != 1 || !strings.Contains(delta[0].Content, "群管（新）") || strings.Contains(delta[0].Content, "bot_config") {
		t.Fatalf("changed contract delta = %#v", delta)
	}
	if got := deliveredContracts(append(prefix[:2:2], delta...)); got["platform"] != changed.raw {
		t.Fatalf("latest delivery must win: %q", got["platform"])
	}
}

// 没执行的、执行失败的调用和 send 不进历史；成功的按顺序留下，ID 换成历史前缀。
func TestSucceededToolCallsSkipsFailedAndUnexecuted(t *testing.T) {
	executed := []llm.Message{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "a", Name: "image", Arguments: map[string]any{"prompt": "猫"}},
			{ID: "b", Name: WebSearchToolName, Arguments: map[string]any{"query": "x"}},
			{ID: "c", Name: finalizeToolName, Arguments: map[string]any{"text": "好"}},
		}},
		{Role: llm.RoleTool, ToolCallID: "a", Content: "queued"},
		{Role: llm.RoleTool, ToolCallID: "b", Content: "timeout", ToolError: true},
		{Role: llm.RoleTool, ToolCallID: "c", Content: "这次 send 没有发出", ToolError: true},
		{Role: llm.RoleSystem, Content: "提醒"},
	}
	calls := SucceededToolCalls(executed)
	if len(calls) != 1 || calls[0].ID != "a" || calls[0].Name != "image" {
		t.Fatalf("calls = %#v", calls)
	}
	messages := HistoryToolMessages("k", calls)
	if len(messages) != 2 || !llm.IsHistoryToolMessage(messages[0]) || !llm.IsHistoryToolMessage(messages[1]) || messages[1].Content != historyToolResult || strings.Contains(messages[1].Content, "queued") {
		t.Fatalf("messages = %#v", messages)
	}
	if HistoryToolMessages("k", nil) != nil {
		t.Fatal("no calls should render nothing")
	}
}
