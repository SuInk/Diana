// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 通话里说的话带着通话 ID，挂断记的那条通话记录也带着同一个 ID，聊天页才能把它们归到一起；
// 通话没挂断时 WebChatInfo 报正在通话。
func TestWebChatGroupsCallTurnsUnderRecord(t *testing.T) {
	runtime, _, _ := newVoiceCallTestRuntime(t, "听得到呀")
	// 测试运行时没配语音插槽，NewVoiceCall 会拒绝；照它的样子手工建，再登记成进行中。
	_, base, err := runtime.ownerChatEvent("bot")
	if err != nil {
		t.Fatal(err)
	}
	base.voiceCall = true
	call := &VoiceCall{runtime: runtime, callID: "c42", base: base, started: time.Now()}
	runtime.trackVoiceCall(call)
	active := runtime.WebChatInfo("bot").ActiveCall
	if active == nil || active.ID != "c42" || active.StartedAt == 0 {
		t.Fatalf("通话中 ActiveCall = %#v", active)
	}
	if err := call.Reply(context.Background(), "喂，听得到吗", func(string) {}); err != nil {
		t.Fatal(err)
	}
	call.started = call.started.Add(-5 * time.Second)
	call.Finish()
	if got := runtime.WebChatInfo("bot").ActiveCall; got != nil {
		t.Fatalf("挂断后 ActiveCall 应为空，got %#v", got)
	}

	history, err := runtime.WebChatHistory(context.Background(), "bot", 50)
	if err != nil {
		t.Fatal(err)
	}
	var user, bot, record bool
	for _, msg := range history {
		switch {
		case msg.Kind == "call":
			record = msg.CallID == "c42"
		case msg.Role == "user" && msg.Text == "喂，听得到吗":
			user = msg.CallID == "c42"
		case msg.Role == "assistant" && strings.Contains(msg.Text, "听得到"):
			bot = msg.CallID == "c42"
		}
	}
	if !user || !bot || !record {
		t.Fatalf("通话轮次和记录都该带通话 ID c42：user=%v bot=%v record=%v history=%#v", user, bot, record, history)
	}
}

func TestVoiceCallIDFromTurn(t *testing.T) {
	cases := map[string]string{
		"call-c42-3":              "c42",
		"call-c42-out-12":         "c42",
		"call-call-qq-9f-a-2":     "call-qq-9f-a",
		"call-call-qq-9f-a-out-1": "call-qq-9f-a",
		"web-out-3":               "",
		"call-nonumber-x":         "",
		"local-call-c42":          "",
		"12345":                   "",
	}
	for id, want := range cases {
		if got := voiceCallIDFromTurn(id); got != want {
			t.Errorf("voiceCallIDFromTurn(%q) = %q, want %q", id, got, want)
		}
	}
}

// 聊天页不能把给模型看的 <diana-quote id="…"/> 原样显示出来：引用段拆成 Quote，
// 原话在记录里就补上。
func TestWebChatHistoryRendersQuoteInsteadOfMarker(t *testing.T) {
	runtime, _, _ := newVoiceCallTestRuntime(t, "")
	_, base, err := runtime.ownerChatEvent("bot")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	first := base
	first.MessageID, first.Time = "10001", now-10
	first.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "今晚吃什么"}}}
	reply := base
	reply.MessageID, reply.Time = "10002", now
	reply.Segments = []MessageSegment{
		{Type: "reply", Data: map[string]string{"id": "10001"}},
		{Type: "text", Data: map[string]string{"text": "第一行\n第二行"}},
	}
	literal := base
	literal.MessageID, literal.Time = "10003", now+1
	literal.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": `<diana-quote id="10001"/>还是火锅`}}}
	for _, event := range []MessageEvent{first, reply, literal} {
		runtime.remember(runtime.bindInboundEventIdentity(event))
	}

	history, err := runtime.WebChatHistory(context.Background(), "bot", 50)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, msg := range history {
		if strings.Contains(msg.Text, "diana-quote") {
			t.Fatalf("正文里不该有引用标记：%#v", msg)
		}
		switch msg.ID {
		case "10002":
			found++
			if msg.Text != "第一行\n第二行" || msg.Quote == nil || msg.Quote.MessageID != "10001" || msg.Quote.Text != "今晚吃什么" {
				t.Fatalf("引用消息 = %#v quote=%#v", msg, msg.Quote)
			}
		case "10003":
			found++
			if msg.Text != "还是火锅" || msg.Quote == nil || msg.Quote.Text != "今晚吃什么" {
				t.Fatalf("文字里的引用标记 = %#v quote=%#v", msg, msg.Quote)
			}
		}
	}
	if found != 2 {
		t.Fatalf("没找到两条引用消息：%#v", history)
	}
}
