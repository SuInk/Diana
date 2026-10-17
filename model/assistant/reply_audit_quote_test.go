package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// 「复读自己」判定已删：它按全群最近几条回复比，换个人问同一件事、连续发图都会被吞掉。
// 旧提示词或用户自定义提示词还可能写出 reply_loop_self_repeat=true，这时也必须照常发送。
func TestReplyAuditIgnoresLegacySelfRepeatVerdict(t *testing.T) {
	for _, marked := range []bool{false, true} {
		p := &sequenceLLMProvider{auditReplies: []string{`{"send_confidence":0.9,"account_safe":true,"count_refusal":false,"reply_loop_meaningless":false,"reply_loop_purposeless":false,"reply_loop_self_repeat":true,"reply_loop_confidence":0.95,"reply_loop_reason":"反复道别"}`}}
		cfg := BotConfig{BotAccount: "42"}
		if marked {
			cfg.MarkedBotIDs = []string{"user"}
		}
		r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return p, nil })
		event := botReplyLoopEvent(r, "repeat", "user", 0, time.Now().Add(-time.Minute), time.Second, "晚安")
		if _, err := r.auditReplyBeforeSend(context.Background(), event, "晚安", "晚安，明天见", r.effectiveConfigForEvent(event), false); err != nil {
			t.Fatalf("旧的复读判定不该再拦回复：marked=%v err=%v", marked, err)
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
