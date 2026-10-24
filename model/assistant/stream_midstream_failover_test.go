package assistant

import (
	"context"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// Gemini SDK 边读边发请求，限流 503 作为流里第一个事件出来。
func midStreamLimitedEvents() []llm.ChatEvent {
	return []llm.ChatEvent{{Type: llm.ChatEventError, Error: "Error 503, Message: Token error: All accounts limited. Wait 1483s., Status: 503 Service Unavailable"}}
}

func streamedReplyEvents(text string) []llm.ChatEvent {
	return []llm.ChatEvent{{Type: llm.ChatEventTextDelta, Text: text}, {Type: llm.ChatEventDone}}
}

// 线上现象：antigravity 读流时报 503，流式包装层退回非流式、从头重跑后备链——
// 限流的主模型被再打一遍，后备也只能走非流式，而后备网关对非流式的支持恰好有缺陷。
// 读流时才到的可切后备错误应当直接在下一个候选上继续流式。
func TestMidStreamFailoverStreamsFromBackup(t *testing.T) {
	primary := &streamRejectionRegistryAdapter{events: midStreamLimitedEvents()}
	backup := &streamRejectionRegistryAdapter{events: streamedReplyEvents("备用回复")}
	provider := streamRejectionProvider(t, primary, backup)

	response, err := (&streamingLLMProvider{provider: provider}).Generate(context.Background(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "你好"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response == nil || response.Text != "备用回复" {
		t.Fatalf("response = %#v", response)
	}
	if primary.streamCalls != 1 || primary.generateCalls != 0 {
		t.Fatalf("主模型 stream=%d generate=%d，期望 1/0：限流的候选不该再非流式打一遍", primary.streamCalls, primary.generateCalls)
	}
	if backup.streamCalls != 1 || backup.generateCalls != 0 {
		t.Fatalf("后备 stream=%d generate=%d，期望 1/0：后备应当继续走流式", backup.streamCalls, backup.generateCalls)
	}
}

// 所有候选限流时各尝试一次，不能重新跑非流式链。
func TestMidStreamFailoverIsBoundedByCandidates(t *testing.T) {
	primary := &streamRejectionRegistryAdapter{events: midStreamLimitedEvents(), response: "非流式兜底"}
	backup := &streamRejectionRegistryAdapter{events: midStreamLimitedEvents(), response: "非流式兜底"}
	provider := streamRejectionProvider(t, primary, backup)

	response, err := (&streamingLLMProvider{provider: provider}).Generate(context.Background(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "你好"}},
	})
	if err == nil || response != nil || primary.generateCalls != 0 || backup.generateCalls != 0 {
		t.Fatalf("response = %#v", response)
	}
	if primary.streamCalls != 1 || backup.streamCalls != 1 {
		t.Fatalf("stream 次数 primary=%d backup=%d，期望各 1", primary.streamCalls, backup.streamCalls)
	}
}

// 未向 observer 提交文本时，读流中断可以有限重试后切换候选。
func TestMidStreamUncommittedFailureRetriesThenSwitches(t *testing.T) {
	primary := &streamRejectionRegistryAdapter{
		events:   []llm.ChatEvent{{Type: llm.ChatEventTextDelta, Text: "半"}},
		response: "主模型非流式",
	}
	backup := &streamRejectionRegistryAdapter{events: streamedReplyEvents("不应该用到")}
	provider := streamRejectionProvider(t, primary, backup)

	response, err := (&streamingLLMProvider{provider: provider}).Generate(context.Background(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "你好"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response == nil || response.Text != "不应该用到" {
		t.Fatalf("response = %#v", response)
	}
	if primary.streamCalls != 3 || primary.generateCalls != 0 || backup.streamCalls != 1 || backup.generateCalls != 0 {
		t.Fatalf("主模型原地重试两次后才切后备：primary stream=%d backup stream=%d generate=%d", primary.streamCalls, backup.streamCalls, backup.generateCalls)
	}
}
