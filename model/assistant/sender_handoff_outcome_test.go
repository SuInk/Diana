// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
	"time"
)

// 连发交接和「发送结果不明」（#830）的衔接：接手那一轮的发送超时、结果不明时，
// 回推或历史确认送达了才算回出去，交接落定；确认不了（dropped_outbound_unconfirmed）
// 不算，前一条被放回来自己回答。

func outcomeDirectedMessage(messageID, text string, at int64) MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, GroupID: outcomeTestGroupID, UserID: outcomeTestUserID, SelfID: outcomeTestSelfID,
		MessageID: messageID, Time: at, ToMe: true,
		RawMessage: "[CQ:at,qq=" + outcomeTestSelfID + "] " + text,
		Segments: []MessageSegment{
			{Type: "at", Data: map[string]string{"qq": outcomeTestSelfID}},
			{Type: "text", Data: map[string]string{"text": " " + text}},
		},
	}
}

func TestHandoffSettlesByConfirmedDeliveryOfAmbiguousSend(t *testing.T) {
	for _, tc := range []struct {
		name        string
		outcomes    []error
		echo        bool
		history     bool
		wantFirst   string
		wantAttempt int
	}{
		// 发送超时，回推确认送达：只发了一次，交接落定，前一条不再回答。
		{name: "confirmed by echo", outcomes: []error{ambiguousSendError("send_group_msg")}, echo: true, wantFirst: "superseded_follow_up", wantAttempt: 1},
		// 没有回推，历史里查到了这条：同样算回出去。
		{name: "confirmed by history", outcomes: []error{ambiguousSendError("send_group_msg")}, history: true, wantFirst: "superseded_follow_up", wantAttempt: 1},
		// 超时、查不到、重发一次仍然不明：确认不了，不算回出去，前一条被放回来自己回答
		// （原来那次、重发一次，加上前一条自己的回复）。
		{name: "unconfirmed", outcomes: []error{ambiguousSendError("send_group_msg"), ambiguousSendError("send_group_msg")}, wantFirst: "replied", wantAttempt: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channel := &ambiguousOutboundChannel{outcomes: tc.outcomes}
			if tc.history {
				channel.history = []map[string]any{selfHistoryItem(54402, time.Now().Add(time.Second), "几条一起回答")}
			}
			provider := &burstReplyProvider{}
			disabled := false
			runtime := NewRuntime(BotConfig{BotAccount: outcomeTestSelfID, BotReplyLoopDetectionEnabled: &disabled},
				channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
			runtime.outboundEchoes.echoWait = 2 * time.Second
			if !tc.echo {
				runtime.outboundEchoes.echoWait = 20 * time.Millisecond
			}
			runtime.outboundEchoes.historyTimeout = time.Second
			feed := newEchoFeed(runtime)

			first := outcomeDirectedMessage("30001", "帮我看看这个报错", time.Now().Unix()-3)
			second := outcomeDirectedMessage("30002", "就是登录那个", time.Now().Unix())
			arriveTogether(runtime, first, second)

			firstAtHandoff := make(chan string, 1)
			channel.onSend = func(attempt int) {
				if attempt != 1 {
					return
				}
				// 接手那一轮正在发送时，前一条走到回复入口：立刻交出去。
				outcome, _ := runtime.replyAndRecord(context.Background(), first, "帮我看看这个报错", "replied")
				firstAtHandoff <- outcome
				if tc.echo {
					feedFrame(t, feed, napCatMessageSentFrame(54401, "group", outcomeTestGroupID, "几条一起回答", ""))
					waitForObservedEchoes(runtime, 1)
				}
			}

			outcome, err := runtime.replyAndRecord(context.Background(), second, "就是登录那个", "replied")
			if err != nil {
				t.Fatalf("absorber error = %v", err)
			}
			confirmed := tc.echo || tc.history
			if confirmed && outcome != "replied" {
				t.Fatalf("absorber outcome=%q, want replied after delivery was confirmed", outcome)
			}
			if !confirmed && outcome != inboundOutcomeDroppedOutboundUnconfirmed {
				t.Fatalf("absorber outcome=%q, want %s", outcome, inboundOutcomeDroppedOutboundUnconfirmed)
			}
			if got := <-firstAtHandoff; got != inboundOutcomeHandedOffPending {
				t.Fatalf("earlier turn at handoff = %q", got)
			}
			if tc.wantFirst == "superseded_follow_up" {
				if by, ok := runtime.senderTurnSupersededBy(first); !ok || by != "30002" {
					t.Fatalf("handoff should be final after confirmed delivery, got %q %v", by, ok)
				}
			}
			waitForCondition(t, 5*time.Second, func() bool { return channel.sentCount() >= tc.wantAttempt })
			time.Sleep(200 * time.Millisecond)
			if got := channel.sentCount(); got != tc.wantAttempt {
				t.Fatalf("send attempts = %d, want %d", got, tc.wantAttempt)
			}
			if tc.wantFirst == "replied" {
				if _, taken := runtime.senderTurnSupersededBy(first); taken {
					t.Fatal("an unconfirmed absorber must release the earlier message")
				}
				if !provider.sawPrompt("帮我看看这个报错") {
					t.Fatal("released earlier message never reached the model")
				}
			}
		})
	}
}
