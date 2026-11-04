// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestAngleMentionCleanup(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{`<@10002>你好 <@!10002>`, `<diana-at id="10002"/>你好 <diana-at id="10002"/>`},
		{`<@unknown>你好`, `你好`},
		{"`<@10002>` <b>文字</b> x < y", "`<@10002>` <b>文字</b> x < y"},
		{"```\n<@unknown>\n```", "```\n<@unknown>\n```"},
	} {
		got := normalizeAngleMentions(tc.input, func(id string) bool { return id == "10002" })
		if got != tc.want {
			t.Fatalf("input=%q got=%q want=%q", tc.input, got, tc.want)
		}
	}
}

func TestQQOfficialAngleMentionsInBodyAndQuote(t *testing.T) {
	const id = "BB9A810ABD29D63652E202B7FBF60AE5"
	data := json.RawMessage(`{"id":"m1","content":"<@` + id + `> 你在机厅吗","group_openid":"g1","author":{"member_openid":"other"}}`)
	event, ok := qqOfficialEventFromDispatch("GROUP_MESSAGE_CREATE", data, "bot")
	if !ok || event.ToMe || strings.Contains(event.RawMessage, "<@") {
		t.Fatalf("event=%+v", event)
	}
	ids := mentionedUserIDs(event.Segments)
	if len(ids) != 1 || ids[0] != id || !strings.Contains(event.RawMessage, "你在机厅吗") {
		t.Fatalf("event=%+v ids=%v", event, ids)
	}
	text, segments := qqOfficialQuotedPayload([]qqOfficialMsgElement{{Content: "<@!" + id + "> 我吗"}})
	if strings.Contains(text, "<@") || len(mentionedUserIDs(segments)) != 1 || !strings.Contains(text, "我吗") {
		t.Fatalf("quote=%q segments=%v", text, segments)
	}
}

func TestRuntimeAngleMentionsUseSessionParticipants(t *testing.T) {
	r := &Runtime{}
	event := MessageEvent{Platform: PlatformTelegram, Kind: EventKindGroup, UserID: "10002"}
	msg := r.normalizeOutgoingMentions(event, OutgoingMessage{Text: `<@10002>你好<@99999><b>普通标签</b>`})
	if msg.Text != `<diana-at id="10002"/>你好<b>普通标签</b>` {
		t.Fatalf("message=%q", msg.Text)
	}
}

// Production's model emitted a valid marker directly adjacent to Chinese prose.
// Verify the wire payload, not the synthetic at segment saved in local history.
func TestTelegramMentionAdjacentProseWireTarget(t *testing.T) {
	for _, suffix := range []string{"你 Plus 快到期了吗", "hello", "_suffix", "😀"} {
		for _, method := range []string{"sendMessage", "sendMessageDraft"} {
			api := newFakeTelegramAPI(t, nil)
			msg := OutgoingMessage{UserID: "10001", Text: "🎉<diana-at id=\"10002\"/>" + suffix, MentionUsernames: map[string]string{"10002": "some_user"}}
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
				t.Fatalf("calls=%v", calls)
			}
			entities, _ := calls[0].Params["entities"].([]any)
			if len(entities) != 1 {
				t.Fatalf("entities=%v", entities)
			}
			e := entities[0].(map[string]any)
			user, _ := e["user"].(map[string]any)
			if e["type"] != "text_mention" || user["id"] != float64(10002) || e["offset"] != float64(2) || e["length"] != float64(10) {
				t.Fatalf("entity=%v", e)
			}
		}
	}
}

func TestLiveMentionProductionSampleReplay(t *testing.T) {
	client := liveLLMClient(t)
	response, err := client.Generate(context.Background(), llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: `只输出待发送的群聊正文。提及用户使用 <diana-at id="10002"/>，不要用纯文本 @ID。不要调用工具。`},
		{Role: llm.RoleUser, Content: "回放这条消息的排版：先提及用户10002，紧接着写“你 Plus 不是快到期了吗”，不要在提及后加空格。"},
	}})
	if err != nil {
		t.Fatal("live replay failed; inspect provider logs")
	}
	api := newFakeTelegramAPI(t, nil)
	r := NewRuntime(BotConfig{}, api.channel(), NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Platform: PlatformTelegram, Kind: EventKindGroup, GroupID: "-10001", UserID: "10002", SenderName: "Alice", SenderUsername: "some_user"}
	msg := r.normalizeOutgoingMentions(event, OutgoingMessage{GroupID: event.GroupID, Text: response.Text})
	msg = r.resolveOutgoingMentionNames(event, msg)
	if !strings.Contains(msg.Text, mentionMarkerFor(event.UserID)) {
		t.Fatalf("model did not emit a usable mention: %q", msg.Text)
	}
	if err := api.channel().Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	calls := api.callsOf("sendMessage")
	if len(calls) != 1 {
		t.Fatalf("calls=%v", calls)
	}
	entities, _ := calls[0].Params["entities"].([]any)
	if len(entities) != 1 || entities[0].(map[string]any)["type"] != "text_mention" {
		t.Fatalf("entities=%v", entities)
	}
	t.Logf("model=%s wire_text=%q entities=%v", response.Model, calls[0].Params["text"], entities)
}
