package assistant

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestEmojiSemanticsInlinePreservesRequestStructure(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "人设示例：💊"},
		{Role: llm.RoleAssistant, Content: "药丸💊"},
		{Role: llm.RoleUser, Content: "🫪", Priority: llm.MessagePriorityCurrent, ContextGroup: "current", AtomicText: true,
			Parts: []llm.ContentPart{{Type: llm.ContentPartText, Text: "再来一个🫪"}, {Type: llm.ContentPartImageURL, ImageURL: "https://example.test/💊.png"}}},
	}
	got := requestWithEmojiSemantics(llm.GenerateRequest{Messages: messages})
	if len(got.Messages) != 3 || !reflect.DeepEqual(got.Messages[:2], messages[:2]) {
		t.Fatal("roles or message ordering changed")
	}
	if messages[2].Content != "🫪" || messages[2].Parts[0].Text != "再来一个🫪" {
		t.Fatal("source request mutated")
	}
	if got.Messages[2].Content != "🫪（表情名称：变形的脸 / distorted face）" || !strings.Contains(got.Messages[2].Parts[0].Text, "distorted face") {
		t.Fatalf("missing inline annotation: %+v", got.Messages[2])
	}
	if got.Messages[2].Priority != llm.MessagePriorityCurrent || got.Messages[2].ContextGroup != "current" || !got.Messages[2].AtomicText || got.Messages[2].Parts[1] != messages[2].Parts[1] {
		t.Fatal("context or image metadata changed")
	}
	if again := requestWithEmojiSemantics(got); !reflect.DeepEqual(again, got) {
		t.Fatal("repeated preparation changed annotations")
	}
}

func TestEmojiSemanticsToolResultsAndSequences(t *testing.T) {
	req := llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleTool, Content: `{"text":"💊","url":"https://example.test/💊.png"}`, ToolCallID: "call-1"},
		{Role: llm.RoleUser, Parts: []llm.ContentPart{{Type: llm.ContentPartText, Text: "👩🏽‍💻 👩"}}},
		{Role: llm.RoleSystem, Content: "旧释义", ContextGroup: emojiSemanticsGroup},
	}}
	got := requestWithEmojiSemantics(req)
	if len(got.Messages) != 2 || got.Messages[0].ToolCallID != "call-1" || !json.Valid([]byte(got.Messages[0].Content)) {
		t.Fatal("tool protocol or stale note cleanup failed")
	}
	if !strings.Contains(got.Messages[0].Content, "💊（表情名称：药丸 / pill）") || !strings.Contains(got.Messages[0].Content, "https://example.test/💊.png") {
		t.Fatal("tool text or URL changed incorrectly")
	}
	text := got.Messages[1].Parts[0].Text
	if !strings.Contains(text, "👩🏽‍💻（表情名称：") || !strings.Contains(text, "woman technologist: medium skin tone") {
		t.Fatal("ZWJ sequence split")
	}
	if again := requestWithEmojiSemantics(got); !reflect.DeepEqual(again, got) {
		t.Fatal("sequence annotations not idempotent")
	}
}

func TestEmojiSemanticsRuntimeProviderIntegration(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	provider := &privacyRequestProvider{reply: "收到"}
	client := runtime.wrapLLMProviderForContext(context.Background(), provider)
	response, err := client.Generate(context.Background(), llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "🍅"}}})
	if err != nil || response.Text != "收到" {
		t.Fatalf("response changed: %+v %v", response, err)
	}
	last := provider.request.Messages[len(provider.request.Messages)-1]
	if last.Role != llm.RoleUser || !strings.Contains(last.Content, "🍅（表情名称：西红柿 / tomato）") {
		t.Fatalf("runtime did not annotate inline: %+v", provider.request.Messages)
	}
	plain := llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "普通文字"}}}
	if got := requestWithEmojiSemantics(plain); !reflect.DeepEqual(got, plain) {
		t.Fatal("plain request changed")
	}
}
