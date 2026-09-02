// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDispatchRomanceAnniversaryGreetings(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.Local)
	memory := newMemoryUserMemoryStore()
	// 三个月整的恋人：该收到问候。
	memory.profiles["10005"] = UserMemoryProfile{
		UserID: "10005", DisplayName: "青禾", Favorability: 90, MessageCount: 100,
		Romance: &UserRomanceState{Active: true, Since: now.AddDate(0, -3, 0), StartedBy: "user"},
	}
	// 普通日子的恋人：不打扰。
	memory.profiles["10006"] = UserMemoryProfile{
		UserID: "10006", Favorability: 80, MessageCount: 60,
		Romance: &UserRomanceState{Active: true, Since: now.AddDate(0, -1, -10)},
	}
	// 不是恋人：无关。
	memory.profiles["10007"] = UserMemoryProfile{UserID: "10007", Favorability: 80}

	channel := &recordingChannel{}
	provider := &capturingLLMProvider{reply: "三个月啦，今天也想你。"}
	runtime := NewRuntime(BotConfig{ID: "bot", RomanceEnabled: boolPointer(true)}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.SetUserMemoryStore(memory)
	runtime.now = func() time.Time { return now }

	runtime.dispatchRomanceAnniversaryGreetings(context.Background())
	// 发送层会自然分条，这里聚合断言：只发给了三个月整的那位，正文来自模型。
	sent := len(channel.sent)
	if sent == 0 {
		t.Fatal("no greeting sent")
	}
	var texts []string
	for _, message := range channel.sent {
		if message.UserID != "10005" || message.GroupID != "" {
			t.Fatalf("sent = %#v", channel.sent)
		}
		texts = append(texts, message.Text)
	}
	if joined := strings.Join(texts, " "); !strings.Contains(joined, "今天也想你") {
		t.Fatalf("greeting = %q", joined)
	}
	// 发过之后落了防重发标记，再扫一遍不会发第二条。
	if got := memory.profiles["10005"].Romance.LastGreetedOn; got != now.Format("2006-01-02") {
		t.Fatalf("last greeted = %q", got)
	}
	runtime.dispatchRomanceAnniversaryGreetings(context.Background())
	if len(channel.sent) != sent {
		t.Fatalf("greeting repeated: %#v", channel.sent)
	}
}

func TestDispatchRomanceAnniversaryGreetingsGates(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.Local)
	profileFor := func() *memoryUserMemoryStore {
		memory := newMemoryUserMemoryStore()
		memory.profiles["10005"] = UserMemoryProfile{
			UserID: "10005", Favorability: 90, MessageCount: 100,
			Romance: &UserRomanceState{Active: true, Since: now.AddDate(-1, 0, 0)},
		}
		return memory
	}

	// 恋爱模式关着（默认）：整个轮询是空转。
	channel := &recordingChannel{}
	off := NewRuntime(BotConfig{ID: "bot"}, channel, NewPluginManager(), nil, nil, nil, nil)
	off.SetUserMemoryStore(profileFor())
	off.now = func() time.Time { return now }
	off.dispatchRomanceAnniversaryGreetings(context.Background())
	if len(channel.sent) != 0 {
		t.Fatalf("disabled romance greeted: %#v", channel.sent)
	}

	// 深夜不发：凌晨的祝福只吵人。
	night := NewRuntime(BotConfig{ID: "bot", RomanceEnabled: boolPointer(true)}, channel, NewPluginManager(), nil, nil, nil, nil)
	night.SetUserMemoryStore(profileFor())
	night.now = func() time.Time { return time.Date(2026, 9, 1, 2, 0, 0, 0, time.Local) }
	night.dispatchRomanceAnniversaryGreetings(context.Background())
	if len(channel.sent) != 0 {
		t.Fatalf("midnight greeting sent: %#v", channel.sent)
	}

	// 模型不可用：退回朴素模板，纪念日不能漏。
	memory := profileFor()
	fallback := NewRuntime(BotConfig{ID: "bot", RomanceEnabled: boolPointer(true)}, channel, NewPluginManager(), nil, nil, nil, nil)
	fallback.SetUserMemoryStore(memory)
	fallback.now = func() time.Time { return now }
	fallback.dispatchRomanceAnniversaryGreetings(context.Background())
	var texts []string
	for _, message := range channel.sent {
		if message.UserID != "10005" {
			t.Fatalf("fallback sent = %#v", channel.sent)
		}
		texts = append(texts, message.Text)
	}
	joined := strings.Join(texts, " ")
	if !strings.Contains(joined, "纪念日快乐") || !strings.Contains(joined, "1 周年") {
		t.Fatalf("fallback greeting = %q", joined)
	}
}
