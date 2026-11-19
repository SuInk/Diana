// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
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
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 2, HeadRegistry: NewToolRegistry(&describedTool{"coding", "仅主人"})}, NewToolRegistry())
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
	if spoken := transcriptPairs(t, first.Transcript); !reflect.DeepEqual(spoken, []string{"好"}) || first.Transcript[len(first.Transcript)-1].Role != llm.RoleTool {
		t.Fatalf("a send-only finish must end the transcript as a send call with its result: %q", spoken)
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
	if sent[lastKeptIndex(sent)].Content != "第二问" {
		t.Fatalf("current message is not the last kept message: %#v", sent)
	}
	// 上一轮的时钟这类当轮状态也留在前缀里，这一轮另发新的。
	clocks := 0
	for _, message := range sent {
		if strings.HasPrefix(message.Content, RuntimeClockMarker) {
			clocks++
		}
	}
	if clocks != 2 {
		t.Fatalf("clock notes in the second request = %d, want last turn's and this turn's", clocks)
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

// 头部锁定时会话键对不上也续接：前缀里的旧头部和调用方存的工具定义原样发，本轮
// 渲染出来的不用，返回的键和定义也还是锁定的那份。
func TestRunnerContinuationLockedHeadIgnoresKeyMismatch(t *testing.T) {
	client := &silentFinalizeClient{arguments: map[string]any{"text": "好"}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 2}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	locked := []llm.ToolDefinition{{Name: "locked_marker", Parameters: map[string]any{"type": "object"}}}
	resp, err := runner.Run(context.Background(), Request{
		Messages: continuationTestMessages("问题"),
		Continuation: &Continuation{
			Prefix:      []llm.Message{{Role: llm.RoleSystem, Content: "锁定的旧头部"}},
			Append:      []llm.Message{{Role: llm.RoleUser, Content: "问题"}},
			Key:         "locked-key",
			Definitions: locked,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Continued || resp.ConversationKey != "locked-key" || len(resp.Definitions) != len(locked) {
		t.Fatalf("a locked head must continue under the locked key: continued=%v key=%q", resp.Continued, resp.ConversationKey)
	}
	request := client.requests[len(client.requests)-1]
	if request.Messages[0].Content != "锁定的旧头部" {
		t.Fatalf("head = %q", request.Messages[0].Content)
	}
	if !reflect.DeepEqual(request.Tools, locked) {
		t.Fatalf("tools must be the locked definitions: %#v", request.Tools)
	}
}

// transcriptPairs 检查前缀里的工具调用和结果一一成对，并按顺序返回发言正文；出现
// send 以外的调用、带供应商原始输出的发言都算错。
func transcriptPairs(t *testing.T, transcript []llm.Message) (spoken []string) {
	t.Helper()
	for index := 0; index < len(transcript); index++ {
		message := transcript[index]
		switch message.Role {
		case llm.RoleTool:
			t.Fatalf("tool result without its call at %d: %#v", index, message)
		case llm.RoleAssistant:
			if len(message.ToolCalls) != 1 || !llm.IsHistoryReplyMessage(message) || message.ToolCalls[0].Name != finalizeToolName ||
				len(message.ResponsesOutput) != 0 || len(message.AnthropicThinking) != 0 || message.ReasoningContent != nil || message.Content != "" {
				t.Fatalf("assistant message at %d is not a bare history reply: %#v", index, message)
			}
			if index+1 >= len(transcript) {
				t.Fatalf("send call at %d has no result", index)
			}
			result := transcript[index+1]
			if result.Role != llm.RoleTool || result.ToolCallID != message.ToolCalls[0].ID || result.Content != historyReplyResult {
				t.Fatalf("send call at %d not paired: %#v", index, result)
			}
			spoken = append(spoken, sendText(message.ToolCalls[0]))
			index++
		}
	}
	return spoken
}

// 一轮对前缀的贡献是最后一次实际发出的整份请求，接上收尾说的话：收尾的几条各成一对，
// 不说话的一轮什么都不加。
func TestTurnTranscriptIsLastRequestPlusFinalSpeech(t *testing.T) {
	request := llm.GenerateRequest{Messages: continuationTestMessages("问题")}
	for _, tc := range []struct {
		name  string
		final string
		want  []string
	}{
		{"one send", "好", []string{"好"}},
		{"two sends", "第一条" + messageBoundaryMarker + "第二条", []string{"第一条", "第二条"}},
		{"silent", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transcript := turnTranscript(request, "trace", tc.final)
			if !reflect.DeepEqual(transcript[:len(request.Messages)], request.Messages) {
				t.Fatalf("the last request must open the transcript: %#v", transcript)
			}
			if got := transcriptPairs(t, transcript[len(request.Messages):]); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if len(transcript) != len(request.Messages)+2*len(tc.want) {
				t.Fatalf("nothing but the spoken pairs may follow the request: %#v", transcript[len(request.Messages):])
			}
		})
	}
	// 同一句话在两轮里各说一遍，调用 ID 不能撞。
	a := turnTranscript(request, "trace-a", "好")
	b := turnTranscript(request, "trace-b", "好")
	if a[len(a)-1].ToolCallID == b[len(b)-1].ToolCallID {
		t.Fatal("the same text in two turns must get different call ids")
	}
}

// 先说一句、查一下、再说一句，下一轮接着续：每一次请求都以上一次请求整份开头（经 sub2api
// 的 gpt 只认「上一次完整请求」这个缓存点），工具调用和结果、中途那句都留在前缀里。
func TestRunnerRequestsArePurePrefixes(t *testing.T) {
	var events []string
	client := &stepsClient{steps: [][]llm.ToolCall{{
		{ID: "send-1", Name: finalizeToolName, Arguments: map[string]any{"text": "我去查一下"}},
		{ID: "search-1", Name: webSearchToolName, Arguments: map[string]any{"query": "天气"}},
	}}}
	search := &orderTool{name: webSearchToolName, events: &events}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 4, Interim: interimRecorder(&events), ToolResultReminder: "工具跑完了，挑要点回复"}, NewToolRegistry(search))
	if err != nil {
		t.Fatal(err)
	}
	first, err := runner.Run(context.Background(), Request{Messages: continuationTestMessages("今天天气怎么样")})
	if err != nil {
		t.Fatal(err)
	}
	if results := client.toolResults(1); len(results) != 2 {
		t.Fatalf("the second step must still see this turn's tool results: %#v", results)
	}
	last := client.requests[len(client.requests)-1].Messages
	if len(first.Transcript) != len(last)+2 {
		t.Fatalf("the transcript must be the last request plus the final pair:\n%#v", first.Transcript)
	}
	if got := transcriptPairs(t, first.Transcript[len(last):]); !reflect.DeepEqual(got, []string{"好了"}) {
		t.Fatalf("final speech = %q", got)
	}
	toolSeen := false
	for _, message := range first.Transcript {
		toolSeen = toolSeen || message.ToolName == webSearchToolName
	}
	if !toolSeen {
		t.Fatal("this turn's tool calls and results must stay in the prefix")
	}
	second, err := runner.Run(context.Background(), Request{
		Messages:     continuationTestMessages("谢谢"),
		Continuation: &Continuation{Prefix: first.Transcript, Append: []llm.Message{{Role: llm.RoleUser, Content: "谢谢"}}, Key: first.ConversationKey},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Continued {
		t.Fatal("matching key must continue")
	}
	for index := 1; index < len(client.requests); index++ {
		before, after := client.requests[index-1].Messages, client.requests[index].Messages
		if len(after) <= len(before) {
			t.Fatalf("request %d does not extend request %d: %d vs %d messages", index, index-1, len(after), len(before))
		}
		for position, message := range before {
			got, want := after[position], message
			got.CacheBreakpoint, want.CacheBreakpoint = false, false
			got.TurnOnly, want.TurnOnly = false, false
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("request %d rewrites request %d at message %d:\n got %#v\nwant %#v", index, index-1, position, got, want)
			}
		}
	}
}

// TailReminder 每一步都接在请求最后、只给那一步看：拿掉它之后各步仍是严格前缀，
// 前缀（Transcript）里一条都没有，下一轮续接也照样每步带一条。
func TestRunnerTailReminderOnlyEndsEachRequest(t *testing.T) {
	const reminder = "【输出前检查】别复述对方原话"
	var events []string
	client := &stepsClient{steps: [][]llm.ToolCall{{
		{ID: "send-1", Name: finalizeToolName, Arguments: map[string]any{"text": "我去查一下"}},
		{ID: "search-1", Name: webSearchToolName, Arguments: map[string]any{"query": "天气"}},
	}}}
	search := &orderTool{name: webSearchToolName, events: &events}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 4, Interim: interimRecorder(&events)}, NewToolRegistry(search))
	if err != nil {
		t.Fatal(err)
	}
	first, err := runner.Run(context.Background(), Request{Messages: continuationTestMessages("今天天气怎么样"), TailReminder: reminder})
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.Run(context.Background(), Request{
		Messages:     continuationTestMessages("谢谢"),
		Continuation: &Continuation{Prefix: first.Transcript, Append: []llm.Message{{Role: llm.RoleUser, Content: "谢谢"}}, Key: first.ConversationKey},
		TailReminder: reminder,
	})
	if err != nil || !second.Continued {
		t.Fatalf("matching key must continue: %v", err)
	}
	if len(client.requests) < 3 {
		t.Fatalf("want several steps over two turns, got %d requests", len(client.requests))
	}
	var stripped [][]llm.Message
	for index, request := range client.requests {
		count := 0
		for _, message := range request.Messages {
			count += strings.Count(message.Content, reminder)
		}
		last := request.Messages[len(request.Messages)-1]
		if count != 1 || last.Content != reminder || last.Role != llm.RoleSystem {
			t.Fatalf("request %d must end with the reminder exactly once: count=%d last=%#v", index, count, last)
		}
		stripped = append(stripped, request.Messages[:len(request.Messages)-1])
	}
	for _, message := range append(append([]llm.Message(nil), first.Transcript...), second.Transcript...) {
		if strings.Contains(message.Content, reminder) {
			t.Fatal("the reminder must not enter the prefix")
		}
	}
	for index := 1; index < len(stripped); index++ {
		before, after := stripped[index-1], stripped[index]
		for position, message := range before {
			got, want := after[position], message
			got.CacheBreakpoint, want.CacheBreakpoint = false, false
			got.TurnOnly, want.TurnOnly = false, false
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("request %d rewrites request %d at message %d", index, index-1, position)
			}
		}
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

// 已加载工具的契约不进前缀：群里又加载了一个工具，续接照旧，请求里也没有契约块。
// 以前契约常驻在历史之前（生产 11-15 平均 7.7 千字），要用的工具现在当轮 tool_search 取。
func TestRunnerContinuationKeepsContractsOutOfPrefix(t *testing.T) {
	client := &silentFinalizeClient{arguments: map[string]any{"text": "好"}}
	runner := contractTestRunner(t, client, "platform", "bot_config")
	first, err := runner.Run(context.Background(), Request{Messages: continuationTestMessages("第一问"), LoadedTools: []string{"platform"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.Run(context.Background(), Request{
		Messages:     continuationTestMessages("第二问"),
		LoadedTools:  []string{"platform", "bot_config"},
		Continuation: &Continuation{Prefix: first.Transcript, Append: []llm.Message{{Role: llm.RoleUser, Content: "第二问"}}, Key: first.ConversationKey},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Continued {
		t.Fatal("loading one more tool must not break the continuation")
	}
	for _, request := range client.requests {
		for _, message := range request.Messages {
			if strings.Contains(message.Content, `"inputSchema"`) {
				t.Fatalf("a loaded contract was sent in the prompt: %.200q", message.Content)
			}
		}
	}
}

// 只用于本轮的内容（跨群参考）发出去了也留在前缀里：前缀是最后一次请求的整份。
func TestTurnTranscriptKeepsTurnOnlyMessages(t *testing.T) {
	request := llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: "head"},
		{Role: llm.RoleUser, Content: "[跨群历史 2026-10-20 10:00:00] 甲: 猫又跑了", TurnOnly: true},
		{Role: llm.RoleUser, Content: "current", CacheBreakpoint: true},
	}}
	transcript := turnTranscript(request, "trace", "")
	if len(transcript) != 3 || transcript[1].TurnOnly || transcript[2].CacheBreakpoint {
		t.Fatalf("every message stays, flags cleared: %#v", transcript)
	}
	if !request.Messages[1].TurnOnly || !request.Messages[2].CacheBreakpoint {
		t.Fatal("the request itself must not be modified")
	}
}
