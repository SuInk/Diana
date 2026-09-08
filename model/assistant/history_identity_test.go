// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestHistoryIdentityUsesAccountsAcrossNicknamesAndLegacyEvents(t *testing.T) {
	cfg := BotConfig{Platform: PlatformOneBotV11, OwnerID: "100001", BotAccount: "200002"}
	for _, tt := range []struct {
		name, userID, platform, want string
	}{
		{"Winter", "100001", PlatformOneBotV11, "bot_owner"},
		{"old owner card", "100001", PlatformOneBotV11, "bot_owner"},
		{"Diana", "200002", PlatformOneBotV11, "bot"},
		{"old bot card", "200002", PlatformOneBotV11, "bot"},
		{"Winter", "300003", PlatformOneBotV11, "user"},
		{"Diana", "300003", PlatformOneBotV11, "user"},
		{"Winter", "100001", PlatformTelegram, "user"},
		{"Diana", "", PlatformOneBotV11, ""},
	} {
		t.Run(tt.name+tt.userID+tt.platform, func(t *testing.T) {
			event := MessageEvent{UserID: tt.userID, SenderName: tt.name, Platform: tt.platform, SelfID: "200002", RawMessage: "hello"}
			// Historical self messages may have no outbound flag.
			item := chatHistoryItem(event, cfg)
			if item.SenderUserID != tt.userID || item.SenderRole != tt.want || item.Sender != tt.name {
				t.Fatalf("identity = %+v", item)
			}
		})
	}
}

func TestHistoryIdentityToolContextAndQuotesSurvivePrivacy(t *testing.T) {
	cfg := BotConfig{Platform: PlatformOneBotV11, OwnerID: "100001", BotAccount: "200002"}
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	current := MessageEvent{Platform: PlatformOneBotV11, Kind: EventKindGroup, UserID: "300003", SelfID: "200002", GroupID: "400004"}
	tool := &dianaChatHistoryTool{runtime: r, event: current}
	oldOwner := MessageEvent{UserID: "100001", SenderName: "old owner card", GroupID: "500005", MessageID: "700007", Time: 1, RawMessage: "earlier"}
	oldBot := MessageEvent{UserID: "200002", SenderName: "old bot card", GroupID: "500005", MessageID: "800008", Time: 2, RawMessage: "reply", Quoted: &QuotedMessage{UserID: "100001", SenderName: "Winter", RawMessage: "question"}}
	items := tool.items(context.Background(), []MessageEvent{oldBot})
	items[0].ContextBefore = tool.items(context.Background(), []MessageEvent{oldOwner})
	if items[0].SenderRole != "bot" || items[0].QuotedSenderRole != "bot_owner" || items[0].ContextBefore[0].SenderRole != "bot_owner" {
		t.Fatalf("roles missing from nested history: %+v", items)
	}
	body, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	scope := newIdentityPrivacyScope()
	ownerAlias := scope.register(cfg.OwnerID, "bot_owner")
	botAlias := scope.register(cfg.BotAccount, "bot")
	protected := scope.protectRequest(llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleTool, Content: string(body)}}})
	text := protected.Messages[len(protected.Messages)-1].Content
	for _, realID := range []string{"100001", "200002", "500005", "700007", "800008"} {
		if strings.Contains(text, realID) {
			t.Fatalf("raw ID leaked: %s", text)
		}
	}
	var decoded []dianaChatHistoryItem
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded[0].SenderUserID != botAlias || decoded[0].QuotedSenderUserID != ownerAlias || decoded[0].ContextBefore[0].SenderUserID != ownerAlias {
		t.Fatalf("aliases not consistent across quotes and context: %s", text)
	}
}

func TestHistoryIdentityAutomaticCrossGroupAndMediaPrompts(t *testing.T) {
	cfg := BotConfig{OwnerID: "100001", BotAccount: "200002"}
	event := MessageEvent{UserID: "100001", SenderName: "old owner card", GroupID: "500005", Time: 1, RawMessage: "context", crossGroupContext: true}
	cross, ok := crossGroupTextContext(event)
	if !ok {
		t.Fatal("missing cross-group text")
	}
	text := historyPromptTextAt(cross, 2, cfg)
	if !strings.HasPrefix(text, "[跨群历史 ") || !strings.Contains(text, `"sender_role":"bot_owner"`) || strings.Contains(text, "500005") {
		t.Fatalf("wrong cross-group identity or source-group leak: %s", text)
	}
	event.Segments = []MessageSegment{{Type: "image", Data: map[string]string{"file": "image.jpg"}}}
	media := agentImageHistoryPromptTextWithDescriptions(event, 2, []string{"图片摘要"}, cfg)
	if !strings.Contains(media, `"sender_role":"bot_owner"`) || !strings.Contains(media, `"sender_user_id":"100001"`) {
		t.Fatalf("media identity missing: %s", media)
	}
	scope := newIdentityPrivacyScope()
	alias := scope.register(cfg.OwnerID, "bot_owner")
	protected := scope.protectText(text)
	if strings.Contains(protected, cfg.OwnerID) || !strings.Contains(protected, alias) {
		t.Fatalf("automatic history not masked: %s", protected)
	}
	quote := quotedPromptText(&QuotedMessage{UserID: cfg.OwnerID, SenderName: "another card", RawMessage: "quoted text"})
	if got := scope.protectText(quote); !strings.Contains(got, alias) || strings.Contains(got, cfg.OwnerID) {
		t.Fatalf("quote identity not masked: %s", got)
	}
}

func TestHistoryIdentityRuntimeKeepsCrossGroupSelfNickname(t *testing.T) {
	now := time.Now().Unix()
	legacy := crossGroupTestEvent(now-60, "500005", "200002", "700007", "历史身份记录")
	legacy.SenderName = "old bot card"
	legacy.SelfID = "200002"
	store := &crossGroupHistoryStore{candidates: []MessageEvent{legacy}}
	channel := &crossGroupMembershipChannel{allowed: map[string]bool{"400004|200002": true}}
	provider := &privacyAwareTestProvider{generate: func(_ int, _ llm.GenerateRequest) (string, error) { return "已确认", nil }}
	r := NewRuntime(BotConfig{OwnerID: "100001", BotAccount: "200002", CrossGroupMemoryEnabled: boolPointer(true)}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	r.SetMessageHistoryStore(store)
	event := crossGroupTestEvent(now, "400004", "300003", "800008", "历史身份记录是谁")
	event.SelfID = "200002"
	if _, err := r.replyTo(context.Background(), event, PlainText(event.Segments)); err != nil {
		t.Fatal(err)
	}
	for _, request := range provider.requests {
		for _, message := range request.Messages {
			if strings.Contains(message.Content, "old bot card") && strings.Contains(message.Content, `"sender_role":"bot"`) && strings.Contains(message.Content, "im_bot_") {
				return
			}
		}
	}
	for _, request := range provider.requests {
		for _, message := range request.Messages {
			if message.Role != llm.RoleSystem {
				t.Logf("%s: %s", message.Role, message.Content)
			}
		}
	}
	t.Fatalf("runtime lost nickname or self identity: requests=%d searches=%d", len(provider.requests), store.searchCalls)
}
