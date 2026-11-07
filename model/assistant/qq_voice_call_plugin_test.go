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

// recordQQVoiceCallNotices 换掉插件发私聊提醒的出口，把提醒收进通道。
func recordQQVoiceCallNotices(plugin *QQVoiceCallPlugin, runtime *Runtime) <-chan string {
	notices := make(chan string, 8)
	plugin.runtime = runtime
	plugin.notify = func(_ context.Context, _ *Runtime, _ string, text string) error {
		notices <- text
		return nil
	}
	return notices
}

func TestQQVoiceCallHangsUpWhenPeerIsNotOwner(t *testing.T) {
	for _, peer := range []string{"10002", ""} {
		link, got := fakeQQCallAgent(t)
		plugin := NewQQVoiceCallPlugin()
		notices := recordQQVoiceCallNotices(plugin, &Runtime{})
		calls := newQQCallSet(plugin, link)
		calls.answered["c1"] = qqCallTarget{profileID: "default", ownerID: "10001"}
		calls.connected(context.Background(), qqcall.Message{Type: qqcall.TypeConnected, Call: "c1", Name: "主人", PeerUIN: peer})
		expectQQCallMessage(t, got, qqcall.Message{Type: qqcall.TypeHangup, Call: "c1"})
		if calls.active != nil {
			t.Fatalf("peer %q: a session was opened for someone who is not the owner", peer)
		}
		select {
		case text := <-notices:
			// 读不到 QQ 号是坏了，要告诉主人；别人打来是正常拦截，不打扰主人。
			if peer != "" || !strings.Contains(text, "读不到对方的 QQ 号") {
				t.Fatalf("peer %q: unexpected owner notice %q", peer, text)
			}
		case <-time.After(300 * time.Millisecond):
			if peer == "" {
				t.Fatal("owner was not told the peer could not be identified")
			}
		}
	}
}

// 前提不齐（这里是没配语音插槽）时不接，并私聊告诉主人缺什么；来电昵称是谁都一样。
func TestQQVoiceCallScreenTellsOwnerWhyItDidNotAnswer(t *testing.T) {
	runtime, _, _ := newVoiceCallTestRuntime(t, "")
	link, got := fakeQQCallAgent(t)
	plugin := NewQQVoiceCallPlugin()
	notices := recordQQVoiceCallNotices(plugin, runtime)
	plugin.change = PluginStateChange{Enabled: true, EnabledProfiles: []string{"bot"}, Settings: SettingValues{}}
	calls := newQQCallSet(plugin, link)
	for range 2 {
		calls.screen(context.Background(), qqcall.Message{Type: qqcall.TypeRinging, Call: "c1", Name: "随便什么昵称🐱"})
	}
	expectNoQQCallMessage(t, got)
	select {
	case text := <-notices:
		if !strings.Contains(text, "语音识别") {
			t.Fatalf("notice should say what is missing, got %q", text)
		}
	case <-time.After(time.Second):
		t.Fatal("owner was not told why the call was not answered")
	}
	select {
	case text := <-notices:
		t.Fatalf("the same problem should only be reported once, got another %q", text)
	case <-time.After(200 * time.Millisecond):
	}
	status, ok := plugin.RuntimeStatus(context.Background())
	if !ok || len(status.Problems) == 0 || !strings.Contains(status.Recent, "没接") {
		t.Fatalf("runtime status should list the problems and the last call, got %#v", status)
	}
}

func TestQQVoiceCallRuntimeStatusSilentWhenDisabled(t *testing.T) {
	plugin := NewQQVoiceCallPlugin()
	plugin.runtime = &Runtime{}
	if _, ok := plugin.RuntimeStatus(context.Background()); ok {
		t.Fatal("a disabled plugin should not report status")
	}
}

func TestWithBotProfileOptionsListsOnlyMatchingPlatforms(t *testing.T) {
	state := PluginState{Manifest: NewQQVoiceCallPlugin().Manifest()}
	profiles := []BotConfig{
		{ID: "tg", Name: "电报", Platform: PlatformTelegram},
		{ID: "qq", Name: "嘉然", Platform: PlatformOneBotV11, BotAccount: "10003"},
	}
	filled := WithBotProfileOptions(state, profiles)
	spec, _ := findPluginSettingSpec(filled.Manifest.Settings, qqVoiceCallSettingProfile)
	if len(spec.Options) != 2 || spec.Options[0].Value != "" || spec.Options[1].Value != "qq" || spec.Options[1].Label != "嘉然（10003）" {
		t.Fatalf("options = %#v", spec.Options)
	}
	original, _ := findPluginSettingSpec(state.Manifest.Settings, qqVoiceCallSettingProfile)
	if len(original.Options) != 0 {
		t.Fatal("filling options must not touch the shared manifest")
	}
	if _, err := normalizePluginSettings(filled.Manifest.Settings, map[string]any{qqVoiceCallSettingProfile: "qq"}); err != nil {
		t.Fatalf("bot_profile value should save: %v", err)
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
