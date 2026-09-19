// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
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
