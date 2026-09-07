package assistant

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestReplyDeliveryModeOnlyConsumesLeadingMetadata(t *testing.T) {
	for _, tc := range []struct {
		input, want string
		mode        replyDeliveryMode
	}{
		{replySingleMarker + "正文", "正文", replyDeliverySingle},
		{replyAutoMarker + "正文", "正文", replyDeliveryAuto},
		{replySingleMarker + replyAutoMarker + "正文", "正文", replyDeliverySingle},
		{replyAutoMarker + replySingleMarker + "正文", "正文", replyDeliverySingle},
		{"解释 " + replySingleMarker, "解释 " + replySingleMarker, ""},
		{"```text\n" + replySingleMarker + "\n```", "```text\n" + replySingleMarker + "\n```", ""},
		{"> " + replySingleMarker, "> " + replySingleMarker, ""},
	} {
		text, mode := consumeReplyDeliveryMode(tc.input)
		if text != tc.want || mode != tc.mode {
			t.Fatalf("input=%q: text=%q mode=%q", tc.input, text, mode)
		}
	}
}

func TestSingleReplyPreservesLayoutAndCodeButIgnoresBoundaries(t *testing.T) {
	body := "## 第一天" + notificationLineMarker + "### 上午" + notificationLineMarker + "- 安排" + notificationLineMarker + "  - 补充" + notificationLineMarker + notificationLineMarker + "## 第二天" + notificationLineMarker + "安排" + notificationSplitMarker + "补充" + notificationLineMarker + "```text" + notificationLineMarker + "literal" + notificationLineMarker + notificationLineMarker + "  indentation" + notificationLineMarker + "```"
	want := restoreExplicitReplyLines(strings.ReplaceAll(body, notificationSplitMarker, notificationLineMarker))
	for name, split := range map[string]func(string, chatSplitLimits) []string{"chat": splitChatReply, "forward": splitForwardReply} {
		got := split(replySingleMarker+body, chatSplitLimits{})
		if !reflect.DeepEqual(got, []string{want}) {
			t.Fatalf("%s changed single-message content: %q", name, got)
		}
	}
}

func TestDeliveryModeSurvivesNormalizationAndRefusalMetadata(t *testing.T) {
	for _, plain := range []bool{false, true} {
		normalized := normalizeReplyPreservingControlIntent(replySingleMarker+"**内容**"+replyRefusalMarker, 0, plain)
		body, intent := consumeReplyControlIntent(normalized)
		if intent.DeliveryMode != replyDeliverySingle || !intent.RefuseCurrent || strings.Contains(body, replySingleMarker) {
			t.Fatalf("normalization lost metadata: body=%q intent=%+v", body, intent)
		}
	}
}

func TestSingleDeliveryOverridesDefaultsWithoutPersisting(t *testing.T) {
	withFastSendTiming(t)
	for _, natural := range []bool{true, false} {
		for _, kind := range []EventKind{EventKindGroup, EventKindPrivate} {
			cfg := BotConfig{BotAccount: "42", NaturalReplySplitEnabled: boolPointer(natural), ForwardReplyThreshold: 1, ForwardReplyChunkThreshold: 1}.WithDefaults()
			channel := &recordingChannel{}
			rt := NewRuntime(cfg, channel, NewPluginManager(), nil, nil, nil, nil)
			event := MessageEvent{Kind: kind, GroupID: "123456", UserID: "10001", SelfID: "42"}
			body := "## 第一部分" + notificationLineMarker + "正文" + notificationLineMarker + "## 第二部分" + notificationLineMarker + "正文" + notificationSplitMarker + "结尾"
			if _, err := rt.sendDecorated(context.Background(), event, replySingleMarker+body, outboundDecoration{}); err != nil {
				t.Fatal(err)
			}
			sent := channel.sentSnapshot()
			if len(sent) != 1 || len(channel.callsSnapshot()) != 0 || strings.Contains(sent[0].Text, replySingleMarker) || strings.Contains(sent[0].Text, notificationSplitMarker) {
				t.Fatalf("kind=%s natural=%v: sent=%v calls=%v", kind, natural, sent, channel.callsSnapshot())
			}
			if event.replyDeliveryMode != "" || boolValue(rt.effectiveConfigForEvent(event).NaturalReplySplitEnabled, true) != natural {
				t.Fatal("per-turn choice mutated event or configuration")
			}
			if got := splitEventChatReply("第一句\n第二句", cfg, event); len(got) != 1 {
				t.Fatal("single-message choice leaked to the next reply")
			}
			if got := splitEventChatReply(replyAutoMarker+"第一句"+notificationSplitMarker+"第二句", cfg, event); len(got) != 2 {
				t.Fatal("explicit auto choice did not override the default")
			}
		}
	}
}

type deliveryModeLLMProvider struct {
	capturingLLMProvider
	finalized bool
}

