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

	"github.com/SuInk/diana/model/llm"
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

// 查过但没查到，要说「查了没有」，不能说成「我看不到」。这条规则对谁都一样，进稳定提示词。
func TestSystemPromptRequiresHonestLookupResults(t *testing.T) {
	runtime := NewRuntime(BotConfig{ID: "qq"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	prompt := runtime.systemPrompt(MessageEvent{Kind: EventKindGroup, GroupID: "1", UserID: "2"}, nil)
	if !strings.Contains(prompt, promptToolFindings) {
		t.Fatal("系统提示词里没有「查过就说查过」这条规则")
	}
	for _, want := range []string{"不要说成自己看不到", "说清手里有什么、缺什么"} {
		if !strings.Contains(promptToolFindings, want) {
			t.Errorf("规则缺少关键要求：%s", want)
		}
	}
}

// 真实模型回放线上那一轮：工具查到了撤回记录，但里面没有对方要的那一条。
// 正确的回答是说清查过、范围到哪、没找到；不能回一句「撤那么快谁看得到呀」。
func TestLivePromptSaysItLookedInsteadOfClaimingBlindness(t *testing.T) {
	client := liveLLMClient(t)
	systemPrompt := defaultSystemPrompt + "\n你是然然，一只软萌爱撒娇的猫娘，对群友热情亲近，说话轻快活泼。" +
		"\n" + promptToolFindings + "\n" + ReplyStyleHuman.prompt(true, personaVoiceFrom("然然", "喵")) +
		"\n" + ReplyStyleHuman.closingAnchor()
	history := []llm.Message{
		{Role: llm.RoleUser, Content: "阿王：你之前撤回了什么"},
		{Role: llm.RoleUser, Content: "小林：忘了，你回答一下"},
		// 线上那次工具就是这样返回的：只给了一天前的旧记录，没有任何一句说明缺了今天的。
		{Role: llm.RoleUser, Content: `【工具结果 chat_history/recalls】{"action":"recalls","returned_count":19,"total":40,"truncated":true,` +
			`"items":[{"local_time":"昨天 20:10","sender":"小林","text":"明天再说吧"},{"local_time":"昨天 20:11","sender":"阿王","text":"行"},` +
			`{"local_time":"昨天 21:03","sender":"小林","text":"我先睡了"}]}`},
	}
	replies := make([]string, 0, livePromptSamples)
	for i := 0; i < livePromptSamples; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		resp, err := client.Generate(ctx, llm.GenerateRequest{Messages: append([]llm.Message{{Role: llm.RoleSystem, Content: systemPrompt}}, history...)})
		cancel()
		if err != nil {
			t.Fatalf("第 %d 次采样失败: %v", i+1, err)
		}
		replies = append(replies, strings.TrimSpace(resp.Text))
	}
	blind, honest := 0, 0
	for i, reply := range replies {
		if claimsBlindness(reply) {
			blind++
			t.Logf("第 %d 条谎称看不到：%q", i+1, reply)
		}
		if mentionsLookup(reply) {
			honest++
		}
	}
	t.Logf("谎称看不到 %d/%d，说明查过 %d/%d", blind, len(replies), honest, len(replies))
	if blind > 1 {
		t.Errorf("仍在把没查到说成看不到：%d/%d", blind, len(replies))
	}
	if honest < len(replies)-1 {
		t.Errorf("没有说清查过和缺口：%d/%d", honest, len(replies))
	}
}

// claimsBlindness 判断这条回复有没有谎称自己看不到撤回。
func claimsBlindness(text string) bool {
	for _, phrase := range []string{"谁看得到", "看不到", "看不见", "没法看", "查不了", "我又不能看", "没有这个功能", "撤得太快"} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

// mentionsLookup 判断这条回复有没有交代「查过、查到什么范围」。
func mentionsLookup(text string) bool {
	for _, phrase := range []string{"查", "翻", "记录里", "只到", "没找到", "没有找到", "没列", "昨天"} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}
