// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestPromptSenderIdentityIncludesDisplayNameAndUserID(t *testing.T) {
	for _, test := range []struct {
		name  string
		event MessageEvent
		want  string
	}{
		{name: "name and id", event: MessageEvent{SenderName: "Alice", UserID: "12345678"}, want: "Alice（12345678）"},
		{name: "id only", event: MessageEvent{UserID: "12345678"}, want: "12345678"},
		{name: "degraded name", event: MessageEvent{SenderName: "12345678", UserID: "12345678"}, want: "12345678"},
		{name: "name only", event: MessageEvent{SenderName: "Alice"}, want: "Alice"},
		{name: "missing identity", event: MessageEvent{}, want: "用户"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := promptSenderIdentity(test.event); got != test.want {
				t.Fatalf("promptSenderIdentity() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestConversationPromptContextsIncludeSenderUserID(t *testing.T) {
	event := MessageEvent{
		Kind:       EventKindGroup,
		SenderName: "Alice",
		UserID:     "12345678",
		RawMessage: "hello",
		Time:       1,
	}
	quoted := &QuotedMessage{SenderName: "Bob", UserID: "87654321", RawMessage: "earlier"}

	for name, text := range map[string]string{
		"history":      historyPromptTextAt(event, 2),
		"supplement":   proactiveTurnPromptTextAt(event, "hello", 2),
		"current":      currentPromptText(event, "hello"),
		"quoted":       quotedPromptText(quoted),
		"summary line": compactContextEvent(event),
	} {
		want := "Alice（12345678）"
		if name == "quoted" {
			want = "Bob（87654321）"
		}
		if !strings.Contains(text, want) {
			t.Fatalf("%s context = %q, missing %q", name, text, want)
		}
	}
}

func TestPromptSenderUserIDRespectsIdentityPrivacy(t *testing.T) {
	scope := newIdentityPrivacyScope()
	alias := scope.register("12345678", "current_user")
	protected := scope.protectText(currentPromptText(MessageEvent{
		SenderName: "Alice",
		UserID:     "12345678",
	}, "hello"))
	if strings.Contains(protected, "12345678") {
		t.Fatalf("protected current prompt leaked the real user ID: %s", protected)
	}
	if !strings.Contains(protected, "Alice（"+alias+"）") {
		t.Fatalf("protected current prompt lost the sender alias: %s", protected)
	}
}

func TestReplyTurnSenderPrivacyWithoutHistory(t *testing.T) {
	for _, source := range []string{"proactive", "backlog"} {
		for _, masking := range []bool{true, false} {
			name := source + "/unmasked"
			if masking {
				name = source + "/masked"
			}
			t.Run(name, func(t *testing.T) {
				provider := &sequenceLLMProvider{replies: []string{
					`{"action":"none","prompt":"","tools":[],"context_message_ids":[],"keep_older_summary":false}`,
					"答案是 2 和 4。",
				}}
				runtime := NewRuntime(BotConfig{LLMIdentityMaskingEnabled: boolPtr(masking)},
					&recordingChannel{}, NewPluginManager(), nil, nil, nil,
					func() (LLMProvider, error) { return provider, nil })
				current := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "87654321",
					MessageID: "current", SenderName: "Bob", RawMessage: "2+2", Time: 106}
				supplement := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "12345678",
					MessageID: "supplement", SenderName: "Alice", RawMessage: "1+1", Time: 100}
				turn := []proactiveReplyCandidate{{Event: supplement, Text: supplement.RawMessage}}
				// Deliberately keep the supplement out of history, as can happen
				// when the prompt's history budget drops an older turn.
				ctx := context.Background()
				if source == "proactive" {
					ctx = withProactiveReplyTurnContext(ctx, turn)
				} else {
					current.backlogTurn = turn
					ctx = withInboundReplyTurnContext(ctx, current)
				}
				if _, err := runtime.replyTo(ctx, current, current.RawMessage); err != nil {
					t.Fatal(err)
				}
				found := false
				for _, request := range provider.requests {
					for _, message := range request.Messages {
						if !strings.HasPrefix(message.Content, "【当前同轮补充消息") {
							continue
						}
						found = true
						if masking {
							if strings.Contains(message.Content, supplement.UserID) || !strings.Contains(message.Content, "Alice（im_user_") {
								t.Fatalf("supplement sender was not masked: %s", message.Content)
							}
						} else if !strings.Contains(message.Content, "Alice（12345678）") {
							t.Fatalf("masking disabled but sender changed: %s", message.Content)
						}
					}
				}
				if !found {
					t.Fatal("provider did not receive the supplement")
				}
			})
		}
	}
}

func TestPromptSenderOpaqueIDPrivacy(t *testing.T) {
	for _, userID := range []string{"ou_user", "staff-1", "member-1"} {
		t.Run(userID, func(t *testing.T) {
			event := MessageEvent{Kind: EventKindGroup, UserID: userID, SenderName: "Alice", RawMessage: "hello"}
			quoted := &QuotedMessage{UserID: userID, SenderName: "Alice", RawMessage: "hello"}
			for name, content := range map[string]string{
				"current":    currentPromptText(event, "hello"),
				"history":    historyPromptTextAt(event, 0),
				"supplement": proactiveTurnPromptTextAt(event, "hello", 0),
				"quoted":     quotedPromptText(quoted),
				"summary":    compactContextEvent(event),
			} {
				scope := newIdentityPrivacyScope()
				// Summaries outlive their original events; their structured identity
				// fields must discover opaque IDs without an event registration.
				if name != "summary" {
					scope.registerEvent(event)
				}
				protected := scope.protectRequest(llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: content}}})
				text := requestTextForPrivacyTest(protected)
				if strings.Contains(text, userID) || !strings.Contains(text, "Alice（im_user_") {
					t.Fatalf("%s leaked or lost opaque sender identity: %s", name, text)
				}
				if restored := scope.restoreText(protected.Messages[len(protected.Messages)-1].Content); restored != content {
					t.Fatalf("%s identity did not round trip: %s", name, restored)
				}
				for _, longerID := range []string{userID + "_other", "other-" + userID, userID + userID} {
					if got := scope.protectText(longerID); got != longerID {
						t.Fatalf("changed another identifier: %s", got)
					}
				}
			}
		})
	}
}
