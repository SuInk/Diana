// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

func interimTestRuntime(channel *recordingChannel, provider LLMProvider) *Runtime {
	return NewRuntime(BotConfig{AgentEnabled: true, BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
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

// 整轮走一遍：send 和工具同一步先说「我去查」，再只 send 结果，两条都发出去。
func TestReplyTurnSendsInterimThenFinal(t *testing.T) {
	channel := &recordingChannel{}
	provider := &sequenceLLMProvider{replies: []string{
		`tool_calls:[{"id":"s1","name":"send","arguments":{"text":"我去查一下。"}},{"id":"t1","name":"tool_search","arguments":{"query":"端口"}}]`,
		`tool_calls:[{"id":"s2","name":"send","arguments":{"text":"查到了，是上次没退干净的进程占着。"}}]`,
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
		"空 send": `tool_calls:[{"id":"s2","name":"send","arguments":{"text":""}}]`,
		"重复中途那句": `tool_calls:[{"id":"s2","name":"send","arguments":{"text":"好嘞，马上发你。"}}]`,
	} {
		t.Run(name, func(t *testing.T) {
			channel := &recordingChannel{}
			provider := &sequenceLLMProvider{replies: []string{
				`tool_calls:[{"id":"s1","name":"send","arguments":{"text":"好嘞，马上发你"}},{"id":"t1","name":"tool_search","arguments":{"query":"发送"}}]`,
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

// 单独 send、填了 continue 的那句（RunReply）常常就是一整条回复：和最终回复一样先过
// 发送前审核，审核拦下就不发；放行时不受中途发言 80 字的限制。
func TestInterimReplyIsAuditedBeforeSend(t *testing.T) {
	long := strings.Repeat("长", interimMessageMaxRunes+10)
	for name, tc := range map[string]struct {
		verdict string
		sent    bool
	}{
		"拦下": {`{"send_confidence":0.99,"reason":"ok","account_safe":false,"account_risk":"explicit"}`, false},
		"放行": {`{"send_confidence":0.99,"reason":"ok","account_safe":true}`, true},
	} {
		t.Run(name, func(t *testing.T) {
			channel := &recordingChannel{}
			provider := &qualityTestProvider{reply: tc.verdict}
			runtime := interimTestRuntime(channel, provider)
			event := interimTestEvent()
			event.proactiveReply = false
			tool := newDianaInterimMessageTool(runtime, event)
			ctx := withInterimMessages(context.Background())
			setInterimReplyAudit(ctx, event.RawMessage, false)
			_, err := tool.RunReply(ctx, map[string]any{"text": long})
			if len(provider.requests) != 1 {
				t.Fatalf("audit calls = %d, want 1", len(provider.requests))
			}
			sent := channel.sentSnapshot()
			if tc.sent && (err != nil || len(sent) != 1 || sent[0].Text != long) {
				t.Fatalf("err = %v, sent = %#v", err, sent)
			}
			if !tc.sent && (err == nil || !strings.Contains(err.Error(), "审核没通过") || len(sent) != 0) {
				t.Fatalf("err = %v, sent = %#v", err, sent)
			}
		})
	}
	// 和工具同一步的那句（Run）照旧不审、限 80 字。
	channel := &recordingChannel{}
	provider := &qualityTestProvider{reply: `{"account_safe":false}`}
	tool := newDianaInterimMessageTool(interimTestRuntime(channel, provider), interimTestEvent())
	ctx := withInterimMessages(context.Background())
	if _, err := tool.Run(ctx, map[string]any{"text": "我去查一下"}); err != nil || len(provider.requests) != 0 {
		t.Fatalf("err = %v, audit calls = %d", err, len(provider.requests))
	}
}

// 运行记录的回复内容要带上中途先发出去的话，按发出顺序一条一行。
func TestReplyRecordTextIncludesInterimMessages(t *testing.T) {
	cases := []struct {
		name    string
		interim []string
		reply   string
		want    string
	}{
		{name: "没有中途发言时原样", reply: "最终答案", want: "最终答案"},
		{name: "中途发言排在前面", interim: []string{"这就来，我去查下"}, reply: "查到了", want: "这就来，我去查下\n查到了"},
		{name: "只有中途发言", interim: []string{"我去查一下", "还在查"}, want: "我去查一下\n还在查"},
		{name: "最终回复就是中途那句时不重复", interim: []string{"在的"}, reply: "在的", want: "在的"},
	}
	for _, tc := range cases {
		if got := replyRecordText(tc.interim, tc.reply); got != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
