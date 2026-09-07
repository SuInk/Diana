package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestReplyLengthLimitIsPerMessageOnAllPlatforms(t *testing.T) {
	for _, platform := range []string{PlatformOneBotV11, PlatformTelegram} {
		p := &compressionTestProvider{err: errors.New("must not compress")}
		cfg := BotConfig{Platform: platform, MaxReplyChars: 50, MarkdownToPlain: boolPointer(false)}
		original := strings.Repeat("甲", 40) + notificationSplitMarker + strings.Repeat("乙", 40)
		got, err := compressionTestRuntime(p).prepareGeneratedReply(context.Background(), cfg, original)
		if err != nil || got != original || len(p.requests) != 0 {
			t.Fatalf("%s applied a total limit: %q %v", platform, got, err)
		}
	}
}

func TestReplyLengthSplitsNaturallyWithoutCompression(t *testing.T) {
	for _, original := range []string{
		strings.Repeat("甲", 40) + notificationSplitMarker + strings.Repeat("乙", 40),
		"## 第一阶段" + notificationLineMarker + "- " + strings.Repeat("甲", 50) + notificationSplitMarker + "## 第二阶段" + notificationLineMarker + "- " + strings.Repeat("乙", 50),
		"```text" + notificationLineMarker + strings.Repeat("a", 50) + notificationLineMarker + "```" + notificationSplitMarker + "```text" + notificationLineMarker + strings.Repeat("b", 50) + notificationLineMarker + "```",
	} {
		p := &compressionTestProvider{err: errors.New("must not compress")}
		cfg := BotConfig{MaxReplyChars: 90, MarkdownToPlain: boolPointer(false)}
		rt := compressionTestRuntime(p)
		got, err := rt.prepareGeneratedReply(context.Background(), cfg, original)
		if err != nil || len(p.requests) != 0 {
			t.Fatalf("natural grouping failed: %q %v", got, err)
		}
		parts := splitChatReply(got, chatSplitLimits{})
		if len(parts) != 2 || rt.replyLengthIssue(cfg, MessageEvent{}, got) != "" {
			t.Fatalf("bad grouping: %q", parts)
		}
		if compressionCandidateIssue(original, got, 0) != "" {
			t.Fatal("natural grouping changed protected content")
		}
	}
}

