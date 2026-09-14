package assistant

import (
	"context"
	"errors"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// streamRejectionRegistryAdapter 能流也能非流。流式时按 events 吐出，非流式时返回 response。
type streamRejectionRegistryAdapter struct {
	streamCalls   int
	generateCalls int
	events        []llm.ChatEvent
	response      string
}

func (a *streamRejectionRegistryAdapter) Generate(context.Context, llm.ModelDefinition, llm.ChatRequest) (llm.ChatResponse, error) {
	a.generateCalls++
	return llm.ChatResponse{Text: a.response}, nil
}

func (a *streamRejectionRegistryAdapter) Stream(context.Context, llm.ModelDefinition, llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	a.streamCalls++
	ch := make(chan llm.ChatEvent, len(a.events))
	for _, event := range a.events {
		ch <- event
	}
	close(ch)
	return ch, nil
}

func streamRejectionProvider(t *testing.T, adapters ...*streamRejectionRegistryAdapter) *registryFailoverLLMProvider {
	t.Helper()
	registry := llm.NewProviderRegistry()
	profiles := make([]llm.Profile, 0, len(adapters))
	for index, adapter := range adapters {
		id := []string{"primary", "backup", "third"}[index]
		if err := registry.RegisterProvider(llm.ProviderDefinition{ID: id, Name: id, Protocol: llm.ProtocolOpenAIResponses, Enabled: true}, adapter); err != nil {
			t.Fatal(err)
		}
		if err := registry.RegisterModel(llm.ModelDefinition{ID: id + ":" + id + "-model", ProviderID: id, ModelID: id + "-model"}); err != nil {
			t.Fatal(err)
		}
		profiles = append(profiles, llm.Profile{ID: id, Group: "chat", Config: llm.ProviderConfig{Model: id + "-model"}})
	}
	provider, err := newRegistryFailoverLLMProvider(registry, profiles, true, false)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func rejectionNoticeStream() []llm.ChatEvent {
	return []llm.ChatEvent{{Type: llm.ChatEventTextDelta, Text: llm.UnverifiedRejectionNotice}, {Type: llm.ChatEventDone}}
}

// 线上现象：Gemini 对整段 prompt 做关键词过滤，流正常打开（HTTP 200），正文却是那段
// 固定的拦截文案。后备切换只在打开流时生效，于是配了后备也不切，直接报「上游拦截」。
// 非流式路径遇到同一段文案是会切的，流式必须和它一致。
func TestStreamedRejectionFailsOverToBackup(t *testing.T) {
	primary := &streamRejectionRegistryAdapter{events: rejectionNoticeStream()}
	backup := &streamRejectionRegistryAdapter{response: "备用回复"}
	provider := streamRejectionProvider(t, primary, backup)

	response, err := (&streamingLLMProvider{provider: provider}).Generate(context.Background(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "省着用，兼职应该没什么好兼职，不如学习"}},
	})
	if err != nil {
		t.Fatalf("流式拿到拦截正文后应当切到后备，而不是报错：%v", err)
	}
	if response == nil || response.Text != "备用回复" {
		t.Fatalf("response = %#v", response)
	}
	if backup.generateCalls != 1 {
		t.Fatalf("后备应当被调用一次，实际 %d", backup.generateCalls)
	}
	// 刚被拦的候选不能从头再打一遍：拦截是按关键词判的，换成非流式照样被拦，
	// 只会白白多等一次、多花一次额度。
	if primary.streamCalls != 1 || primary.generateCalls != 0 {
		t.Fatalf("主模型 stream=%d generate=%d，期望 1/0", primary.streamCalls, primary.generateCalls)
	}
}

// 只有一个候选时没有别的地方可切，照旧把拦截原样报出去。
func TestStreamedRejectionWithoutBackupStillReportsRejection(t *testing.T) {
	primary := &streamRejectionRegistryAdapter{events: rejectionNoticeStream()}
	provider := streamRejectionProvider(t, primary)

	response, err := (&streamingLLMProvider{provider: provider}).Generate(context.Background(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "你好"}},
	})
	if !errors.Is(err, llm.ErrUnverifiedRejection) || response != nil {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if primary.generateCalls != 0 {
		t.Fatalf("没有后备时不该再非流式打一遍：generate=%d", primary.generateCalls)
	}
}

// Gemini 结构化拦截码是有意设计成不换模型重发的，流里拿到它也不能切后备。
func TestStreamedContentBlockStillStopsWithoutFailover(t *testing.T) {
	blocked := &llm.ContentBlockedError{Provider: llm.ProviderGemini, Stage: "prompt", Reason: "BLOCKLIST", Message: "blocked"}
	primary := &streamRejectionRegistryAdapter{events: []llm.ChatEvent{{Type: llm.ChatEventError, Error: blocked.Error(), ErrorCause: blocked}}}
	backup := &streamRejectionRegistryAdapter{response: "不应该用到"}
	provider := streamRejectionProvider(t, primary, backup)

	response, err := (&streamingLLMProvider{provider: provider}).Generate(context.Background(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "你好"}},
	})
	if !errors.Is(err, llm.ErrContentBlocked) || response != nil {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if backup.generateCalls != 0 || backup.streamCalls != 0 {
		t.Fatalf("结构化拦截不该切后备：backup generate=%d stream=%d", backup.generateCalls, backup.streamCalls)
	}
}
