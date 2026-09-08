package assistant

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestEmojiSemanticsPreservesInputAndOnlyNamesPresentEmoji(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "人设示例：💊"},
		{Role: llm.RoleAssistant, Content: "药丸💊"},
		{Role: llm.RoleUser, Content: "🫪", Parts: []llm.ContentPart{{Type: llm.ContentPartText, Text: "再来一个🫪"}, {Type: llm.ContentPartImageURL, ImageURL: "https://example.test/💊.png"}}},
	}
	req := llm.GenerateRequest{Messages: messages}
	got := requestWithEmojiSemantics(req)
	if len(got.Messages) != 4 || !reflect.DeepEqual(got.Messages[:3], messages) || len(req.Messages) != 3 {
		t.Fatal("source messages changed")
	}
	note := got.Messages[3].Content
	if !strings.Contains(note, "🫪：变形的脸 / distorted face") || strings.Contains(note, "💊") || strings.Count(note, "distorted face") != 1 {
		t.Fatalf("incorrect note: %s", note)
	}
	if again := requestWithEmojiSemantics(got); !reflect.DeepEqual(again, got) {
		t.Fatal("repeated preparation duplicates or changes annotation")
	}
}

func TestEmojiSemanticsToolResultsAndMultimodalText(t *testing.T) {
	req := llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleTool, Content: `{"text":"💊"}`, ToolCallID: "call-1"},
		{Role: llm.RoleUser, Parts: []llm.ContentPart{{Type: llm.ContentPartText, Text: "👩🏽‍💻"}}},
	}}
	got := requestWithEmojiSemantics(req)
	note := got.Messages[len(got.Messages)-1].Content
	if !strings.Contains(note, "💊：药丸 / pill") || !strings.Contains(note, "woman technologist: medium skin tone") {
		t.Fatalf("missing tool or multipart semantics: %s", note)
	}
	if !reflect.DeepEqual(got.Messages[:2], req.Messages) {
		t.Fatal("tool protocol or multipart input changed")
	}
}

func TestEmojiSemanticsRuntimeProviderIntegration(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	provider := &privacyRequestProvider{reply: "收到"}
	client := runtime.wrapLLMProviderForContext(context.Background(), provider)
	response, err := client.Generate(context.Background(), llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "🫪"}}})
	if err != nil || response.Text != "收到" {
		t.Fatalf("response changed: %+v, %v", response, err)
	}
	if !strings.Contains(provider.request.Messages[len(provider.request.Messages)-1].Content, "distorted face") {
		t.Fatal("runtime did not attach semantics")
	}
	plain := llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "普通文字"}}}
	if got := requestWithEmojiSemantics(plain); !reflect.DeepEqual(got, plain) {
		t.Fatal("plain request changed")
	}
}
