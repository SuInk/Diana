// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/SuInk/diana/model/qqcall"
	"github.com/SuInk/diana/model/voicecall"
)

// fakeQQCallAgent 起一个假的 qqcall-agent，把 Diana 发来的控制消息收进通道。
func fakeQQCallAgent(t *testing.T) (*qqCallLink, <-chan qqcall.Message) {
	t.Helper()
	got := make(chan qqcall.Message, 8)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			kind, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg qqcall.Message
			if kind == websocket.TextMessage && json.Unmarshal(data, &msg) == nil {
				got <- msg
			}
		}
	}))
	t.Cleanup(srv.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &qqCallLink{conn: conn}, got
}

func expectQQCallMessage(t *testing.T, got <-chan qqcall.Message, want qqcall.Message) {
	t.Helper()
	select {
	case msg := <-got:
		if msg.Type != want.Type || msg.Call != want.Call {
			t.Fatalf("agent got %+v, want %+v", msg, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("agent got nothing, want %+v", want)
	}
}

func expectNoQQCallMessage(t *testing.T, got <-chan qqcall.Message) {
	t.Helper()
	select {
	case msg := <-got:
		t.Fatalf("agent should get nothing, got %+v", msg)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestQQVoiceCallHangsUpWhenPeerIsNotOwner(t *testing.T) {
	for _, peer := range []string{"10002", ""} {
		link, got := fakeQQCallAgent(t)
		calls := newQQCallSet(NewQQVoiceCallPlugin(), link)
		calls.answered["c1"] = qqCallTarget{profileID: "default", ownerID: "10001"}
		calls.connected(context.Background(), qqcall.Message{Type: qqcall.TypeConnected, Call: "c1", Name: "主人", PeerUIN: peer})
		expectQQCallMessage(t, got, qqcall.Message{Type: qqcall.TypeHangup, Call: "c1"})
		if calls.active != nil {
			t.Fatalf("peer %q: a session was opened for someone who is not the owner", peer)
		}
	}
}

func TestQQVoiceCallIgnoresCallsItDidNotAnswer(t *testing.T) {
	link, got := fakeQQCallAgent(t)
	calls := newQQCallSet(NewQQVoiceCallPlugin(), link)
	// 别人在 noVNC 里手动接起来的电话，即使对方是主人也不归机器人管。
	calls.connected(context.Background(), qqcall.Message{Type: qqcall.TypeConnected, Call: "c2", PeerUIN: "10001"})
	expectNoQQCallMessage(t, got)
	if calls.active != nil {
		t.Fatal("opened a session for a call the bot did not answer")
	}
}

func TestQQVoiceCallEndedForgetsAnsweredCall(t *testing.T) {
	link, _ := fakeQQCallAgent(t)
	calls := newQQCallSet(NewQQVoiceCallPlugin(), link)
	calls.answered["c3"] = qqCallTarget{profileID: "default", ownerID: "10001"}
	calls.ended("c3")
	if _, ok := calls.answered["c3"]; ok {
		t.Fatal("ended call is still remembered as answered")
	}
}

func TestQQVoiceCallTransportFlushesOnInterrupt(t *testing.T) {
	link, got := fakeQQCallAgent(t)
	out := &qqCallTransport{ctx: context.Background(), link: link}
	_ = out.WriteJSON(voicecall.ServerMessage{Type: voicecall.ServerAudioStart, MIME: "audio/mpeg"})
	_ = out.WriteBinary([]byte("partial"))
	if err := out.WriteJSON(voicecall.ServerMessage{Type: voicecall.ServerInterrupted}); err != nil {
		t.Fatal(err)
	}
	expectQQCallMessage(t, got, qqcall.Message{Type: qqcall.TypeFlush})
	if out.buf.Len() != 0 {
		t.Fatal("interrupted sentence audio was kept")
	}
}
