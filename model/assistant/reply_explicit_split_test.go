package assistant

import (
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestReplyPromptsUseTheActualEventSplitMode(t *testing.T) {
	for _, natural := range []bool{true, false} {
		cfg := BotConfig{NaturalReplySplitEnabled: boolPointer(natural)}.WithDefaults()
		r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
		for _, event := range []MessageEvent{
			{Kind: EventKindGroup},
			{Kind: EventKindGroup, proactiveReply: true},
			{Kind: EventKindGroup, proactiveReply: true, chatInReply: true},
			{Kind: EventKindPrivate},
		} {
			wantsNatural := natural
			mainPrompt := r.systemPromptWithMode(event, nil, event.proactiveReply)
			persona := r.withUserFacingPersona(event, []llm.Message{{Role: llm.RoleUser, Content: "test"}})
			for _, prompt := range []string{mainPrompt, persona[0].Content} {
				if strings.Contains(prompt, replySegmentationRule) != wantsNatural || strings.Contains(prompt, replySegmentationMarkerOnlyRule) == wantsNatural {
					t.Fatalf("natural=%v kind=%s casual=%v prompt disagrees with delivery", natural, event.Kind, event.chatInReply)
				}
			}
		}
	}
}

func TestCasualChatUsesMarkersForIndependentUtterances(t *testing.T) {
	parts := []string{"唔……在呢喵", "这么晚了还没睡呀，怎么啦喵？", "本喵有点困困的，正慢吞吞听着呢喵~"}
	event := MessageEvent{Kind: EventKindGroup, proactiveReply: true, chatInReply: true}
	got := splitEventChatReply(strings.Join(parts, notificationSplitMarker), BotConfig{}.WithDefaults(), event)
	if len(got) != len(parts) || strings.Join(got, "\n") != strings.Join(parts, "\n") {
		t.Fatalf("explicit utterances were joined or altered: %q", got)
	}
	list := "1. 检查连接" + notificationLineMarker + "2. 查看日志"
	got = splitEventChatReply(list, BotConfig{}.WithDefaults(), event)
	if len(got) != 1 || got[0] != "1. 检查连接\n2. 查看日志" {
		t.Fatalf("list layout was flattened or split: %q", got)
	}
}

func TestProactiveRepliesHonorExplicitMarkers(t *testing.T) {
	for _, natural := range []bool{true, false} {
		cfg := BotConfig{NaturalReplySplitEnabled: boolPointer(natural), ReplyMaxBubbles: 1}.WithDefaults()
		for _, casual := range []bool{true, false} {
			event := MessageEvent{Kind: EventKindGroup, proactiveReply: true, chatInReply: casual}
			got := splitEventChatReply("第一句"+notificationSplitMarker+"第二句"+notificationSplitMarker+"第三句", cfg, event)
			if !natural {
				if len(got) != 1 || got[0] != "第一句\n第二句\n第三句" {
					t.Fatalf("disabled multi-message: %q", got)
				}
				continue
			}
			if len(got) != 3 || strings.Join(got, "|") != "第一句|第二句|第三句" {
				t.Fatalf("natural=%v casual=%v explicit splits=%q", natural, casual, got)
			}
		}
	}
}

func TestRoutedRequestRequiresExplicitSplitting(t *testing.T) {
	for _, natural := range []bool{true, false} {
		cfg := BotConfig{NaturalReplySplitEnabled: boolPointer(natural), ReplyMaxBubbles: 4}.WithDefaults()
		got := splitEventChatReply("先给结论\n再补充理由", cfg, MessageEvent{Kind: EventKindGroup, proactiveReply: true})
		if len(got) != 1 || got[0] != "先给结论，再补充理由" {
			t.Fatalf("natural=%v raw newline controlled delivery: %q", natural, got)
		}
	}
}

func TestCasualReplyUsesLineMarkersInsideCodeFences(t *testing.T) {
	text := "说明" + notificationSplitMarker + "```txt" + notificationLineMarker + "literal" + notificationLineMarker + "```" + notificationSplitMarker + "结尾"
	got := splitEventChatReply(text, BotConfig{}.WithDefaults(), MessageEvent{Kind: EventKindGroup, chatInReply: true})
	if len(got) != 3 || got[1] != "```txt\nliteral\n```" {
		t.Fatalf("code layout marker was not restored: %q", got)
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
		if !strings.Contains(prompt, notificationSplitMarker) || !strings.Contains(prompt, notificationLineMarker) || strings.Contains(prompt, "不使用分条标记") || strings.Contains(prompt, "最终只发送一条") {
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
