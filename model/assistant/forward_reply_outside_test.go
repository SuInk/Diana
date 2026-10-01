// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// orderedChannel 把直发和 API 调用记在同一条时间线上，好核对卡片前后的顺序。
type orderedChannel struct {
	mu     sync.Mutex
	log    []string
	cards  [][]map[string]any
	apiErr error
}

func (*orderedChannel) Connect(context.Context, EventHandler) error { return nil }
func (*orderedChannel) Close() error                                { return nil }
func (*orderedChannel) Status() ChannelStatus                       { return ChannelStatus{} }

func (c *orderedChannel) CallAPI(_ context.Context, action string, params map[string]any) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.log = append(c.log, "api:"+action)
	if c.apiErr != nil {
		return nil, c.apiErr
	}
	if nodes, ok := params["messages"].([]map[string]any); ok {
		c.cards = append(c.cards, nodes)
	}
	return map[string]any{"message_id": "card"}, nil
}

func (c *orderedChannel) Send(_ context.Context, msg OutgoingMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.log = append(c.log, "send:"+msg.Text)
	return nil
}

func (c *orderedChannel) snapshot() ([]string, [][]map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.log...), append([][]map[string]any(nil), c.cards...)
}

const forwardOutsideTestBlock = "配置如下：" + notificationLineMarker +
	"1. 打开设置页，找到聊天模型这一栏，选上要用的模型" + notificationLineMarker +
	"2. 在下面的合并转发里把字数阈值调到合适的位置" + notificationLineMarker +
	"3. 保存以后发一条长消息试一下，看卡片是不是按预期出现"

func forwardOutsideTestRuntime(channel Channel) (*Runtime, MessageEvent) {
	runtime := NewRuntime(BotConfig{BotAccount: "42", ForwardReplyThreshold: 60, SendChunkIntervalMS: 1}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, GroupID: "30003", UserID: "10001", SelfID: "42"}
	return runtime, event
}

func forwardCardTexts(nodes []map[string]any) []string {
	var texts []string
	for _, node := range nodes {
		data, _ := node["data"].(map[string]any)
		texts = append(texts, forwardNodeContentText(data["content"]))
	}
	return texts
}

// 开场一句和收尾一句留在卡片外，正文进卡片，顺序和原文一致。
func TestForwardReplyKeepsLeadAndTailOutsideCard(t *testing.T) {
	withFastSendTiming(t)
	channel := &orderedChannel{}
	runtime, event := forwardOutsideTestRuntime(channel)
	reply := "给你整理好了" + notificationSplitMarker + forwardOutsideTestBlock + notificationSplitMarker + "有问题再问我"

	if _, err := runtime.sendDecorated(t.Context(), event, reply, outboundDecoration{}); err != nil {
		t.Fatal(err)
	}
	log, cards := channel.snapshot()
	want := []string{"send:给你整理好了", "api:send_group_forward_msg", "send:有问题再问我"}
	if strings.Join(log, "|") != strings.Join(want, "|") {
		t.Fatalf("发送顺序 = %#v，想要 %#v", log, want)
	}
	if len(cards) != 1 {
		t.Fatalf("应只发一张卡片，实际 %d 张", len(cards))
	}
	texts := forwardCardTexts(cards[0])
	if len(texts) != 1 || !strings.HasPrefix(texts[0], "配置如下") {
		t.Fatalf("卡片里应只有正文，实际 %#v", texts)
	}
}

