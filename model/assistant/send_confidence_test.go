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
	if err := r.judgeProactiveReplyQuality(context.Background(), MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u"}, original, reply, r.Config()); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatal("audit must run once")
	}
	prompt := provider.requests[0].Messages[0].Content
	for _, expected := range []string{"send_confidence", "过去的自述与未来的假设或调侃", "唯一含义", "发送决定由运行时按阈值执行"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("missing audit contract: %s", expected)
		}
	}
	if strings.Contains(prompt, "should_send") {
		t.Fatal("old boolean protocol still advertised")
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

func TestReplyAuditPromptDefinesAccuracyCategories(t *testing.T) {
	for _, want := range []string{"accuracy_issue", "wording", "不拦截", "contradiction", "off_topic", "truncated", "harmful_advice", `"accuracy_issue":"none"`} {
		if !strings.Contains(proactiveReplyQualityPrompt, want) {
			t.Fatalf("audit prompt missing %q", want)
		}
	}
}
