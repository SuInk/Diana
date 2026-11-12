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
	if sent[len(sent)-1].Content != "第二问" {
		t.Fatalf("current message is not last: %#v", sent[len(sent)-1])
	}
	// 发言者工具环境每轮都发、只用于本轮：两轮的请求里各有一份，前缀里一份都没有。
	environments := func(messages []llm.Message) (count int) {
		for _, message := range messages {
			if message.Role == llm.RoleSystem && strings.HasPrefix(message.Content, speakerToolPromptMarker+"\n") {
				count++
				if !message.TurnOnly {
					t.Fatalf("the speaker environment must be turn-only: %#v", message)
				}
			}
		}
		return count
	}
	if environments(client.requests[0].Messages) != 1 || environments(sent) != 1 {
		t.Fatalf("each request must carry exactly one environment block: %d / %d", environments(client.requests[0].Messages), environments(sent))
	}
	for _, transcript := range [][]llm.Message{first.Transcript, second.Transcript} {
		for _, message := range transcript {
			if message.TurnOnly || strings.HasPrefix(message.Content, speakerToolPromptMarker) || strings.HasPrefix(message.Content, RuntimeClockMarker) {
				t.Fatalf("turn-only state leaked into the prefix: %#v", message)
			}
		}
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

// 一轮对前缀的贡献只有第一步请求里不是只用于本轮的消息，加实际发出的每句话：收尾的
// 几条各成一对，不说话的一轮什么都不加。
func TestTurnTranscriptKeepsOnlySpokenText(t *testing.T) {
	request := llm.GenerateRequest{Messages: continuationTestMessages("问题")}
	interim := []Step{
		{Tool: finalizeToolName, Input: map[string]any{"text": "我去查一下"}, Output: "已发送"},
		{Tool: "web_search", Input: map[string]any{"query": "x"}, Output: "结果"},
		{Tool: finalizeToolName, Input: map[string]any{"text": "没发出去的"}, Error: "被拦下"},
	}
	for _, tc := range []struct {
		name  string
		steps []Step
		final string
		want  []string
	}{
		{"one send", nil, "好", []string{"好"}},
		{"two sends", nil, "第一条" + messageBoundaryMarker + "第二条", []string{"第一条", "第二条"}},
		{"silent", nil, "", nil},
		{"interim then final", interim, "查到了", []string{"我去查一下", "查到了"}},
		{"interim then silent", interim, "", []string{"我去查一下"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transcript := turnTranscript(request, "trace", tc.steps, tc.final)
			if !reflect.DeepEqual(transcript[:len(request.Messages)], request.Messages) {
				t.Fatalf("the first request must open the transcript: %#v", transcript)
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
	a := turnTranscript(request, "trace-a", nil, "好")
	b := turnTranscript(request, "trace-b", nil, "好")
	if a[len(a)-1].ToolCallID == b[len(b)-1].ToolCallID {
		t.Fatal("the same text in two turns must get different call ids")
	}
}

// 先说一句、查一下、再说一句：这一轮各步的请求里工具结果照常可见，记下来的前缀里
// 没有任何工具调用和结果，只有按顺序的两句发言；下一轮照着它续接。
func TestRunnerTranscriptDropsToolStepsAndKeepsSpeechOrder(t *testing.T) {
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
	first, err := runner.Run(context.Background(), Request{Messages: continuationTestMessages("今天天气怎么样")})
	if err != nil {
		t.Fatal(err)
	}
	if results := client.toolResults(1); len(results) != 2 {
		t.Fatalf("the second step must still see this turn's tool results: %#v", results)
	}
	var base []llm.Message
	for _, message := range client.requests[0].Messages {
		if !message.TurnOnly {
			message.CacheBreakpoint = false
			base = append(base, message)
		}
	}
	if len(first.Transcript) != len(base)+4 || !reflect.DeepEqual(first.Transcript[:len(base)], base) {
		t.Fatalf("the transcript must be the first request minus turn-only messages plus two spoken pairs:\n%#v", first.Transcript)
	}
	if got := transcriptPairs(t, first.Transcript); !reflect.DeepEqual(got, []string{"我去查一下", "好了"}) {
		t.Fatalf("spoken = %q", got)
	}
	for _, message := range first.Transcript {
		if message.ToolName == webSearchToolName || strings.Contains(message.Content, `{"ok":true}`) {
			t.Fatalf("tool step leaked into the prefix: %#v", message)
		}
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
	sent := client.requests[2].Messages
	for index, message := range first.Transcript {
		got := sent[index]
		got.CacheBreakpoint = false
		if !reflect.DeepEqual(got, message) {
			t.Fatalf("prefix differs at %d:\n got %#v\nwant %#v", index, got, message)
		}
	}
	if got := transcriptPairs(t, second.Transcript); !reflect.DeepEqual(got, []string{"我去查一下", "好了", "好了"}) {
		t.Fatalf("spoken after two turns = %q", got)
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

// 前缀里有、当前发言者却用不了的契约留在原处，补一句说明。说明是当前发言者的状态，
// 只用于本轮：用不了时每轮都发，都能用时不发——前缀里不留上一位发言者的那句。
func TestContractDeltaMarksAvailabilityTurnOnly(t *testing.T) {
	platform := loadedContract{name: "platform", raw: `{"name":"platform","description":"群管","inputSchema":{}}`}
	config := loadedContract{name: "bot_config", raw: `{"name":"bot_config","description":"配置","inputSchema":{}}`}
	prefix := []llm.Message{
		{Role: llm.RoleSystem, Content: "人设"},
		{Role: llm.RoleUser, Content: sharedContractsMessage(renderLoadedContracts([]loadedContract{platform, config})) + "\n\n## Skills"},
	}
	if delta := contractDelta([]loadedContract{platform, config}, prefix, nil); len(delta) != 0 {
		t.Fatalf("nothing changed, nothing to append: %#v", delta)
	}
	// 普通成员：bot_config 用不了，连着两轮都要说。
	for round := 0; round < 2; round++ {
		delta := contractDelta([]loadedContract{platform}, prefix, nil)
		if len(delta) != 1 || delta[0].Role != llm.RoleSystem || !delta[0].TurnOnly || !strings.Contains(delta[0].Content, "用不了：bot_config。") {
			t.Fatalf("round %d: blocked contract notice = %#v", round, delta)
		}
	}
	// 定义变了的契约按新的再发一次，这一条留在前缀里。
	changed := loadedContract{name: "platform", raw: `{"name":"platform","description":"群管（新）","inputSchema":{}}`}
	delta := contractDelta([]loadedContract{changed, config}, prefix, nil)
	if len(delta) != 1 || delta[0].TurnOnly || !strings.Contains(delta[0].Content, "群管（新）") || strings.Contains(delta[0].Content, "bot_config") {
		t.Fatalf("changed contract delta = %#v", delta)
	}
	if got := deliveredContracts(append(prefix[:2:2], delta...)); got["platform"] != changed.raw {
		t.Fatalf("latest delivery must win: %q", got["platform"])
	}
}

// 只用于本轮的内容（跨群参考）随本轮请求发出，但不进下一轮续接的前缀。
func TestTurnTranscriptDropsTurnOnlyMessages(t *testing.T) {
	request := llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: "head"},
		{Role: llm.RoleUser, Content: "[跨群历史 2026-10-20 10:00:00] 甲: 猫又跑了", TurnOnly: true},
		{Role: llm.RoleUser, Content: "current", CacheBreakpoint: true},
	}}
	transcript := turnTranscript(request, "trace", nil, "")
	if len(transcript) != 2 || transcript[0].Content != "head" || transcript[1].Content != "current" || transcript[1].CacheBreakpoint {
		t.Fatalf("turn-only message must be dropped and breakpoints cleared: %#v", transcript)
	}
	if !request.Messages[1].TurnOnly || len(request.Messages) != 3 {
		t.Fatal("the request itself must not be modified")
	}
}
