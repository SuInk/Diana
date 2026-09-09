package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestBatchedCandidatesKeepTheirOwnReplyTargets(t *testing.T) {
	provider := &qualityTestProvider{reply: `{"should_reply":false,"category":"none","reason":"test"}`}
	r := NewRuntime(BotConfig{BotAccount: "bot", OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	first := MessageEvent{Kind: EventKindGroup, SelfID: "bot", GroupID: "g", UserID: "owner", MessageID: "first", Quoted: &QuotedMessage{MessageID: "self-message", UserID: "bot"}}
	second := first
	second.MessageID = "second"
	second.Quoted = &QuotedMessage{MessageID: "other-message", UserID: "another"}
	r.routeProactiveReplyBatch(context.Background(), []proactiveReplyCandidate{{Event: first, Text: "first"}, {Event: second, Text: "second"}})
	if len(provider.requests) != 1 {
		t.Fatalf("requests=%d", len(provider.requests))
	}
	text := provider.requests[0].Messages[len(provider.requests[0].Messages)-1].Content
	var payload proactiveReplyPayload
	if err := json.Unmarshal([]byte(text[strings.Index(text, "{"):]), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Candidates) != 2 || payload.Candidates[0].Addressing.ReplyTarget != "self" || payload.Candidates[1].Addressing.ReplyTarget != "other" {
		t.Fatalf("batch relationships=%+v", payload.Candidates)
	}
}

func TestMessageAddressingDistinguishesReplyTargets(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event MessageEvent
		want  string
	}{
		{"none", MessageEvent{}, "none"},
		{"unresolved", MessageEvent{Segments: []MessageSegment{{Type: "reply", Data: map[string]string{"id": "m"}}}}, "unknown"},
		{"unknown_author", MessageEvent{Quoted: &QuotedMessage{MessageID: "m"}}, "unknown"},
		{"other", MessageEvent{Quoted: &QuotedMessage{MessageID: "m", UserID: "other"}}, "other"},
		{"self", MessageEvent{Quoted: &QuotedMessage{MessageID: "m", UserID: "bot"}}, "self"},
		{"self_id_authoritative", MessageEvent{SelfID: "actual", Quoted: &QuotedMessage{MessageID: "m", UserID: "actual"}}, "self"},
		{"semantic_not_reply", MessageEvent{Quoted: &QuotedMessage{MessageID: "m", UserID: "bot", Semantic: true}}, "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := addressingForEvent(tc.event, BotConfig{BotAccount: "bot"}); got.ReplyTarget != tc.want {
				t.Fatalf("got=%+v want=%s", got, tc.want)
			}
		})
	}
}

func TestTelegramOtherBotMentionAndReplyRemainExplicit(t *testing.T) {
	text := "😀 @kosamerobot 你说呢"
	event := telegramMessageToEvent(&telegramMessage{MessageID: 108162, Chat: &telegramChat{ID: -100, Type: "supergroup"}, From: &telegramUser{ID: 10001}, Text: text,
		Entities: []telegramEntity{{Type: "mention", Offset: 3, Length: 12}},
		ReplyTo:  &telegramMessage{MessageID: 108158, From: &telegramUser{ID: 10002, FirstName: "other"}, Text: "hello"},
	}, "99999", "dianabot")
	if event.ToMe {
		t.Fatal("mentioning another username marked ToMe")
	}
	// The relationship survives the same JSON persistence as message history.
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var restored MessageEvent
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	got := addressingForEvent(restored, BotConfig{})
	if got.ReplyTarget != "other" || !got.MentionsOther || got.MentionsSelf || len(got.Mentions) != 1 || got.Mentions[0].Username != "kosamerobot" {
		t.Fatalf("addressing=%+v", got)
	}
	unknown := telegramMentionTargets(text, []telegramEntity{{Type: "mention", Offset: 3, Length: 12}}, "99999", "")
	if unknown[0].Target != "unknown" {
		t.Fatal("missing bot username was guessed")
	}
	self := telegramMentionTargets("@DIANABOT", []telegramEntity{{Type: "mention", Offset: 0, Length: 9}}, "99999", "dianabot")
	if self[0].Target != "self" {
		t.Fatal("case-insensitive self mention not recognized")
	}
}

func TestAddressingReachesRoutingAndFinalPromptWithoutLeakingIDs(t *testing.T) {
	cfg := BotConfig{BotAccount: "99999"}
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, SelfID: "99999", UserID: "10001", GroupID: "20000", MessageID: "30000", Quoted: &QuotedMessage{MessageID: "30001", UserID: "10002", SenderName: "other"}, MentionTargets: []MessageMention{{UserID: "10003", Target: "other"}}}
	payload := r.proactiveReplyPayload(event, "reply")
	raw, _ := json.Marshal(payload)
	if !strings.Contains(string(raw), `"quoted_is_bot":false`) || !strings.Contains(string(raw), `"reply_target":"other"`) {
		t.Fatalf("routing lost explicit false: %s", raw)
	}
	prompt := r.systemPromptWithRelationshipAndAgentTools(event, nil, false, RelationshipPolicy{}, false, nil)
	if !strings.Contains(prompt, `"reply_target":"other"`) || !strings.Contains(prompt, messageAddressingRule) {
		t.Fatal("main reply missed recipient context")
	}
	scope := newIdentityPrivacyScope()
	scope.registerEvent(event)
	protected := scope.protectRequest(llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: string(raw)}}})
	for _, id := range []string{"10002", "10003", "30001"} {
		if strings.Contains(protected.Messages[0].Content, id) {
			t.Fatalf("unmasked addressing ID %s", id)
		}
	}
}
