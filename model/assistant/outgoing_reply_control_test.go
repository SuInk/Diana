package assistant

import (
	"context"
	"strings"
	"testing"
)

func TestOutgoingReplyControlModesAndInvalidTargets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   ReplyDecorationMode
		prefix string
		wantID string
	}{
		{"auto historical target", ReplyDecorationAuto, "[diana-reply:106019]", "106019"},
		{"on ignores historical target", ReplyDecorationOn, "[diana-reply:106019]", "106020"},
		{"off ignores valid target", ReplyDecorationOff, "[diana-reply:106019]", ""},
		{"unknown alias", ReplyDecorationAuto, "[diana-reply:im_message_702fc1baad94]", ""},
		{"missing target", ReplyDecorationAuto, "[diana-reply:999999]", ""},
		{"cross session target", ReplyDecorationAuto, "[diana-reply:106018]", ""},
		{"invalid numeric target", ReplyDecorationAuto, "[diana-reply:106020oops]", ""},
		{"multiple targets", ReplyDecorationAuto, "[diana-reply:106019][diana-reply:106020]", ""},
		{"whitespace", ReplyDecorationAuto, " \n[diana-reply:106019] ", "106019"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := NewRuntime(BotConfig{ReplyReferenceMode: tc.mode}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
			event := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "10001", MessageID: "106020"}
			runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "10001", MessageID: "106019", RawMessage: "old"})
			runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: "group-2", UserID: "10001", MessageID: "106018", RawMessage: "other group"})
			msg := runtime.applyOutgoingReplyMarker(context.Background(), event, OutgoingMessage{
				Text: tc.prefix + "正文保留", ReplyMessageID: event.MessageID,
			})
			if msg.Text != "正文保留" || msg.ReplyMessageID != tc.wantID {
				t.Fatalf("text=%q reply=%q, want %q", msg.Text, msg.ReplyMessageID, tc.wantID)
			}
		})
	}
}

func TestReplyAliasRestorationRequiresExactIdentifier(t *testing.T) {
	scope := newIdentityPrivacyScope()
	alias := scope.registerMessageID("106020")
	for _, text := range []string{alias + "7", alias + "_stale", "im_message_702fc1baad94"} {
		input := replyMarkerPrefix + text + "]正文"
		if got := scope.restoreText(input); got != input {
			t.Fatalf("unknown alias partially restored: %q -> %q", input, got)
		}
	}
	if got := scope.restoreText(replyMarkerPrefix + alias + "]正文"); got != "[diana-reply:106020]正文" {
		t.Fatalf("known alias not restored: %q", got)
	}
	// A late sending path can still resolve a known alias from the same scope.
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "10001", MessageID: "106020"}
	for _, value := range []string{alias, alias + "7"} {
		msg := runtime.applyOutgoingReplyMarker(withIdentityPrivacyScope(context.Background(), scope), event,
			OutgoingMessage{Text: replyMarkerPrefix + value + "]正文"})
		want := ""
		if value == alias {
			want = event.MessageID
		}
		if msg.ReplyMessageID != want || strings.Contains(msg.Text, "im_") || msg.Text != "正文" {
			t.Fatalf("alias=%q result=%#v", value, msg)
		}
	}
}