// 一串短气泡凑够条数进的卡片：首尾和中间是同一种东西，不能拆散。
func TestForwardReplyKeepsShortBubbleListTogether(t *testing.T) {
	withFastSendTiming(t)
	channel := &orderedChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "42", ForwardReplyChunkThreshold: 3, SendChunkIntervalMS: 1}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, GroupID: "30003", UserID: "10001", SelfID: "42"}
	reply := strings.Join([]string{"推荐这几部", "星际穿越", "盗梦空间", "信条", "都挺好看"}, notificationSplitMarker)

	if _, err := runtime.sendDecorated(t.Context(), event, reply, outboundDecoration{}); err != nil {
		t.Fatal(err)
	}
	log, cards := channel.snapshot()
	if len(log) != 1 || log[0] != "api:send_group_forward_msg" {
		t.Fatalf("短气泡清单应整张进卡片，实际 %#v", log)
	}
	if got := len(cards[0]); got != 5 {
		t.Fatalf("卡片节点数 = %d，想要 5", got)
	}
}

// 拆完剩下的正文不够阈值就不拆：卡片得是因为正文长才存在。
func TestForwardReplyKeepsLeadWhenBodyAloneIsShort(t *testing.T) {
	channel := &orderedChannel{}
	runtime, event := forwardOutsideTestRuntime(channel)
	cfg := runtime.effectiveConfigForEvent(event)
	cfg.ForwardReplyThreshold = len([]rune(strings.ReplaceAll(forwardOutsideTestBlock, notificationLineMarker, "\n"))) + 2
	reply := "给你整理好了" + notificationSplitMarker + forwardOutsideTestBlock

	lead, card, tail := splitForwardReplyOutside(reply, event, cfg)
	if lead != "" || tail != "" || len(card) != 2 {
		t.Fatalf("lead=%q tail=%q card=%#v", lead, tail, card)
	}
}

// 清单项和长句不算「那句话」，紧挨着的不是正文块也不拆。
func TestForwardTalkBubbleShape(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"给你整理好了", true},
		{"[diana-at:10001] 整理好了，看卡片", true},
		{"1. 第一项", false},
		{"- 第一项", false},
		{"## 第一天", false},
		{"第一行\n第二行", false},
		{strings.Repeat("长", forwardOutsideBubbleMaxRunes+1), false},
		{"[CQ:image,file=a.png]", false},
	} {
		if got := forwardTalkBubble(tc.text); got != tc.want {
			t.Errorf("forwardTalkBubble(%q) = %v，想要 %v", tc.text, got, tc.want)
		}
	}
}

// 引用标记跟着开场那句走，不进卡片。
func TestForwardReplyLeadCarriesReplyMarker(t *testing.T) {
	runtime, event := forwardOutsideTestRuntime(&orderedChannel{})
	cfg := runtime.effectiveConfigForEvent(event)
	reply := replyMarkerPrefix + "m1]给你整理好了" + notificationSplitMarker + forwardOutsideTestBlock

	lead, card, _ := splitForwardReplyOutside(reply, event, cfg)
	if lead != replyMarkerPrefix+"m1]给你整理好了" {
		t.Fatalf("lead = %q", lead)
	}
	for _, chunk := range card {
		if strings.Contains(chunk, replyMarkerPrefix) {
			t.Fatalf("引用标记进了卡片：%q", chunk)
		}
	}
}

// 卡片发不出去时退回逐条：开场已经发过，不再发第二遍。
func TestForwardReplyFallbackDoesNotRepeatLead(t *testing.T) {
	withFastSendTiming(t)
	channel := &orderedChannel{apiErr: errors.New("forward unsupported")}
	runtime, event := forwardOutsideTestRuntime(channel)
	reply := "给你整理好了" + notificationSplitMarker + forwardOutsideTestBlock + notificationSplitMarker + "有问题再问我"

	if _, err := runtime.sendDecorated(t.Context(), event, reply, outboundDecoration{}); err != nil {
		t.Fatal(err)
	}
	log, _ := channel.snapshot()
	leads := 0
	for _, entry := range log {
		if entry == "send:给你整理好了" {
			leads++
		}
	}
	if leads != 1 {
		t.Fatalf("开场应只发一次，实际 %d 次：%#v", leads, log)
	}
	if last := log[len(log)-1]; last != "send:有问题再问我" {
		t.Fatalf("收尾应最后发出，实际 %#v", log)
	}
}