func (p *deliveryModeLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	response, err := p.capturingLLMProvider.Generate(ctx, req)
	if err != nil || response.Text != p.reply {
		return response, err
	}
	for _, tool := range req.Tools {
		if tool.Name == "agent.finalize" {
			p.finalized = true
			response.ToolCalls = []llm.ToolCall{{ID: "choice", Name: tool.Name, Arguments: map[string]any{"content": response.Text}}}
			response.Text = ""
		}
	}
	return response, nil
}

func TestReplyToCarriesSingleModeWithoutLeakingIntoHistory(t *testing.T) {
	withFastSendTiming(t)
	testReplyToSingleMode(t, true)
}

func TestNonAgentReplyRetainsUserDeliveryChoice(t *testing.T) {
	provider := &capturingLLMProvider{reply: replySingleMarker + "第一段" + notificationLineMarker + "第二段"}
	channel := &recordingChannel{}
	rt := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001"}
	reply, err := rt.generateReply(context.Background(), BotConfig{AgentEnabled: false}, event, RelationshipPolicy{}, []llm.Message{{Role: llm.RoleUser, Content: "一次发完"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, mode := consumeReplyDeliveryMode(reply); mode != replyDeliverySingle {
		t.Fatal("non-Agent generation lost the delivery choice")
	}
	if len(provider.requestSnapshot().Tools) != 0 {
		t.Fatal("non-Agent probe unexpectedly used tools")
	}
	if _, err := rt.sendDecorated(context.Background(), event, reply, outboundDecoration{}); err != nil || len(channel.sentSnapshot()) != 1 {
		t.Fatalf("non-Agent single delivery failed: %v", err)
	}
}

type deliveryDraftChannel struct{ messages []OutgoingMessage }

func (c *deliveryDraftChannel) SendTextDraft(_ context.Context, message OutgoingMessage, _ int64) error {
	c.messages = append(c.messages, message)
	return nil
}

func TestDeliveryChoiceDoesNotLeakInStreamingDrafts(t *testing.T) {
	for _, marker := range []string{replySingleMarker, replyAutoMarker} {
		channel := &deliveryDraftChannel{}
		draft := &telegramReplyDraft{channel: channel}
		for i := 1; i <= len(marker); i++ {
			draft.ObserveTextDelta(context.Background(), marker[:i])
		}
		if len(channel.messages) != 0 {
			t.Fatalf("partial control prefix leaked: %+v", channel.messages)
		}
		draft.ObserveTextDelta(context.Background(), marker+"\n正文")
		if len(channel.messages) != 1 || channel.messages[0].Text != "正文" {
			t.Fatalf("draft did not strip the control prefix: %+v", channel.messages)
		}
	}
}

func testReplyToSingleMode(t *testing.T, agentEnabled bool) {
	t.Helper()
	channel := &recordingChannel{}
	provider := &deliveryModeLLMProvider{capturingLLMProvider: capturingLLMProvider{reply: replySingleMarker + "## 第一部分" + notificationLineMarker + "内容" + notificationSplitMarker + "## 第二部分" + notificationLineMarker + "内容"}}
	rt := NewRuntime(BotConfig{AgentEnabled: agentEnabled, ForwardReplyThreshold: 1}.WithDefaults(), channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	input := "请放在一条消息里发完，不要分条"
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", MessageID: "single-choice", RawMessage: input}
	reply, err := rt.replyTo(context.Background(), event, input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reply, replySingleMarker) || strings.Contains(reply, notificationSplitMarker) || len(channel.sentSnapshot()) != 1 || len(channel.callsSnapshot()) != 0 {
		t.Fatalf("reply=%q sent=%v calls=%v", reply, channel.sentSnapshot(), channel.callsSnapshot())
	}
	if got := channel.sentSnapshot()[0].Text; got != reply {
		t.Fatalf("returned reply differs from sent text: %q vs %q", reply, got)
	}
	if provider.finalized != agentEnabled {
		t.Fatalf("finalize=%v, agentEnabled=%v", provider.finalized, agentEnabled)
	}
	history := rt.contextHistory(event)
	if len(history) == 0 {
		t.Fatal("outgoing history is empty")
	}
	for _, item := range history {
		if strings.Contains(item.RawMessage+item.botReply, replySingleMarker) {
			t.Fatal("delivery metadata leaked into history")
		}
	}
	raw, err := json.Marshal(event)
	if err != nil || strings.Contains(string(raw), "replyDeliveryMode") {
		t.Fatal("per-turn delivery metadata was persisted")
	}
}

func TestSingleModeStillHonorsExplicitPlatformChunkLimit(t *testing.T) {
	got := splitChatReply(replySingleMarker+strings.Repeat("字", 30), chatSplitLimits{ChunkSize: 10})
	if len(got) < 2 || strings.Join(got, "") != strings.Repeat("字", 30) {
		t.Fatalf("platform fallback lost content: %q", got)
	}
}
