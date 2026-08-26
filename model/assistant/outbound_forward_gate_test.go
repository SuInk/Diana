// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// 截图里那张卡片：正文是站外搬来的，发帖人昵称本身就带着违禁词。
// 抽取要把两样都拿到，否则审核看到的只是半张卡。
func TestForwardNodeAuditTextCoversNameAndBody(t *testing.T) {
	nodes := buildCustomForwardNodes([]OutgoingMessage{
		{
			ForwardName: "8964天安门椒盐菠萝",
			Text:        "测试\n含1050mg普瑞巴林的尿",
		},
		{
			ForwardName: "Diana",
			Text:        "识别：小蓝鸟学习版",
		},
	}, "Diana", "10001")

	text := forwardNodeAuditText(nodes)
	for _, want := range []string{"8964天安门椒盐菠萝", "含1050mg普瑞巴林的尿", "识别：小蓝鸟学习版"} {
		if !strings.Contains(text, want) {
			t.Errorf("审核文本漏了 %q：\n%s", want, text)
		}
	}
}

// 纯图片、纯视频的卡片没有文字可审，不该白跑一次模型。
func TestForwardNodeAuditTextEmptyForMediaOnly(t *testing.T) {
	nodes := buildCustomForwardNodes([]OutgoingMessage{
		{ForwardName: "", ImageURLs: []string{"https://example.com/a.jpg"}},
	}, "", "10001")
	if len(nodes) == 0 {
		t.Skip("没有构造出节点")
	}
	// 昵称退回默认的 Diana，正文为空——只有机器人自己的名字不构成要审的内容。
	text := forwardNodeAuditText(nodes)
	if strings.Contains(text, "http") {
		t.Fatalf("图片地址不该进审核文本：%s", text)
	}
}

// 按 message_id 引用的节点（sendForwardMessageIDNodes 造的）没有 data.content，
// 抽取不能因此崩掉，也不该编出文字来。
func TestForwardNodeAuditTextHandlesMessageIDNodes(t *testing.T) {
	nodes := []map[string]any{
		{"type": "node", "data": map[string]any{"id": "12345678"}},
	}
	if text := forwardNodeAuditText(nodes); text != "" {
		t.Fatalf("引用式节点没有可审文字，实际拿到 %q", text)
	}
}

// content 从 JSON 里回来时是 []any 而不是 []map[string]any，两种都要认。
func TestForwardNodeAuditTextAcceptsGenericContentSlice(t *testing.T) {
	nodes := []map[string]any{
		{"type": "node", "data": map[string]any{
			"name": "路人",
			"content": []any{
				map[string]any{"type": "text", "data": map[string]any{"text": "站外正文"}},
				map[string]any{"type": "image", "data": map[string]any{"file": "a.jpg"}},
			},
		}},
	}
	text := forwardNodeAuditText(nodes)
	if !strings.Contains(text, "路人") || !strings.Contains(text, "站外正文") {
		t.Fatalf("[]any 形式的 content 没抽出来：%q", text)
	}
	if strings.Contains(text, "a.jpg") {
		t.Fatalf("图片文件名不该进审核文本：%q", text)
	}
}

// 没有内容就直接返回，不碰模型——nil runtime 都不该崩。
func TestAuditForwardNodesSafetySkipsEmpty(t *testing.T) {
	runtime := carryoverRuntime()
	if err := runtime.auditForwardNodesSafety(t.Context(), MessageEvent{}, nil); err != nil {
		t.Fatalf("空节点不该报错：%v", err)
	}
	nodes := []map[string]any{{"type": "node", "data": map[string]any{"id": "12345678"}}}
	if err := runtime.auditForwardNodesSafety(t.Context(), MessageEvent{}, nodes); err != nil {
		t.Fatalf("无文字节点不该报错：%v", err)
	}
}

// 卡片审出问题就不发。这条走的是真的发送入口，不是直接调审核函数——
// 原来的缺口正是「审核函数本身没错，只是这条路上没人调它」。
func TestSendForwardNodesBlocksUnsafeCard(t *testing.T) {
	provider := &qualityTestProvider{reply: `{"should_send":true,"confidence":0.99,"reason":"ok","account_safe":false,"account_risk":"politics"}`}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	event := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, GroupID: "100200301", UserID: "100200711"}
	nodes := buildCustomForwardNodes([]OutgoingMessage{
		{ForwardName: "8964天安门椒盐菠萝", Text: "站外正文"},
	}, "Diana", "42")

	_, err := runtime.sendForwardNodesWithResult(t.Context(), event, nodes)
	if err == nil {
		t.Fatal("审出涉政内容的卡片不该发出去")
	}
	if len(provider.requests) != 1 {
		t.Fatalf("同一张卡片只该审一次，实际 %d 次", len(provider.requests))
	}
	if sent := channel.sentSnapshot(); len(sent) != 0 {
		t.Fatalf("被拦下的卡片不该有任何投递：%#v", sent)
	}
}

// 审核挂掉时放行，和 auditReplyAccountSafety 的既有取舍一致。
func TestSendForwardNodesFailsOpenWhenAuditBreaks(t *testing.T) {
	provider := &qualityTestProvider{reply: "not json at all"}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	event := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, GroupID: "100200301", UserID: "100200711"}
	nodes := buildCustomForwardNodes([]OutgoingMessage{{ForwardName: "路人", Text: "站外正文"}}, "Diana", "42")

	if err := runtime.auditForwardNodesSafety(t.Context(), event, nodes); err != nil {
		t.Fatalf("审核读不出结果时该放行：%v", err)
	}
}
