package assistant

import (
	"context"
	"strings"
	"testing"
)

func TestSendConfidenceRequiresUnambiguousScore(t *testing.T) {
	for _, raw := range []string{
		`{}`, `{"send_confidence":null}`, `{"send_confidence":"0.97"}`,
		`{"send_confidence":-0.1}`, `{"send_confidence":1.1}`,
		`{"should_send":false,"confidence":0.97}`, `{"confidence":0.97}`,
	} {
		if _, ok := parseProactiveReplyQualityDecision(raw); ok {
			t.Fatalf("ambiguous/invalid score accepted: %s", raw)
		}
	}
	for _, tc := range []struct {
		raw  string
		send bool
	}{
		{`{"send_confidence":0}`, false}, {`{"send_confidence":0.8999}`, false},
		{`{"send_confidence":0.9}`, true}, {`{"send_confidence":1}`, true},
	} {
		decision, ok := parseProactiveReplyQualityDecision(tc.raw)
		if !ok {
			t.Fatal(tc.raw)
		}
		if got := (&Runtime{}).proactiveQualityError(MessageEvent{}, decision, BotConfig{ProactiveReplyThreshold: 0.9}) == nil; got != tc.send {
			t.Fatalf("%s send=%v", tc.raw, got)
		}
	}
}

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

// 线上 09-15：「库、SDK 必须选版本号」这种只是说得偏绝对的回复，send_confidence 给 0.84，
// 被 0.90 的阈值拦掉；同一类问题别的时候给 0.91 又放行。现在发送与否只看类别。
func TestAccuracyIssueCategoryDecidesSending(t *testing.T) {
	cfg := BotConfig{ProactiveReplyThreshold: 0.9}
	for _, tc := range []struct {
		raw  string
		send bool
	}{
		{`{"send_confidence":0.84,"accuracy_issue":"wording","reason":"表述过于绝对"}`, true},
		{`{"send_confidence":0.2,"accuracy_issue":"none"}`, true},
		{`{"send_confidence":0.95,"accuracy_issue":"contradiction","reason":"白 K6 说成黑 K6"}`, false},
		{`{"send_confidence":0.95,"accuracy_issue":"off_topic"}`, false},
		{`{"send_confidence":0.95,"accuracy_issue":"truncated"}`, false},
		{`{"send_confidence":0.95,"accuracy_issue":"harmful_advice"}`, false},
		// 缺字段或写歪了：退回旧的阈值判断。
		{`{"send_confidence":0.84}`, false},
		{`{"send_confidence":0.84,"accuracy_issue":"minor"}`, false},
		{`{"send_confidence":0.95,"accuracy_issue":"minor"}`, true},
	} {
		decision, ok := parseProactiveReplyQualityDecision(tc.raw)
		if !ok {
			t.Fatal(tc.raw)
		}
		err := (&Runtime{}).proactiveQualityError(MessageEvent{}, decision, cfg)
		if got := err == nil; got != tc.send {
			t.Fatalf("%s send=%v err=%v", tc.raw, got, err)
		}
	}
	decision, _ := parseProactiveReplyQualityDecision(`{"send_confidence":0.9,"accuracy_issue":"contradiction","reason":"提醒状态前后矛盾"}`)
	err := (&Runtime{}).proactiveQualityError(MessageEvent{}, decision, cfg)
	if err == nil || !strings.Contains(err.Error(), "前后矛盾") || !strings.Contains(err.Error(), "提醒状态前后矛盾") {
		t.Fatalf("rejection should name the category and reason: %v", err)
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
