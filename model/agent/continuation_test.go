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
	last := first.Transcript[len(first.Transcript)-1]
	if last.Role != llm.RoleAssistant || last.Content != "好" || len(last.ToolCalls) > 0 {
		t.Fatalf("a send-only finish must be recorded as a plain reply: %#v", last)
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

// 只调 send 的收尾记成普通发言：每次 send 一条，不说话的一轮什么都不留。留下 send
// 调用和「已发送」时，模型下一轮会把它读成刚说过，新消息没人接也判「已经回应过」。
func TestTranscriptAfterRecordsSendOnlyFinishAsPlainReplies(t *testing.T) {
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
			transcript := transcriptAfter(request, &llm.GenerateResponse{ToolCalls: tc.calls, ResponsesOutput: []json.RawMessage{json.RawMessage(`{"type":"function_call"}`)}}, "")
			added := transcript[len(request.Messages):]
			var got []string
			for _, message := range added {
				if message.Role != llm.RoleAssistant || len(message.ToolCalls) > 0 || len(message.ResponsesOutput) > 0 {
					t.Fatalf("send-only finish left a tool call behind: %#v", message)
				}
				got = append(got, message.Content)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// 带别的工具的一步照旧配对：供应商拒绝没有结果的调用。
func TestTranscriptAfterKeepsMixedCallsPaired(t *testing.T) {
	request := llm.GenerateRequest{Messages: continuationTestMessages("问题")}
	calls := []llm.ToolCall{
		{ID: "s1", Name: finalizeToolName, Arguments: map[string]any{"text": "我去查一下"}},
		{ID: "t1", Name: "web_search", Arguments: map[string]any{"query": "x"}},
	}
	added := transcriptAfter(request, &llm.GenerateResponse{ToolCalls: calls}, "")[len(request.Messages):]
	if len(added) != 3 || len(added[0].ToolCalls) != 2 || added[1].ToolCallID != "s1" || added[2].ToolCallID != "t1" {
		t.Fatalf("mixed step must keep calls paired with results: %#v", added)
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
