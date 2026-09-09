package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestExplicitBotQuoteRequiresVerifiedIdentity(t *testing.T) {
	cfg := BotConfig{BotAccount: "42"}
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", SelfID: "42", Quoted: &QuotedMessage{MessageID: "old", UserID: "42", GroupID: "g"}}
	if !explicitlyRepliesToBot(event, cfg) {
		t.Fatal("bot quote not recognized")
	}
	event.Quoted.Semantic = true
	if explicitlyRepliesToBot(event, cfg) {
		t.Fatal("inferred reference treated as explicit reply")
	}
	event.Quoted.Semantic = false
	event.SelfID = "43"
	if explicitlyRepliesToBot(event, cfg) {
		t.Fatal("stale configured bot ID overrode actual channel identity")
	}
	event.SelfID = "42"
	event.Quoted.UserID = "user"
	event.Quoted.SenderName = "Diana"
	if explicitlyRepliesToBot(event, cfg) {
		t.Fatal("quoted display name treated as bot identity")
	}
	event.Quoted.UserID = "42"
	event.Quoted.GroupID = "other"
	if explicitlyRepliesToBot(event, cfg) {
		t.Fatal("cross-group material treated as a direct reply")
	}
}

func TestExplicitQuoteUsesDirectAuditPolicy(t *testing.T) {
	provider := &qualityTestProvider{reply: `{"should_send":false,"confidence":0.94,"reason":"需要更正年份","account_safe":true}`}
	r := NewRuntime(BotConfig{BotAccount: "42", ReplyAccountSafetyAuditEnabled: boolPointer(true), BotReplyLoopDetectionEnabled: boolPointer(false)}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", SelfID: "42", UserID: "u", Quoted: &QuotedMessage{MessageID: "old", UserID: "42"}, proactiveReply: true}
	if _, err := r.evaluateProactiveReplyQuality(context.Background(), event, "你上网查一下", "答复", r.Config()); err != nil {
		t.Fatalf("explicit reply silently rejected as proactive: %v", err)
	}
	event.Quoted.UserID = "other"
	if _, err := r.evaluateProactiveReplyQuality(context.Background(), event, "闲聊", "答复", r.Config()); err == nil {
		t.Fatal("ordinary chat lost quality gate")
	}
	event.Quoted.UserID = "42"
	provider.reply = `{"should_send":true,"confidence":0.99,"account_safe":false,"account_risk":"explicit","account_risk_reason":"命中账号安全规则"}`
	if _, err := r.evaluateProactiveReplyQuality(context.Background(), event, "请求", "答复", r.Config()); err == nil {
		t.Fatal("explicit quote bypassed configured account safety")
	}
}

type quotedReplyProvider struct{}

func (quotedReplyProvider) Generate(ctx context.Context, _ llm.GenerateRequest) (*llm.GenerateResponse, error) {
	text := "这里是核对后的回答"
	if llmUsagePurposeFromContext(ctx) == "reply_send_audit" {
		text = `{"should_send":false,"confidence":0.94,"reason":"主动回复准确度不足","account_safe":true}`
	}
	return &llm.GenerateResponse{Text: text}, nil
}

func TestExplicitQuotedRequestProducesDirectReplyOutcome(t *testing.T) {
	channel := &recordingChannel{}
	r := NewRuntime(BotConfig{BotAccount: "42", AgentEnabled: false, ReplyAccountSafetyAuditEnabled: boolPointer(true), BotReplyLoopDetectionEnabled: boolPointer(false)}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return quotedReplyProvider{}, nil })
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", SelfID: "42", UserID: "u", MessageID: "new", RawMessage: "你上网查一下", Quoted: &QuotedMessage{MessageID: "old", UserID: "42", RawMessage: "之前的答复"}, proactiveReply: true, chatInReply: true}
	outcome, err := r.replyAndRecord(context.Background(), event, event.RawMessage, "replied_proactive")
	if err != nil || outcome != "replied_direct_followup" {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	if sent := channel.sentSnapshot(); len(sent) != 1 || !strings.Contains(sent[0].Text, "核对后的回答") {
		t.Fatalf("direct reply lost: %+v", sent)
	}
}
