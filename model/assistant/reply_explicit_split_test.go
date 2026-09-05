package assistant

import (
	"strings"
	"testing"
)

func TestProactiveRepliesHonorExplicitMarkers(t *testing.T) {
	for _, natural := range []bool{true, false} {
		cfg := BotConfig{NaturalReplySplitEnabled: boolPointer(natural), ReplyMaxBubbles: 1}.WithDefaults()
		for _, casual := range []bool{true, false} {
			event := MessageEvent{Kind: EventKindGroup, proactiveReply: true, chatInReply: casual}
			got := splitEventChatReply("第一句<dianabr>第二句<botbr>第三句", cfg, event)
			if len(got) != 3 || strings.Join(got, "|") != "第一句|第二句|第三句" {
				t.Fatalf("natural=%v casual=%v explicit splits=%q", natural, casual, got)
			}
		}
	}
}

func TestRoutedRequestUsesConfiguredNaturalSplitting(t *testing.T) {
	for _, natural := range []bool{true, false} {
		cfg := BotConfig{NaturalReplySplitEnabled: boolPointer(natural), ReplyMaxBubbles: 4}.WithDefaults()
		got := splitEventChatReply("先给结论\n再补充理由", cfg, MessageEvent{Kind: EventKindGroup, proactiveReply: true})
		want := 1
		if natural {
			want = 2
		}
		if len(got) != want {
			t.Fatalf("natural=%v splits=%q, want %d", natural, got, want)
		}
	}
}

func TestCasualReplyPreservesMarkersInsideCodeFences(t *testing.T) {
	text := "说明<dianabr>\n```txt\nliteral <dianabr>\n```\n<dianabr>结尾"
	got := splitEventChatReply(text, BotConfig{}.WithDefaults(), MessageEvent{Kind: EventKindGroup, chatInReply: true})
	if len(got) != 3 || !strings.Contains(got[1], "literal <dianabr>") {
		t.Fatalf("code fence marker was consumed: %q", got)
	}
}

func TestCasualPacingPromptDoesNotApplyToRoutedRequests(t *testing.T) {
	r := NewRuntime(DefaultBotConfig(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	for _, casual := range []bool{true, false} {
		event := MessageEvent{Kind: EventKindGroup, GroupID: "100", UserID: "200", proactiveReply: true, chatInReply: casual}
		prompt := r.systemPromptWithMode(event, nil, true)
		if strings.Contains(prompt, proactiveReplyPacingPrompt) != casual {
			t.Fatalf("casual=%v pacing prompt applied to wrong route", casual)
		}
		if !strings.Contains(prompt, proactiveReplyToolResultPrompt) {
			t.Fatal("tool-result truthfulness rule was lost")
		}
		if !strings.Contains(prompt, "可以使用 <dianabr>") || strings.Contains(prompt, "不使用分条标记") || strings.Contains(prompt, "最终只发送一条") {
			t.Fatalf("casual=%v prompt still suppresses explicit splitting", casual)
		}
	}
}

func TestLegacySingleMessagePromptMigratesWithoutChangingCustomPrompts(t *testing.T) {
	for _, original := range []string{"", legacySingleMessageProactiveReplyPrompt, "  " + legacySingleMessageProactiveReplyPrompt + "\n"} {
		cfg := (BotConfig{ProactiveReplyPrompt: original}).WithDefaults()
		if cfg.ProactiveReplyPrompt != defaultProactiveReplyPrompt {
			t.Fatal("legacy default was not migrated")
		}
		if got := ConfigFromPayload(PayloadFromConfig(cfg), cfg).WithDefaults(); got.ProactiveReplyPrompt != defaultProactiveReplyPrompt {
			t.Fatal("migrated prompt did not survive config round trip")
		}
	}
	custom := "我的规则：最终只发送一条简洁完整的回复。"
	if got := (BotConfig{ProactiveReplyPrompt: custom}).WithDefaults(); got.ProactiveReplyPrompt != custom {
		t.Fatal("custom prompt was overwritten")
	}
}