func TestReplyLengthCompressesOnlyTheOversizedPart(t *testing.T) {
	p := &compressionTestProvider{outputs: []string{"精简的要点"}}
	original := "保留这条" + notificationSplitMarker + strings.Repeat("长", 100)
	got, err := compressionTestRuntime(p).prepareGeneratedReply(context.Background(), BotConfig{MaxReplyChars: 20}, original)
	if err != nil || len(p.requests) != 1 {
		t.Fatalf("compression failed: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(p.requests[0].Messages[1].Content), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["reply"] != strings.Repeat("长", 100) || payload["max_characters"] != float64(0) || payload["max_characters_per_message"] != float64(20) {
		t.Fatalf("wrong compression scope: %v", payload)
	}
	parts := splitChatReply(got, chatSplitLimits{})
	if len(parts) != 2 || parts[0] != "保留这条" || parts[1] != "精简的要点" {
		t.Fatalf("unchanged message lost: %q", parts)
	}
}

func TestReplyLengthSingleAndMarkerOnlyDoNotInferBoundaries(t *testing.T) {
	for _, single := range []bool{false, true} {
		p := &compressionTestProvider{outputs: []string{"完整的简短答复"}}
		cfg := BotConfig{MaxReplyChars: 20, NaturalReplySplitEnabled: boolPointer(false)}
		original := strings.Repeat("甲", 15) + "。" + strings.Repeat("乙", 15)
		if single {
			original = replySingleMarker + original
		}
		got, err := compressionTestRuntime(p).prepareGeneratedReply(context.Background(), cfg, original)
		if err != nil || len(p.requests) != 1 || len(splitChatReply(got, chatSplitLimits{MarkerOnly: true})) != 1 {
			t.Fatalf("mode ignored: %q %v", got, err)
		}
	}
}

func TestReplyLengthCompressionBudgetIsBoundedAcrossParts(t *testing.T) {
	p := &compressionTestProvider{outputs: []string{"一", "二"}}
	original := strings.Join([]string{strings.Repeat("甲", 30), strings.Repeat("乙", 30), strings.Repeat("丙", 30)}, notificationSplitMarker)
	got, err := compressionTestRuntime(p).prepareGeneratedReply(context.Background(), BotConfig{MaxReplyChars: 10}, original)
	if got != "" || !errors.Is(err, errReplyCompression) || len(p.requests) != 2 {
		t.Fatalf("unbounded or partial compression: %q %v calls=%d", got, err, len(p.requests))
	}
}

func TestReplyLengthDoesNotSilentlyDropAnOversizedPart(t *testing.T) {
	p := &compressionTestProvider{outputs: []string{notificationSplitMarker, notificationSplitMarker}}
	original := "保留这条" + notificationSplitMarker + strings.Repeat("长", 30)
	got, err := compressionTestRuntime(p).prepareGeneratedReply(context.Background(), BotConfig{MaxReplyChars: 10}, original)
	if got != "" || !errors.Is(err, errReplyCompression) || len(p.requests) != 2 {
		t.Fatalf("marker-only compression dropped content: %q %v", got, err)
	}
}

func TestReplyLengthPreservesOneBotForwardPackaging(t *testing.T) {
	p := &compressionTestProvider{err: errors.New("must not compress")}
	channel := &recordingChannel{}
	cfg := BotConfig{Platform: PlatformOneBotV11, BotAccount: "42", MaxReplyChars: 30, ForwardReplyChunkThreshold: 1}.WithDefaults()
	rt := NewRuntime(cfg, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return p, nil })
	parts := []string{strings.Repeat("甲", 20), strings.Repeat("乙", 20)}
	prepared, err := rt.prepareGeneratedReply(context.Background(), cfg, strings.Join(parts, notificationSplitMarker))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.sendDecorated(context.Background(), MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", SelfID: "42"}, prepared, outboundDecoration{}); err != nil {
		t.Fatal(err)
	}
	calls := recordedCallsByAction(channel.callsSnapshot(), "send_group_forward_msg")
	if len(calls) != 1 || len(channel.sentSnapshot()) != 0 || len(p.requests) != 0 {
		t.Fatal("packaging caused compression or changed delivery")
	}
	for _, part := range parts {
		if !forwardCallContainsText(calls[0], part) {
			t.Fatal("forward packaging lost reply content")
		}
	}
}

func TestReplyLengthReadableExample(t *testing.T) {
	for _, platform := range []string{PlatformOneBotV11, PlatformTelegram} {
		p := &compressionTestProvider{err: errors.New("must not compress")}
		channel := &recordingChannel{}
		cfg := BotConfig{Platform: platform, MaxReplyChars: 22, SendChunkIntervalMS: 1}.WithDefaults()
		rt := NewRuntime(cfg, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return p, nil })
		event := MessageEvent{Platform: platform, Kind: EventKindPrivate, UserID: "10001", SelfID: "42"}
		input := "先核对配置是否生效。然后查看服务启动日志。最后确认端口是否被占用。"
		prepared, err := rt.prepareGeneratedReply(context.Background(), cfg, input, event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rt.sendDecorated(context.Background(), event, prepared, outboundDecoration{}); err != nil {
			t.Fatal(err)
		}
		var sent []string
		for _, message := range channel.sentSnapshot() {
			sent = append(sent, message.Text)
			if replyCompressionRunes(message.Text) > 22 {
				t.Fatal("per-message budget exceeded")
			}
		}
		if len(sent) != 2 || len(p.requests) != 0 || p.generationCalls != 0 {
			t.Fatal("readable example unexpectedly used a model or wrong grouping")
		}
		raw, err := json.Marshal(map[string]any{"platform": platform, "input": input, "limit_per_message": 22, "sent": sent, "model_calls": 0})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("DELIVERY_EXAMPLE %s", raw)
	}
}
