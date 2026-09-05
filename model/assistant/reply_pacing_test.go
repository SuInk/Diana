package assistant

import (
	"strings"
	"testing"
)

func TestProactiveReplyUsesOneBubbleWithoutLosingText(t *testing.T) {
	cfg := BotConfig{DirectReplyChunkSize: 8, ReplyMaxBubbles: 4}.WithDefaults()
	text := "第一句回应。\n第二句补充。<dianabr>第三句。<botbr>第四句。"
	for _, event := range []MessageEvent{
		{Kind: EventKindGroup, proactiveReply: true},
		{Kind: EventKindGroup, chatInReply: true},
	} {
		got := splitEventChatReply(text, cfg, event)
		if len(got) != 1 {
			t.Fatalf("proactive bubbles: %q", got)
		}
		for _, want := range []string{"第一句回应", "第二句补充", "第三句", "第四句"} {
			if !strings.Contains(got[0], want) {
				t.Fatalf("lost %q: %q", want, got)
			}
		}
		if strings.Contains(got[0], "br>") {
			t.Fatalf("marker leaked: %q", got)
		}
	}
}

func TestDirectedReplyKeepsConfiguredSplitting(t *testing.T) {
	cfg := BotConfig{}.WithDefaults()
	for _, event := range []MessageEvent{{Kind: EventKindGroup}, {Kind: EventKindPrivate}} {
		if got := splitEventChatReply("第一句<dianabr>第二句", cfg, event); len(got) != 2 {
			t.Fatalf("direct reply changed: %q", got)
		}
	}
}

func TestProactiveReplyTransportLimitDoesNotTruncate(t *testing.T) {
	text := strings.Repeat("测", notificationChunkSize*2+50)
	got := splitEventChatReply(text, BotConfig{}.WithDefaults(), MessageEvent{Kind: EventKindGroup, proactiveReply: true})
	if len(got) < 2 || strings.Join(got, "") != text {
		t.Fatalf("transport splitting lost content: chunks=%d", len(got))
	}
}

func TestOneBotGroupToolPlatformScope(t *testing.T) {
	for _, platform := range []string{PlatformOneBotV11, PlatformTelegram, PlatformQQOfficial} {
		for _, kind := range []EventKind{EventKindGroup, EventKindPrivate} {
			want := platform == PlatformOneBotV11 && kind == EventKindGroup
			if got := supportsOneBotGroupTool(BotConfig{Platform: platform}, MessageEvent{Kind: kind}); got != want {
				t.Fatalf("platform=%s kind=%s: %v", platform, kind, got)
			}
		}
	}
}
