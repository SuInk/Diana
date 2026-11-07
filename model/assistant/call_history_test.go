// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 挂断时记一条通话记录：聊天页显示成通话提示，模型看到的是旁白，不是谁说的话。
func TestVoiceCallFinishRecordsCallHistoryOnce(t *testing.T) {
	runtime, _, _ := newVoiceCallTestRuntime(t, "")
	_, base, err := runtime.ownerChatEvent("bot")
	if err != nil {
		t.Fatal(err)
	}
	call := &VoiceCall{runtime: runtime, callID: "c1", base: base, started: time.Now().Add(-(3*time.Minute + 12*time.Second))}
	call.Finish()
	call.Finish()

	history, err := runtime.WebChatHistory(context.Background(), "bot", 50)
	if err != nil {
		t.Fatal(err)
	}
	var calls []WebChatMessage
	for _, msg := range history {
		if msg.Kind == "call" {
			calls = append(calls, msg)
		}
	}
	if len(calls) != 1 || calls[0].Text != "[语音通话] 通话 3 分 12 秒" || calls[0].Role != "user" {
		t.Fatalf("通话记录 = %#v，全部历史 %#v", calls, history)
	}

	var event MessageEvent
	runtime.mu.RLock()
	for _, item := range runtime.history[sessionKey(runtime.bindInboundEventIdentity(base))] {
		if isCallHistoryEvent(item) {
			event = item
		}
	}
	runtime.mu.RUnlock()
	if !isNarrationHistoryEvent(event) || assistantHistoryEvent(event, "bot") {
		t.Fatalf("通话记录应是旁白，event = %#v", event)
	}
	cfg := runtime.effectiveConfigForEvent(base)
	messages := runtime.renderPromptHistoryEvent(context.Background(), base, event, cfg, true)
	if len(messages) != 1 || messages[0].Role != llm.RoleUser || !strings.Contains(messages[0].Content, "语音通话记录，不是谁说的话") || !strings.Contains(messages[0].Content, "3 分 12 秒") {
		t.Fatalf("给模型看的通话记录 = %#v", messages)
	}
}

// 刚接通就挂、连一句话都来不及说的不记。
func TestVoiceCallFinishSkipsInstantHangup(t *testing.T) {
	runtime, _, _ := newVoiceCallTestRuntime(t, "")
	_, base, err := runtime.ownerChatEvent("bot")
	if err != nil {
		t.Fatal(err)
	}
	(&VoiceCall{runtime: runtime, base: base, started: time.Now()}).Finish()
	history, err := runtime.WebChatHistory(context.Background(), "bot", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range history {
		if msg.Kind == "call" {
			t.Fatalf("瞬间挂断不该记通话：%#v", msg)
		}
	}
}

func TestFormatCallDuration(t *testing.T) {
	for duration, want := range map[time.Duration]string{
		400 * time.Millisecond:          "1 秒",
		42 * time.Second:                "42 秒",
		3*time.Minute + 12*time.Second:  "3 分 12 秒",
		2*time.Hour + 5*time.Minute + 9: "2 小时 5 分",
	} {
		if got := formatCallDuration(duration); got != want {
			t.Errorf("formatCallDuration(%s) = %q, want %q", duration, got, want)
		}
	}
}
