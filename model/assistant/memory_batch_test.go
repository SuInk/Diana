// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func batchMemoryRuntime(t *testing.T, reply string) (*Runtime, *testStructuredMemoryStore, *capturingLLMProvider) {
	t.Helper()
	memory := &testStructuredMemoryStore{}
	provider := &capturingLLMProvider{reply: reply}
	runtime := NewRuntime(BotConfig{BotAccount: "bot"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.SetStructuredMemoryStore(memory)
	return runtime, memory, provider
}

func batchPayload(messageID, text string, at int64) MemoryJobPayload {
	return MemoryJobPayload{Kind: MemoryJobEvent, Session: "group:123", Event: MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "123",
		UserID:     "user",
		SenderName: "Alice",
		MessageID:  messageID,
		Time:       at,
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}}
}

// 攒批的意义在于一次调用覆盖多条消息：三条消息只能产生一次 LLM 请求，
// 固定前缀和 existing_memories 才不会被重复付费三遍。
func TestMemoryGateBatchesSeveralMessagesIntoOneCall(t *testing.T) {
	reply := `{"memories":[
		{"action":"upsert","key":"profile.pet.cat","kind":"fact","topic":"宠物","content":"Alice养了一只猫","evidence":"我养了只猫","source_type":"explicit","confidence":0.95,"importance":0.6,"visibility":"session","sensitive":false,"source_index":0},
		{"action":"upsert","key":"preference.food.spicy","kind":"preference","topic":"饮食偏好","content":"Alice不吃辣","evidence":"我不吃辣","source_type":"explicit","confidence":0.95,"importance":0.6,"visibility":"session","sensitive":false,"source_index":2}
	]}`
	runtime, memory, provider := batchMemoryRuntime(t, reply)
	payloads := []MemoryJobPayload{
		batchPayload("m1", "我养了只猫", 100),
		batchPayload("m2", "今天下雨了", 200),
		batchPayload("m3", "我不吃辣", 300),
	}
	if err := runtime.processEventMemoryJobs(context.Background(), memory, payloads); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 {
		t.Fatalf("LLM 调用次数 = %d，攒批应该只有 1 次", provider.calls)
	}
	user := ""
	for _, message := range provider.request.Messages {
		if message.Role == llm.RoleUser {
			user = message.Content
		}
	}
	if !strings.Contains(user, `"current_batch"`) || strings.Contains(user, `"current":{`) {
		t.Fatalf("批量提示词应只给 current_batch：%q", user[:min(len(user), 400)])
	}
	for _, text := range []string{"我养了只猫", "今天下雨了", "我不吃辣"} {
		if !strings.Contains(user, text) {
			t.Fatalf("批次里缺了 %q", text)
		}
	}
	// 候选按 source_index 回到各自的来源消息，出处不能张冠李戴。
	if len(memory.applied) != 2 {
		t.Fatalf("applied = %#v", memory.applied)
	}
	bySource := map[string]string{}
	for _, request := range memory.applied {
		if len(request.Candidates) != 1 {
			t.Fatalf("request = %#v", request)
		}
		bySource[request.SourceMessageID] = request.Candidates[0].Key
	}
	if bySource["m1"] != "profile.pet.cat" || bySource["m3"] != "preference.food.spicy" || len(bySource) != 2 {
		t.Fatalf("来源归属错了：%#v", bySource)
	}
}

// source_index 缺失或越界时归到最新那条，不能丢候选。
func TestMemoryGateBatchFallsBackToNewestSource(t *testing.T) {
	reply := `{"memories":[
		{"action":"upsert","key":"profile.city","kind":"fact","topic":"居住地","content":"Alice住在杭州","evidence":"我在杭州","source_type":"explicit","confidence":0.95,"importance":0.6,"visibility":"session","sensitive":false},
		{"action":"upsert","key":"profile.job","kind":"fact","topic":"职业","content":"Alice在写后端","evidence":"我写后端","source_type":"explicit","confidence":0.95,"importance":0.6,"visibility":"session","sensitive":false,"source_index":9}
	]}`
	runtime, memory, _ := batchMemoryRuntime(t, reply)
	payloads := []MemoryJobPayload{batchPayload("m1", "我在杭州", 100), batchPayload("m2", "我写后端", 200)}
	if err := runtime.processEventMemoryJobs(context.Background(), memory, payloads); err != nil {
		t.Fatal(err)
	}
	if len(memory.applied) != 1 || memory.applied[0].SourceMessageID != "m2" || len(memory.applied[0].Candidates) != 2 {
		t.Fatalf("applied = %#v", memory.applied)
	}
}

// 单条时的提示词形状必须保持不变：还是 current，没有 current_batch。
func TestMemoryGateSingleMessageKeepsCurrentShape(t *testing.T) {
	runtime, memory, provider := batchMemoryRuntime(t, `{"memories":[]}`)
	if err := runtime.processEventMemoryJobs(context.Background(), memory, []MemoryJobPayload{batchPayload("m1", "我养了只猫", 100)}); err != nil {
		t.Fatal(err)
	}
	user := ""
	for _, message := range provider.request.Messages {
		if message.Role == llm.RoleUser {
			user = message.Content
		}
	}
	if strings.Contains(user, "current_batch") {
		t.Fatalf("单条不该走批量形状：%q", user)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(user[strings.Index(user, "{"):]), &payload); err != nil {
		t.Fatalf("上下文不是合法 JSON：%v", err)
	}
	if _, ok := payload["current"]; !ok {
		t.Fatalf("单条必须给 current：%v", payload)
	}
}
