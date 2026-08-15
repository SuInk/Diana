package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestRecentTextReferenceSurfaceNormalization(t *testing.T) {
	tests := []struct {
		recent   string
		current  string
		wantNorm string
	}{
		{recent: "GLM-5.3", current: "5.3", wantNorm: "glm53"},
		{recent: "glm 5.3", current: "5.3", wantNorm: "glm53"},
		{recent: "GLM5.3", current: "5.3", wantNorm: "glm53"},
		{recent: "Model X v2.1", current: "2.1", wantNorm: "modelxv21"},
		{recent: "Product-A 2026.4", current: "2026.4", wantNorm: "producta20264"},
	}
	for _, test := range tests {
		t.Run(test.recent, func(t *testing.T) {
			keys := recentTextReferenceKeys(test.current)
			if len(keys) != 1 {
				t.Fatalf("keys=%#v", keys)
			}
			candidates := recentTextCandidatesFromSource(test.recent, keys[0], recentTextReferenceCandidate{Method: "same_sender"})
			if len(candidates) != 1 || candidates[0].Normalized != test.wantNorm {
				t.Fatalf("candidates=%#v want normalized %q", candidates, test.wantNorm)
			}
		})
	}
}

func TestRecentTextReferenceResolvesUniqueAssistantReferent(t *testing.T) {
	history := []MessageEvent{
		textReferenceEvent(100, "user-a", "m1", "OpenCode Go currently has which models?"),
		textReferenceEventWithReply(110, "user-a", "m2", "Is GLM 5.3 available?", "可用列表包含 GLM-5.3 / 5.2 / 5.1。"),
	}
	event := textReferenceEvent(130, "user-a", "m3", "What do you think of 5.3?")
	reference := resolveRecentTextReference(event, event.RawMessage, history, "bot")
	if reference == nil || reference.Canonical != "GLM-5.3" || reference.Method != "assistant_reply" || reference.SourceMessageID != "m2" {
		t.Fatalf("reference=%#v", reference)
	}
}

func TestRecentTextReferenceReportsGenuineAmbiguity(t *testing.T) {
	history := []MessageEvent{
		textReferenceEvent(100, "user-a", "m1", "GLM-5.3 的推理能力"),
		textReferenceEvent(110, "user-a", "m2", "App 5.3 的界面变化"),
	}
	event := textReferenceEvent(120, "user-a", "m3", "5.3 怎么样？")
	reference := resolveRecentTextReference(event, event.RawMessage, history, "bot")
	if reference == nil || reference.Method != "ambiguous" || len(reference.Candidates) != 2 {
		t.Fatalf("reference=%#v", reference)
	}
	prompt := recentTextReferencePrompt(reference)
	for _, want := range []string{"GLM-5.3", "App 5.3", "不能猜测"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt=%q missing %q", prompt, want)
		}
	}
}

func TestRecentTextReferenceExplicitQuoteWins(t *testing.T) {
	history := []MessageEvent{textReferenceEvent(110, "user-a", "near", "App 5.3 的界面变化")}
	event := textReferenceEvent(120, "user-a", "current", "5.3 怎么样？")
	event.Quoted = &QuotedMessage{
		MessageID: "older", UserID: "user-a", SenderName: "Alice",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "GLM-5.3 的推理能力"}}},
	}
	reference := resolveRecentTextReference(event, event.RawMessage, history, "bot")
	if reference == nil || reference.Canonical != "GLM-5.3" || reference.Method != "explicit_quote" {
		t.Fatalf("reference=%#v", reference)
	}
}

func TestRecentTextReferenceDoesNotCrossUsersOrInventCandidate(t *testing.T) {
	history := []MessageEvent{textReferenceEvent(100, "user-a", "m1", "GLM-5.3 的推理能力")}
	event := textReferenceEvent(110, "user-b", "m2", "5.3 怎么样？")
	if reference := resolveRecentTextReference(event, event.RawMessage, history, "bot"); reference != nil {
		t.Fatalf("cross-user reference=%#v", reference)
	}
	if reference := resolveRecentTextReference(event, event.RawMessage, nil, "bot"); reference != nil {
		t.Fatalf("invented reference=%#v", reference)
	}
}

func TestRecentTextReferenceIgnoresInterleavedGroupNoise(t *testing.T) {
	history := []MessageEvent{
		textReferenceEventWithReply(100, "user-a", "m1", "GLM 5.3 可用吗？", "GLM-5.3 可以使用。"),
		textReferenceEvent(105, "user-b", "noise-1", "今天吃什么"),
		textReferenceEvent(108, "user-c", "noise-2", "晚点再说"),
	}
	event := textReferenceEvent(120, "user-a", "m2", "5.3 怎么样？")
	reference := resolveRecentTextReference(event, event.RawMessage, history, "bot")
	if reference == nil || reference.Canonical != "GLM-5.3" {
		t.Fatalf("reference=%#v", reference)
	}
}

func TestRecentTextReferenceSurvivesIntoAgentProviderRequest(t *testing.T) {
	provider := &capturingLLMProvider{reply: `{"action":"final","content":"GLM-5.3 的综合能力不错。"}`}
	runtime := NewRuntime(BotConfig{
		AgentEnabled: true, AgentWorkDir: t.TempDir(), AgentMaxSteps: 3,
	}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	for index := 0; index < 24; index++ {
		runtime.remember(textReferenceEvent(int64(10+index), "user-a", "old-"+time.Unix(int64(index), 0).Format("150405"), strings.Repeat("旧上下文 ", 80)))
	}
	runtime.remember(textReferenceEvent(100, "user-a", "m1", "OpenCode Go currently has which models?"))
	runtime.remember(textReferenceEventWithReply(110, "user-a", "m2", "Is GLM 5.3 available?", "列表包含 GLM-5.3 / 5.2 / 5.1。"))
	current := textReferenceEvent(150, "user-a", "current", "What do you think of 5.3?")
	originalText := current.RawMessage
	reply, err := runtime.replyTo(context.Background(), current, current.RawMessage)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "GLM-5.3") {
		t.Fatalf("reply=%q", reply)
	}
	request := provider.requestSnapshot()
	if len(request.Messages) == 0 {
		t.Fatal("provider request is empty")
	}
	currentMessage := request.Messages[len(request.Messages)-1]
	for _, want := range []string{"【运行时已解析的文本指代】", `"canonical":"GLM-5.3"`, `"shorthand":"5.3"`} {
		if !strings.Contains(currentMessage.Content, want) {
			t.Fatalf("current provider message missing %q: %q", want, currentMessage.Content)
		}
	}
	if currentMessage.Priority != llm.MessagePriorityCurrent {
		t.Fatalf("current priority=%d", currentMessage.Priority)
	}
	if current.RawMessage != originalText || PlainText(current.Segments) != originalText {
		t.Fatalf("persistable user text changed: %#v", current)
	}
}

func textReferenceEvent(at int64, userID, messageID, text string) MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: userID, SenderName: userID,
		Time: at, MessageID: messageID, RawMessage: text,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

func textReferenceEventWithReply(at int64, userID, messageID, text, reply string) MessageEvent {
	event := textReferenceEvent(at, userID, messageID, text)
	event.botReply = reply
	return event
}
