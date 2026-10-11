// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

func TestTelegramDraftResolvesUsernameOnEveryUpdate(t *testing.T) {
	api := newFakeTelegramAPI(t, nil)
	event := MessageEvent{Platform: PlatformTelegram, Kind: EventKindPrivate, UserID: "10002", SenderName: "Alice", SenderUsername: "alice_name", Time: 10}
	r := &Runtime{channel: api.channel(), history: map[string][]MessageEvent{}}
	draft := r.telegramReplyDraft(event, BotConfig{})
	if draft == nil {
		t.Fatal("missing private draft")
	}
	draft.ObserveTextDelta(context.Background(), "[diana-at:10002] hello")
	r.history[sessionKey(event)] = []MessageEvent{{Platform: PlatformTelegram, Kind: EventKindPrivate, UserID: "10002", SenderName: "Alice", SenderUsername: "alice_new", Time: 11}}
	draft.lastSent = draft.lastSent.Add(-telegramDraftUpdateInterval)
	draft.ObserveTextDelta(context.Background(), "[diana-at:10002] hello [diana-at:10003]")
	calls := api.callsOf("sendMessageDraft")
	if len(calls) != 2 {
		t.Fatalf("draft calls = %#v", calls)
	}
	for i, want := range []string{"@alice_name hello", "@alice_new hello @10003"} {
		if calls[i].Params["text"] != want {
			t.Fatalf("draft[%d] = %#v, want text %q", i, calls[i].Params, want)
		}
		if calls[i].Params["parse_mode"] != nil {
			t.Fatalf("unexpected parse_mode: %#v", calls[i].Params)
		}
	}
}

func TestTelegramUsernameDeliveryEntities(t *testing.T) {
	for _, method := range []string{"sendMessage", "sendMessageDraft"} {
		t.Run(method, func(t *testing.T) {
			api := newFakeTelegramAPI(t, nil)
			msg := OutgoingMessage{
				UserID: "10002", Text: "🎉 **[diana-at:10002]** [diana-at:10003]",
				MentionNames:     map[string]string{"10002": "Alice", "10003": "小😀_*"},
				MentionUsernames: map[string]string{"10002": "some_user_name"},
			}
			var err error
			if method == "sendMessage" {
				err = api.channel().Send(context.Background(), msg)
			} else {
				err = api.channel().SendTextDraft(context.Background(), msg, 1)
			}
			if err != nil {
				t.Fatal(err)
			}
			calls := api.callsOf(method)
			if len(calls) != 1 {
				t.Fatalf("calls=%#v", calls)
			}
			params := calls[0].Params
			if params["text"] != "🎉 @some_user_name @小😀_*" || params["parse_mode"] != nil {
				t.Fatalf("params=%#v", params)
			}
			entities, ok := params["entities"].([]any)
			if !ok || len(entities) != 3 {
				t.Fatalf("entities=%#v", params["entities"])
			}
			seen := map[string]bool{}
			for _, raw := range entities {
				entity := raw.(map[string]any)
				kind := entity["type"].(string)
				seen[kind] = true
				switch kind {
				case "mention", "bold":
					if entity["offset"] != float64(3) || entity["length"] != float64(15) || entity["user"] != nil {
						t.Fatalf("username entity=%#v", entity)
					}
				case "text_mention":
					user, _ := entity["user"].(map[string]any)
					if entity["offset"] != float64(19) || entity["length"] != float64(6) || user["id"] != float64(10003) {
						t.Fatalf("fallback entity=%#v", entity)
					}
				default:
					t.Fatalf("unexpected entity=%#v", entity)
				}
			}
			if !seen["mention"] || !seen["text_mention"] || !seen["bold"] {
				t.Fatalf("entities=%#v", entities)
			}
		})
	}
}

func TestTelegramMentionUsernameMappingKeepsAuthenticationSeparate(t *testing.T) {
	for _, tc := range []struct {
		name           string
		from           *telegramUser
		senderChat     *telegramChat
		known          bool
		username, auth string
	}{
		{"human", &telegramUser{ID: 10002, Username: "alice_name"}, nil, true, "alice_name", "alice_name"},
		{"bot", &telegramUser{ID: 10002, IsBot: true, Username: "alice_bot"}, nil, true, "alice_bot", ""},
		{"no username", &telegramUser{ID: 10002}, nil, true, "", ""},
		{"anonymous", &telegramUser{ID: 10002, IsBot: true, Username: "proxy_bot"}, &telegramChat{ID: -10001}, false, "", ""},
		{"missing user", nil, nil, false, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := &telegramMessage{MessageID: 10004, Chat: &telegramChat{ID: -10001, Type: "supergroup"}, From: tc.from, SenderChat: tc.senderChat, Text: "hello"}
			event := telegramMessageToEvent(message, "10005", "diana_bot")
			if event.SenderUsername != tc.auth || (event.SenderMentionUsername != nil) != tc.known {
				t.Fatalf("event=%+v", event)
			}
			if tc.known && *event.SenderMentionUsername != tc.username {
				t.Fatalf("display username=%q", *event.SenderMentionUsername)
			}
			quoted := telegramQuotedMessage(message)
			if quoted == nil || (quoted.SenderMentionUsername != nil) != tc.known {
				t.Fatalf("quoted=%+v", quoted)
			}
			if tc.known && *quoted.SenderMentionUsername != tc.username {
				t.Fatalf("quoted username=%q", *quoted.SenderMentionUsername)
			}
			if tc.auth == "" {
				cfg := BotConfig{Platform: PlatformTelegram, OwnerID: "@alice_bot"}
				if got := cfg.OwnerIDForEvent(event); got != cfg.OwnerID {
					t.Fatalf("display metadata changed owner identity: %q", got)
				}
			}
		})
	}
}

