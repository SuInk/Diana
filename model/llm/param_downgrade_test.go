// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 结论不能永久钉死：过期之后按未知处理，强制工具会重新发出去，网关升级支持了就
// 能自己恢复。
func TestDowngradeMemoExpires(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		requests = append(requests, body)
		w.Header().Set("Content-Type", "application/json")
		if body["tool_choice"] != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Thinking mode does not support this tool_choice"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"c","model":"test","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	now := time.Now()
	restore := stubDowngradeMemoClock(func() time.Time { return now })
	defer restore()

	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "k", BaseURL: server.URL + "/v1", APIFormat: APIFormatChatCompletions, Model: "thinking"}
	request := GenerateRequest{
		Messages:   []Message{{Role: RoleUser, Content: "hi"}},
		ToolChoice: "agent_finalize",
		Tools:      []ToolDefinition{{Name: "agent_finalize", Parameters: map[string]any{"type": "object"}}},
	}
	if _, err := newOpenAICompatibleClient(cfg, server.Client()).Generate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests=%d, want the rejection plus the downgraded retry", len(requests))
	}

	// 保质期内：直接按降级后的请求发。
	now = now.Add(DowngradeMemoTTL - time.Minute)
	if _, err := newOpenAICompatibleClient(cfg, server.Client()).Generate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 || requests[2]["tool_choice"] != nil {
		t.Fatalf("a fresh conclusion was ignored: requests=%d", len(requests))
	}

	// 过期之后：重新试一次强制工具。
	now = now.Add(2 * time.Minute)
	if _, err := newOpenAICompatibleClient(cfg, server.Client()).Generate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 5 || requests[3]["tool_choice"] == nil {
		t.Fatalf("an expired conclusion was still in force: requests=%d", len(requests))
	}
}

// 落盘往返：导出的结论装回来之后照样生效，过期的记录直接丢掉。
func TestDowngradeRecordsRoundTrip(t *testing.T) {
	now := time.Now()
	restore := stubDowngradeMemoClock(func() time.Time { return now })
	defer restore()

	key := "openai_compatible|https://gw.example/v1|thinking"
	rememberedDowngrades.remember(key, map[string]bool{downgradeFieldToolChoice: true})
	records := DowngradeRecords()
	found := false
	for _, record := range records {
		if record.Key == key && record.Field == downgradeFieldToolChoice {
			found = true
		}
	}
	if !found {
		t.Fatalf("the conclusion was not exported: %#v", records)
	}

	rememberedDowngrades.forget(key, downgradeFieldToolChoice)
	if rememberedDowngrades.seen(key, downgradeFieldToolChoice) {
		t.Fatal("forget did not drop the conclusion")
	}
	RestoreDowngradeRecords(records)
	if !rememberedDowngrades.seen(key, downgradeFieldToolChoice) {
		t.Fatal("the restored conclusion is not in force")
	}

	rememberedDowngrades.forget(key, downgradeFieldToolChoice)
	stale := []DowngradeRecord{{Key: key, Field: downgradeFieldToolChoice, LearnedAt: now.Add(-DowngradeMemoTTL - time.Minute)}}
	RestoreDowngradeRecords(stale)
	if rememberedDowngrades.seen(key, downgradeFieldToolChoice) {
		t.Fatal("an expired record was restored")
	}
}

// 探测必须绕开已经记住的结论，否则结论再没有翻身的机会：网关改好了也测不出来。
func TestProbeForcedToolChoiceBypassesAndClearsMemo(t *testing.T) {
	accept := false
	var sawToolChoice []bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		sawToolChoice = append(sawToolChoice, body["tool_choice"] != nil)
		w.Header().Set("Content-Type", "application/json")
		if body["tool_choice"] != nil && !accept {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Thinking mode does not support this tool_choice"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"c","model":"test","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	now := time.Now()
	restore := stubDowngradeMemoClock(func() time.Time { return now })
	defer restore()

	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "k", BaseURL: server.URL + "/v1", APIFormat: APIFormatChatCompletions, Model: "thinking"}
	client := newOpenAICompatibleClient(cfg, server.Client())
	key := downgradeMemoKey(cfg, cfg.Model)

	if _, err := client.ProbeForcedToolChoice(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !rememberedDowngrades.seen(key, downgradeFieldToolChoice) {
		t.Fatal("the probe did not record the rejection")
	}

	// 网关改好了：探测仍然带着 tool_choice 去问，并把旧结论抹掉。
	accept = true
	sawToolChoice = nil
	if _, err := client.ProbeForcedToolChoice(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sawToolChoice) != 1 || !sawToolChoice[0] {
		t.Fatalf("the probe reused the remembered downgrade: %#v", sawToolChoice)
	}
	if rememberedDowngrades.seen(key, downgradeFieldToolChoice) {
		t.Fatal("the stale conclusion survived a successful probe")
	}
}

// 与字段无关的失败（鉴权等）不该动结论。
func TestProbeForcedToolChoiceKeepsMemoOnUnrelatedFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer server.Close()

	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "k", BaseURL: server.URL + "/v1", APIFormat: APIFormatChatCompletions, Model: "thinking"}
	client := newOpenAICompatibleClient(cfg, server.Client())
	if _, err := client.ProbeForcedToolChoice(context.Background()); err == nil {
		t.Fatal("ProbeForcedToolChoice error = nil, want the auth failure")
	}
	if rememberedDowngrades.seen(downgradeMemoKey(cfg, cfg.Model), downgradeFieldToolChoice) {
		t.Fatal("an unrelated failure was recorded as a rejection")
	}
}

// stubDowngradeMemoClock 拨快记忆的表，并在结束时清干净，免得污染其他用例。
func stubDowngradeMemoClock(now func() time.Time) func() {
	rememberedDowngrades.mu.Lock()
	previous := rememberedDowngrades.now
	rememberedDowngrades.now = now
	rememberedDowngrades.mu.Unlock()
	return func() {
		rememberedDowngrades.mu.Lock()
		rememberedDowngrades.now = previous
		rememberedDowngrades.mu.Unlock()
	}
}
