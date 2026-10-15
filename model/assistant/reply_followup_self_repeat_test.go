package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDirectSelfRepeatGuardRemainsEnabled(t *testing.T) {
	for _, marked := range []bool{false, true} {
		p := &sequenceLLMProvider{auditReplies: []string{selfRepeatVerdict(true, 0.95, "反复道别")}}
		cfg := BotConfig{BotAccount: "42"}
		if marked {
			cfg.MarkedBotIDs = []string{"user"}
		}
		r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return p, nil })
		event := botReplyLoopEvent(r, "repeat", "user", 0, time.Now().Add(-time.Minute), time.Second, "晚安")
		_, err := r.auditReplyBeforeSend(context.Background(), event, "晚安", "晚安，明天见", r.effectiveConfigForEvent(event), false)
		if !errors.Is(err, errReplySelfRepeatDropped) {
			t.Fatalf("不能绕过直接回复的防复读审核：marked=%v err=%v", marked, err)
		}
	}
}

func TestReplyAuditOnlyIncludesQuoteForLoopCheck(t *testing.T) {
	p := &qualityTestProvider{reply: `{"send_confidence":0.99,"account_safe":0.98}`}
	r := topicTestRuntime(p)
	event := botReplyLoopEvent(r, "quote", "user", 0, time.Now().Add(-time.Minute), time.Second, "鸡爪不应该叫鸡脚")
	for _, loop := range []bool{false, true} {
		if _, err := r.runReplyAudit(context.Background(), event, event.RawMessage, "鸡爪确实是脚", BotConfig{}, botReplyLoopEvidence{}, replyAuditNeed{Loop: loop}); err != nil {
			t.Fatal(err)
		}
		request := p.requests[len(p.requests)-1]
		content := request.Messages[len(request.Messages)-1].Content
		var payload map[string]json.RawMessage
		if err := json.Unmarshal([]byte(strings.TrimPrefix(content, "请审核以下回复：\n")), &payload); err != nil {
			t.Fatal(err)
		}
		if _, exists := payload["original_request_context"]; exists != loop {
			t.Fatalf("quoted context included=%v, loop=%v", exists, loop)
		}
	}
}