func assertTelegramDraftPayloadBounds(t *testing.T, params map[string]any) {
	t.Helper()
	text, ok := params["text"].(string)
	if !ok || !utf8.ValidString(text) || len(utf16.Encode([]rune(text))) > telegramTextLimit {
		t.Fatalf("invalid draft text or UTF16 length: %#v", params)
	}
	boundaries := map[int]bool{0: true}
	units := 0
	for _, r := range text {
		units += len(utf16.Encode([]rune{r}))
		boundaries[units] = true
	}
	entities, _ := params["entities"].([]any)
	for _, raw := range entities {
		entity := raw.(map[string]any)
		offset, length := int(entity["offset"].(float64)), int(entity["length"].(float64))
		if length <= 0 || !boundaries[offset] || !boundaries[offset+length] {
			t.Fatalf("entity splits a rune or exceeds draft: %#v", entity)
		}
	}
}

func TestTelegramDraftTruncatesExpandedUsernamesWithoutDisabling(t *testing.T) {
	api := newFakeTelegramAPI(t, nil)
	username := strings.Repeat("a", 32)
	event := telegramMentionTestEvent("10002", 10, &username)
	event.Kind, event.GroupID = EventKindPrivate, ""
	r := &Runtime{channel: api.channel(), history: map[string][]MessageEvent{}}
	draft := r.telegramReplyDraft(event, BotConfig{})
	source := strings.Repeat("[diana-at:10002]", 125)
	draft.ObserveTextDelta(context.Background(), source)
	if draft.disabled {
		t.Fatal("expanded username draft disabled streaming")
	}
	draft.lastSent = draft.lastSent.Add(-telegramDraftUpdateInterval)
	draft.ObserveTextDelta(context.Background(), source+"next")
	calls := api.callsOf("sendMessageDraft")
	if len(calls) != 2 {
		t.Fatalf("draft updates = %d, want 2", len(calls))
	}
	for _, call := range calls {
		assertTelegramDraftPayloadBounds(t, call.Params)
		if call.Params["text"] != strings.Repeat("@"+username, 124) || len(call.Params["entities"].([]any)) != 124 {
			t.Fatal("draft should retain 124 whole usernames and no partial mention")
		}
	}
	if err := api.channel().Send(context.Background(), OutgoingMessage{UserID: "10002", Text: source, MentionUsernames: map[string]string{"10002": username}}); err == nil {
		t.Fatal("final send must reject rather than truncate an expanded message")
	}
	if len(api.callsOf("sendMessage")) != 0 {
		t.Fatal("overlong final message reached the API")
	}
}

func TestTelegramDraftTruncationPreservesUTF16Entities(t *testing.T) {
	prefix := strings.Repeat("x", telegramTextLimit-6)
	for _, tc := range []struct {
		name, source, want string
		usernames          map[string]string
		kind               string
		length             int
	}{
		{"emoji at boundary", strings.Repeat("😀", 2047) + "x😀end", strings.Repeat("😀", 2047) + "x", nil, "", 0},
		{"format crosses boundary", strings.Repeat("😀", 2047) + "**😀end**", strings.Repeat("😀", 2048), nil, "bold", 2},
		{"username crosses boundary", prefix + "[diana-at:10002]", prefix, map[string]string{"10002": "long_username"}, "", 0},
		{"fallback crosses boundary", prefix + "[diana-at:10002]", prefix, nil, "", 0},
		{"link crosses boundary", prefix + "[abcdefghij](https://example.com)", prefix, nil, "", 0},
		{"mention ends at boundary", prefix + "[diana-at:10002]😀", prefix + "@alice", map[string]string{"10002": "alice"}, "mention", 6},
		{"entities after boundary", strings.Repeat("😀", 2048) + " **later** [diana-at:10002]", strings.Repeat("😀", 2048), nil, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeTelegramAPI(t, nil)
			msg := OutgoingMessage{UserID: "10002", Text: tc.source, MentionNames: map[string]string{"10002": "Long😀Name"}, MentionUsernames: tc.usernames}
			if err := api.channel().SendTextDraft(context.Background(), msg, 1); err != nil {
				t.Fatal(err)
			}
			calls := api.callsOf("sendMessageDraft")
			if len(calls) != 1 {
				t.Fatalf("draft calls = %d", len(calls))
			}
			params := calls[0].Params
			assertTelegramDraftPayloadBounds(t, params)
			if params["text"] != tc.want {
				t.Fatalf("draft text length = %d, want %d", utf16Length(params["text"].(string)), utf16Length(tc.want))
			}
			entities, _ := params["entities"].([]any)
			if tc.kind == "" {
				if len(entities) != 0 {
					t.Fatalf("unexpected entities: %#v", entities)
				}
			} else if len(entities) != 1 || entities[0].(map[string]any)["type"] != tc.kind || entities[0].(map[string]any)["length"] != float64(tc.length) {
				t.Fatalf("truncated entities = %#v", entities)
			}
			if err := api.channel().Send(context.Background(), msg); err == nil || len(api.callsOf("sendMessage")) != 0 {
				t.Fatal("overlong final message must be rejected before the API")
			}
		})
	}
}

func TestTelegramLengthBudgetUsesUsername(t *testing.T) {
	event := MessageEvent{Platform: PlatformTelegram, Kind: EventKindPrivate, UserID: "10002", SenderName: "A", SenderUsername: "long_username", Time: 10}
	r := &Runtime{}
	cfg := BotConfig{Platform: PlatformTelegram}
	part := strings.Repeat("x", telegramTextLimit-5) + "[diana-at:10002]"
	if issue := r.replyPartLimitIssue(cfg, event, part); issue == "" {
		t.Fatal("rendered username must count toward the Telegram limit")
	}
}
