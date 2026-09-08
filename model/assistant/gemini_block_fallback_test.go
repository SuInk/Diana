package assistant

import (
	"context"
	"errors"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestGeminiBlockCodeFallsBackWithoutSameModelRetry(t *testing.T) {
	blocked := &llm.ContentBlockedError{Provider: llm.ProviderGemini, Stage: "prompt", Reason: "BLOCKLIST", Message: "任意语言，不依赖这段文字"}
	for _, stream := range []bool{false, true} {
		registry := llm.NewProviderRegistry()
		primary := &retryRegistryAdapter{err: blocked}
		backup := &retryRegistryAdapter{succeedAt: 1, response: "备用回复"}
		primaryStream := &streamFailoverRegistryAdapter{streamErr: blocked}
		backupStream := &streamFailoverRegistryAdapter{events: []llm.ChatEvent{{Type: llm.ChatEventTextDelta, Text: "备用回复"}, {Type: llm.ChatEventDone}}}
		var first, last llm.LLMAdapter = primary, backup
		if stream {
			first, last = primaryStream, backupStream
		}
		for _, item := range []struct {
			id      string
			adapter llm.LLMAdapter
		}{{"primary", first}, {"backup", last}} {
			if err := registry.RegisterProvider(llm.ProviderDefinition{ID: item.id, Name: item.id, Protocol: llm.ProtocolOpenAIResponses, Enabled: true}, item.adapter); err != nil {
				t.Fatal(err)
			}
			if err := registry.RegisterModel(llm.ModelDefinition{ID: item.id + ":" + item.id + "-model", ProviderID: item.id, ModelID: item.id + "-model"}); err != nil {
				t.Fatal(err)
			}
		}
		provider, err := newRegistryFailoverLLMProvider(registry, []llm.Profile{{ID: "primary", Group: "chat", Config: llm.ProviderConfig{Model: "primary-model"}}, {ID: "backup", Group: "chat", Config: llm.ProviderConfig{Model: "backup-model"}}}, true, false)
		if err != nil {
			t.Fatal(err)
		}
		var response *llm.GenerateResponse
		request := llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "你好"}}}
		if stream {
			response, err = (&streamingLLMProvider{provider: provider}).Generate(context.Background(), request)
		} else {
			response, err = provider.Generate(context.Background(), request)
		}
		if err != nil || response == nil || response.Text != "备用回复" {
			t.Fatalf("stream=%t response=%+v err=%v", stream, response, err)
		}
		if stream {
			if primaryStream.streamCalls != 1 || backupStream.streamCalls != 1 {
				t.Fatal("stream retries wrong")
			}
		} else if primary.calls != 1 || backup.calls != 1 {
			t.Fatal("generate retries wrong")
		}
	}
	events := make(chan llm.ChatEvent, 1)
	events <- llm.ChatEvent{Type: llm.ChatEventError, Error: blocked.Error(), ErrorCause: blocked}
	close(events)
	_, err := accumulateChatEvents(context.Background(), events)
	if !errors.Is(err, llm.ErrContentBlocked) {
		t.Fatal("stream lost typed block code")
	}
}
