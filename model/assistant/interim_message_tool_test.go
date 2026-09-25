// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

func interimTestRuntime(channel *recordingChannel, provider LLMProvider) *Runtime {
	return NewRuntime(BotConfig{AgentEnabled: true, AgentMaxSteps: 4, BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
}

func interimTestEvent() MessageEvent {
	return MessageEvent{
		Kind:           EventKindGroup,
		SelfID:         "42",
		GroupID:        "20001",
		UserID:         "10001",
		MessageID:      "interim-1",
		SenderName:     "Alice",
		RawMessage:     "帮我查下端口怎么被占了",
		Segments:       []MessageSegment{{Type: "text", Data: map[string]string{"text": "帮我查下端口怎么被占了"}}},
		proactiveReply: true,
	}
}

// 说一句就发出去，记进账本，这一轮不结束。
func TestInterimMessageToolSendsNowAndKeepsCount(t *testing.T) {
	channel := &recordingChannel{}
	runtime := interimTestRuntime(channel, &sequenceLLMProvider{})
	tool := newDianaInterimMessageTool(runtime, interimTestEvent())
	ctx := withInterimMessages(context.Background())
	out, err := tool.Run(ctx, map[string]any{"text": "我去查一下。"})
	if err != nil {
		t.Fatal(err)
	}
	if sent := channel.sentSnapshot(); len(sent) != 1 || sent[0].Text != "我去查一下" {
		t.Fatalf("sent = %#v", sent)
	}
	if !strings.Contains(out, `"remaining":2`) || len(interimMessagesSent(ctx)) != 1 {
		t.Fatalf("result = %s ledger = %q", out, interimMessagesSent(ctx))
	}
}

// 不是用来把答案拆开发的：长话、换行、超过次数都不发。
func TestInterimMessageToolLimits(t *testing.T) {
	channel := &recordingChannel{}
	runtime := interimTestRuntime(channel, &sequenceLLMProvider{})
	tool := newDianaInterimMessageTool(runtime, interimTestEvent())
	ctx := withInterimMessages(context.Background())
	if _, err := tool.Run(ctx, map[string]any{"text": strings.Repeat("长", interimMessageMaxRunes+1)}); err == nil {
		t.Fatal("an over-long interim message was accepted")
	}
	if _, err := tool.Run(ctx, map[string]any{"text": "第一句\n第二句"}); err == nil {
		t.Fatal("a multi-line interim message was accepted")
	}
	for i := 0; i < interimMessageMaxPerTurn; i++ {
		if _, err := tool.Run(ctx, map[string]any{"text": "进度更新"}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := tool.Run(ctx, map[string]any{"text": "再说一句"})
	if err != nil || !strings.Contains(out, `"ok":false`) {
		t.Fatalf("the fourth interim message was not refused: %s %v", out, err)
	}
	if len(channel.sentSnapshot()) != interimMessageMaxPerTurn {
		t.Fatalf("sent %d messages, want %d", len(channel.sentSnapshot()), interimMessageMaxPerTurn)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"text": "没有账本"}); err == nil {
		t.Fatal("interim messages must only work inside a reply turn")
	}
}

// 整轮走一遍：先说「我去查」，再给结果，两条都发出去。
func TestReplyTurnSendsInterimThenFinal(t *testing.T) {
	channel := &recordingChannel{}
	provider := &sequenceLLMProvider{replies: []string{
		`{"action":"tool","tool":"say","input":{"text":"我去查一下。"}}`,
		`{"action":"final","content":"查到了，是上次没退干净的进程占着。"}`,
	}}
	runtime := interimTestRuntime(channel, provider)
	event := interimTestEvent()
	reply, err := runtime.replyTo(context.Background(), event, event.RawMessage)
	if err != nil {
		t.Fatal(err)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 2 || sent[0].Text != "我去查一下" || !strings.HasPrefix(sent[1].Text, "查到了") || !strings.HasPrefix(reply, "查到了") {
		t.Fatalf("reply=%q sent=%#v", reply, sent)
	}
}

// 中途已经把话说完了：收尾静默，不补「没有生成有效回复」，也不把同一句再发一遍。
func TestReplyTurnAfterInterimNeedsNoFinalText(t *testing.T) {
	for name, final := range map[string]string{
		"静默收尾":   `{"action":"final","silent":true,"silent_reason":"中途已经说完了"}`,
		"重复中途那句": `{"action":"final","content":"好嘞，马上发你。"}`,
	} {
		t.Run(name, func(t *testing.T) {
			channel := &recordingChannel{}
			provider := &sequenceLLMProvider{replies: []string{
				`{"action":"tool","tool":"say","input":{"text":"好嘞，马上发你"}}`,
				final,
			}}
			runtime := interimTestRuntime(channel, provider)
			event := interimTestEvent()
			_, _ = runtime.replyTo(context.Background(), event, event.RawMessage)
			sent := channel.sentSnapshot()
			if len(sent) != 1 || sent[0].Text != "好嘞，马上发你" {
				t.Fatalf("sent = %#v", sent)
			}
		})
	}
}
