package assistant

import (
	"context"
	"strings"
	"testing"
)

func TestDeepsleepFutureJokeUsesSendConfidence(t *testing.T) {
	original := "我从来没有说过 deepsleep 的嘲讽，我决定把天赋带到深度求索。"
	reply := "等晚高峰排队的时候，你肯定还是第一个喊 deepsleep 的～"
	provider := &qualityTestProvider{reply: `{"send_confidence":0.97,"reason":"过去自述与未来调侃不构成事实矛盾","account_safe":true}`}
	r := NewRuntime(BotConfig{ProactiveReplyThreshold: 0.9}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	if err := r.judgeProactiveReplyQuality(context.Background(), MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u"}, original, reply, r.ProfileConfig("")); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatal("audit must run once")
	}
	prompt := provider.requests[0].Messages[0].Content
	// send_confidence 不再参与发送决定，精简后的提示词不再要它，旧回答照样能解析。
	if !strings.Contains(prompt, `{"account_safe":0.98}`) {
		t.Fatal("missing audit contract")
	}
	if strings.Contains(prompt, "should_send") || strings.Contains(prompt, "send_confidence") {
		t.Fatal("retired fields still advertised")
	}
	decision, ok := parseProactiveReplyQualityDecision(`{"send_confidence":1,"account_safe":false,"account_risk":"explicit"}`)
	if !ok || accountSafetyError(decision) == nil {
		t.Fatal("send score overrode independent safety verdict")
	}
}

func TestReplyAuditPromptOmitsAccuracyAudit(t *testing.T) {
	for _, unwanted := range []string{"是否答非所问", "accuracy_issue", "明确矛盾", "被截断"} {
		if strings.Contains(proactiveReplyQualityPrompt, unwanted) {
			t.Fatalf("accuracy audit still present: %q", unwanted)
		}
	}
	if !strings.Contains(proactiveReplyQualityPrompt, "准确与否、答没答对题都不归你管") {
		t.Fatal("prompt does not document removed accuracy audit")
	}
}
