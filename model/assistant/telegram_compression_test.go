package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestTelegramOversizedReplyCompressesBeforeSend(t *testing.T) {
	for _, single := range []bool{false, true} {
		p := &compressionTestProvider{outputs: []string{"压缩后的完整要点"}}
		api := newFakeTelegramAPI(t, nil)
		cfg := BotConfig{Platform: PlatformTelegram, MaxReplyChars: 8000}.WithDefaults()
		rt := NewRuntime(cfg, api.channel(), NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return p, nil })
		original := strings.Repeat("说明", 2500)
		if single {
			original = replySingleMarker + original
		}
		prepared, err := rt.prepareGeneratedReply(context.Background(), cfg, original)
		if err != nil || len(p.requests) != 1 {
			t.Fatalf("single=%v: %v calls=%d", single, err, len(p.requests))
		}
		if single && !strings.HasPrefix(prepared, replySingleMarker) {
			t.Fatal("single choice was lost")
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(p.requests[0].Messages[1].Content), &payload); err != nil {
			t.Fatal(err)
		}
		if payload["platform_max_utf16_units"] != float64(telegramTextLimit) || payload["single_message"] != single {
			t.Fatal("compression model did not receive platform constraints")
		}
		if _, err := rt.sendDecorated(context.Background(), MessageEvent{Platform: PlatformTelegram, Kind: EventKindPrivate, UserID: "10001"}, prepared, outboundDecoration{}); err != nil {
			t.Fatal(err)
		}
		calls := api.callsOf("sendMessage")
		if len(calls) != 1 || calls[0].Params["text"] != "压缩后的完整要点" {
			t.Fatalf("wrong final message: %v", calls)
		}
	}
}

func TestTelegramCapacityUsesRenderedUTF16AfterCompression(t *testing.T) {
	p := &compressionTestProvider{outputs: []string{strings.Repeat("\U0001f600", 2100), "简短结论"}}
	cfg := BotConfig{Platform: PlatformTelegram, MaxReplyChars: 8000}
	got, err := compressionTestRuntime(p).prepareGeneratedReply(context.Background(), cfg, replySingleMarker+strings.Repeat("\U0001f600", 2200))
	if err != nil || got != replySingleMarker+"简短结论" || len(p.requests) != 2 {
		t.Fatalf("invalid platform length accepted: %q %v", got, err)
	}
}

func TestTelegramSafePartsDoNotTriggerCompression(t *testing.T) {
	p := &compressionTestProvider{err: errors.New("must not call")}
	cfg := BotConfig{Platform: PlatformTelegram, MaxReplyChars: 3500}
	original := strings.Repeat("甲", 3000) + notificationSplitMarker + strings.Repeat("乙", 3000)
	got, err := compressionTestRuntime(p).prepareGeneratedReply(context.Background(), cfg, original)
	if err != nil || got != original || len(p.requests) != 0 {
		t.Fatal("safe existing segments caused an extra compression call")
	}
}

func TestTelegramCompressionCanRegroupWholeCodeBlocks(t *testing.T) {
	first := "```text\n" + strings.Repeat("a", 2500) + "\n```"
	second := "```text\n" + strings.Repeat("b", 2500) + "\n```"
	p := &compressionTestProvider{outputs: []string{first + "\n" + notificationSplitMarker + "\n" + second}}
	cfg := BotConfig{Platform: PlatformTelegram, MaxReplyChars: 8000}
	got, err := compressionTestRuntime(p).prepareGeneratedReply(context.Background(), cfg, first+"\n"+second)
	if err != nil || len(splitChatReply(got, chatSplitLimits{})) != 2 || len(p.requests) != 0 {
		t.Fatalf("code blocks could not be regrouped safely: %v", err)
	}
	p = &compressionTestProvider{}
	_, err = compressionTestRuntime(p).prepareGeneratedReply(context.Background(), cfg, "```text\n"+strings.Repeat("\U0001f600", 2100)+"\n```")
	if !errors.Is(err, errReplyCompression) || len(p.requests) != 0 {
		t.Fatal("indivisible oversized code should not be rewritten")
	}
}

func TestTelegramPromptIncludesPlatformCapacity(t *testing.T) {
	messages := withReplyGenerationBudget([]llm.Message{{Role: llm.RoleUser, Content: "问题"}}, 8000, PlatformTelegram)
	if !strings.Contains(messages[0].Content, "8000") || !strings.Contains(messages[0].Content, "4096") || !strings.Contains(messages[0].Content, "单条") {
		t.Fatal("generation prompt lacks the total or platform budget")
	}
}
