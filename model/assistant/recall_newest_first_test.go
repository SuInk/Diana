// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// 线上 2026-09-16 19:13：群友问「他刚才撤回了什么」，机器人调了 recalls，
// 拿回来的却是一天前的旧记录——当天 40 条撤回按时间升序排，输出预算从尾部裁，
// 刚刚发生的那 21 条全被裁掉了。于是她只能回一句「撤那么快谁看得到呀」。
func TestChatHistoryRecallsKeepsNewestWhenBudgetClips(t *testing.T) {
	history := NewMessageHistoryPlugin()
	now := time.Now().Unix()
	const total = 40
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("msg-%02d", i)
		// 越靠后越新：第 0 条是 20 小时前，最后一条是刚刚。
		at := now - int64((total-i)*30*60)
		text := fmt.Sprintf("第%02d条被撤回的内容，这里写长一点好把输出预算撑满：%s", i, strings.Repeat("内容", 60))
		history.Observe(context.Background(), MessageEvent{
			Kind: EventKindGroup, GroupID: "123", UserID: "20002", MessageID: id, Time: at,
			RawMessage: text, SenderName: "Alice",
			Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
		})
		history.Observe(context.Background(), messageEventFromEnvelope(oneBotEnvelope{
			PostType: "notice", NoticeType: "group_recall", GroupID: "123", UserID: "20002",
			MessageID: id, Time: at + 1,
		}))
	}
	runtime := NewRuntime(BotConfig{}.WithDefaults(), nilChannel{}, NewPluginManager(history), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10001", MessageID: "query-1", Time: now}

	raw, err := newDianaChatHistoryTool(runtime, event).withRecallSink(&recallDisclosureSink{}).
		Run(context.Background(), map[string]any{"operation": "recalls"})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Message string `json:"message"`
		Total   int    `json:"total"`
		Items   []struct {
			MessageID string `json:"message_id"`
			Text      string `json:"text"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("解析失败：%v\n%s", err, raw)
	}
	if len(result.Items) == 0 {
		t.Fatal("一条都没返回")
	}
	// 最新的那条必须在，而且排在最前面。
	if result.Items[0].MessageID != fmt.Sprintf("msg-%02d", total-1) {
		t.Fatalf("第一条不是最新的撤回：%s", result.Items[0].MessageID)
	}
	// 被裁掉的应当是更早的，不能出现「只剩最旧几条」。
	if strings.Contains(raw, "第00条") && !strings.Contains(raw, fmt.Sprintf("第%02d条", total-1)) {
		t.Fatal("裁掉的是最新的记录")
	}
	if result.Total != total {
		t.Fatalf("total = %d，应当是全部 %d 条", result.Total, total)
	}
	if len(result.Items) < total && !strings.Contains(result.Message, "更早的没有列出") {
		t.Fatalf("没有说明还有更早的记录：%s", result.Message)
	}
}

// limit 以前被忽略：模型想多要几条，回来的还是同一批，remaining 永远不减。
func TestChatHistoryRecallsHonorsLimit(t *testing.T) {
	history := NewMessageHistoryPlugin()
	now := time.Now().Unix()
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("m-%d", i)
		at := now - int64((10-i)*60)
		history.Observe(context.Background(), MessageEvent{
			Kind: EventKindGroup, GroupID: "123", UserID: "20002", MessageID: id, Time: at,
			RawMessage: fmt.Sprintf("第%d条", i), SenderName: "Alice",
			Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": fmt.Sprintf("第%d条", i)}}},
		})
		history.Observe(context.Background(), messageEventFromEnvelope(oneBotEnvelope{
			PostType: "notice", NoticeType: "group_recall", GroupID: "123", UserID: "20002",
			MessageID: id, Time: at + 1,
		}))
	}
	runtime := NewRuntime(BotConfig{}.WithDefaults(), nilChannel{}, NewPluginManager(history), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10001", MessageID: "q", Time: now}
	raw, err := newDianaChatHistoryTool(runtime, event).withRecallSink(&recallDisclosureSink{}).
		Run(context.Background(), map[string]any{"operation": "recalls", "limit": 3})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Items []struct {
			Text string `json:"text"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 3 {
		t.Fatalf("limit=3 却返回 %d 条", len(result.Items))
	}
	if !strings.Contains(result.Items[0].Text, "第9条") {
		t.Fatalf("limit 之后仍要最新优先：%s", result.Items[0].Text)
	}
}
