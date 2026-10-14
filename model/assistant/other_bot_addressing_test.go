package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
)

func TestUndirectedTurnCannotChangeSettings(t *testing.T) {
	registry := agent.NewToolRegistry()
	applyUndirectedTurnLimits(MessageEvent{Kind: EventKindGroup, GroupID: "20001", proactiveReply: true}, registry)
	err := registry.OperationDisabledError(botConfigToolName, map[string]any{"operation": "update"})
	if err == nil || !strings.Contains(err.Error(), "@我") {
		t.Fatalf("主动接话轮次仍能改设置: %v", err)
	}
	if err := registry.OperationDisabledError(botConfigToolName, map[string]any{"operation": "get"}); err != nil {
		t.Fatalf("查看设置被拦了: %v", err)
	}

	directed := agent.NewToolRegistry()
	applyUndirectedTurnLimits(MessageEvent{Kind: EventKindGroup, GroupID: "20001", ToMe: true}, directed)
	if err := directed.OperationDisabledError(botConfigToolName, map[string]any{"operation": "update"}); err != nil {
		t.Fatalf("@本机的轮次被拦了: %v", err)
	}
}

func TestMessageAddressedToOtherBotSkipsReplyFlow(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "10000", MarkedBotIDs: []string{"10099"}}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	at := func(id string) MessageSegment { return MessageSegment{Type: "at", Data: map[string]string{"qq": id}} }
	text := func(s string) MessageSegment { return MessageSegment{Type: "text", Data: map[string]string{"text": s}} }

	mentionOther := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "30001", SelfID: "10000",
		Segments: []MessageSegment{at("10099"), text(" 签到")}}
	_, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), mentionOther)
	if handled || outcome != "ignored_other_bot_addressed" {
		t.Fatalf("@别的机器人: handled=%v outcome=%q", handled, outcome)
	}

	quoteOther := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "30002", SelfID: "10000",
		Quoted: &QuotedMessage{MessageID: "29999", UserID: "10099"}, Segments: []MessageSegment{text("再来一次")}}
	if !runtime.addressedToOtherBot(quoteOther, "再来一次") {
		t.Fatal("回复别的机器人的消息没被认出来")
	}

	both := mentionOther
	both.Segments = []MessageSegment{at("10099"), at("10000"), text(" 你俩谁对")}
	if runtime.addressedToOtherBot(both, "你俩谁对") {
		t.Fatal("同时 @本机的消息被跳过了")
	}
	human := mentionOther
	human.Segments = []MessageSegment{at("10002"), text(" 签到")}
	if runtime.addressedToOtherBot(human, "签到") {
		t.Fatal("@真人被当成叫别的机器人")
	}
}

func TestAnsweredElsewhereBeforeGeneration(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "10000", MarkedBotIDs: []string{"10099"}}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	sent := time.Now().Add(-time.Minute).Unix()
	command := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "30001", SelfID: "10000", Time: sent, RawMessage: "签到"}
	runtime.remember(command)
	if taken, _ := runtime.answeredElsewhere(context.Background(), command, nil); taken {
		t.Fatal("没人接的消息被判成已被接走")
	}

	runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10002", MessageID: "30002", Time: sent + 1, RawMessage: "哈哈"})
	if taken, _ := runtime.answeredElsewhere(context.Background(), command, nil); taken {
		t.Fatal("真人随口一句被当成接走")
	}

	runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10099", MessageID: "30003", Time: sent + 2, RawMessage: "签到成功"})
	taken, reason := runtime.answeredElsewhere(context.Background(), command, nil)
	if !taken || !strings.Contains(reason, "别的机器人") {
		t.Fatalf("别的机器人已接: taken=%v reason=%q", taken, reason)
	}

	late := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "40001", SelfID: "10000", Time: sent - 100, RawMessage: "签到"}
	if taken, _ := runtime.answeredElsewhere(context.Background(), late, nil); taken {
		t.Fatal("窗口之外的发言被算进来了")
	}

	quoting := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "50001", SelfID: "10000", Time: sent + 10, RawMessage: "怎么修"}
	runtime.remember(quoting)
	runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10003", MessageID: "50002", Time: sent + 12,
		Segments: []MessageSegment{{Type: "reply", Data: map[string]string{"id": "50001"}}, {Type: "text", Data: map[string]string{"text": "重启就好"}}}})
	if taken, _ := runtime.answeredElsewhere(context.Background(), quoting, nil); !taken {
		t.Fatal("别人已回复这条，没认出来")
	}
}
