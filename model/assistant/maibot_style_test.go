package assistant

import (
	"strings"
	"testing"

	"github.com/SuInk/diana/internal/maibotreply"
)

func TestMaiBotStyleUsesOnePassPromptWithoutTakingParticipation(t *testing.T) {
	cfg := (BotConfig{ReplyComposer: "maibot", SystemPrompt: "# 测试人设\n保留完整身份", AgentEnabled: false}).WithDefaults()
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	head, tail := runtime.systemPromptPartsWithRelationshipAndAgentTools(MessageEvent{Kind: EventKindGroup}, nil, false, RelationshipPolicy{}, false, nil)
	joined := head + "\n" + tail
	for _, required := range []string{"保留完整身份", maibotreply.DefaultReplyStyle, "最好一次对一个话题进行回复", maiBotCleanVocabulary, maiBotNoParentheticalLaugh, maiBotTrailingPeriodPreference} {
		if !strings.Contains(joined, required) {
			t.Fatalf("prompt lost %q", required)
		}
	}
	for _, forbidden := range []string{"控制回复的频率", cfg.prompt(promptChineseSlangSpec)} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("prompt unexpectedly contains %q", forbidden)
		}
	}
}

func TestMaiBotStyleConfigRoundTripAndCustomStyle(t *testing.T) {
	cfg := (BotConfig{ReplyComposer: "maibot", MaiBotReplyStyle: "  像熟人一样轻松说话  "}).WithDefaults()
	if cfg.MaiBotReplyStyle != "像熟人一样轻松说话" {
		t.Fatalf("style=%q", cfg.MaiBotReplyStyle)
	}
	payload := PayloadFromConfig(cfg)
	got := ConfigFromPayload(payload, BotConfig{}).WithDefaults()
	if got.ReplyComposer != "maibot" || got.MaiBotReplyStyle != cfg.MaiBotReplyStyle {
		t.Fatalf("round trip lost MaiBot config: %#v", got)
	}
	if prompt := maiBotStylePrompt(MessageEvent{Kind: EventKindPrivate}, got); !strings.Contains(prompt, cfg.MaiBotReplyStyle) || strings.Contains(prompt, maibotreply.DefaultReplyStyle) {
		t.Fatalf("custom style not applied: %s", prompt)
	}
}
