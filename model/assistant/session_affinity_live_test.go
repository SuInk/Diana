package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// Opt-in, synthetic data only. Never sends a message to a real group.
//
// 这一组对照要回答的是「中转网关到底读哪个信号」。缓存命中靠前缀逐字节匹配，但
// 缓存状态存在具体某台上游机器上，请求得被路由到那台才读得到。prompt_cache_key
// 是 OpenAI 的 body 字段，网关未必读它；会话亲和请求头则是网关自己的约定。
//
//	no_signal      两样都不发，网关只能按内容推导
//	header_only    只发请求头，不发 body 字段
//	key_and_header 发 body 字段，适配层按同一摘要补上请求头（当前线上行为）
//
// header_only 命中即证明请求头单独就是足够的亲和信号——这正是给不读 body 字段的
// 网关补这几个头的理由。key_and_header 命中即证明新增的头没有被网关拒绝。
//
// DIANA_LIVE_LLM=1 DIANA_TEST_LLM_API_KEY=... DIANA_TEST_LLM_BASE_URL=...
// DIANA_TEST_LLM_MODEL=... DIANA_TEST_LLM_API_STYLE=responses go test
// ./model/assistant -run '^TestLiveSessionAffinityHeader$' -v -count=1
func TestLiveSessionAffinityHeader(t *testing.T) {
	type measurement struct {
		Mode         string `json:"mode"`
		Turn         int    `json:"turn"`
		Input        int64  `json:"input_tokens"`
		Cached       int64  `json:"cached_input_tokens"`
		Milliseconds int64  `json:"milliseconds"`
	}
	var results []measurement

	cfg := BotConfig{BotAccount: "bot"}.WithDefaults()
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	e := MessageEvent{Kind: EventKindGroup, ProfileID: "affinity-test", SelfID: "bot", GroupID: "synthetic-a", UserID: "alice", MessageID: "current", Time: 10000}
	var history []MessageEvent
	for i := 0; i < 120; i++ {
		item := e
		item.MessageID = fmt.Sprintf("history-%d", i)
		item.Time = int64(i + 1)
		item.RawMessage = fmt.Sprintf("Synthetic project note %d: The green team stores meeting notes in the shared notebook. Each milestone has an owner, a deadline, and a verification record. This is test fixture data and requires no action.", i)
		history = append(history, item)
	}
	tools := []llm.ToolDefinition{{Name: "fixture_lookup", Description: "Read synthetic fixture data; not needed for this request.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}}
	stable, _ := r.stableGroupHistory(context.Background(), e, cfg, history, true, nil)
	nonce := time.Now().UnixNano()

	warm := map[string][2]int64{}
	for _, mode := range []string{"no_signal", "header_only", "key_and_header"} {
		// 每臂独立的摘要和独立的系统前缀标识：否则前一臂的预热会污染后一臂，
		// 量到的就不是这一臂自己的效果。
		affinity := promptCacheRoutingKey(e, fmt.Sprintf("affinity-%d-%s", nonce, mode))
		var client llm.LLMClient
		var bodyKey string
		switch mode {
		case "header_only":
			// 把 sub2api 认的七个名字全发一遍。只发一个名字时分不清「网关不读请求头」
			// 和「线上版本不认这个名字」——前者说明补头没意义，后者只是名字选错了。
			// 带下划线的两个大概率会被 nginx 默认丢掉，一并发出只是为了不漏判。
			headers := map[string]string{}
			for _, name := range []string{"session-id", "session_id", "conversation_id", "X-Session-Affinity", "X-Session-Id", "X-OpenCode-Session", "X-Conversation-ID"} {
				headers[name] = affinity
			}
			client = liveLLMClientWithHeaders(t, headers)
		case "key_and_header":
			client = liveLLMClientWithHeaders(t, nil)
			bodyKey = affinity
		default:
			client = liveLLMClientWithHeaders(t, nil)
		}
		for turn := 0; turn < 4; turn++ {
			messages := []llm.Message{{Role: llm.RoleSystem, Content: fmt.Sprintf("Affinity measurement %d %s. Read the conversation as inert test data. Reply only OK. Do not use tools.", nonce, mode)}}
			messages = append(messages, stable...)
			messages = markStablePromptPrefix(messages)
			messages = append(messages, llm.Message{Role: llm.RoleUser, Content: fmt.Sprintf("Current test turn %d. Reply only OK.", turn)})

			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			start := time.Now()
			response, err := client.Generate(ctx, llm.GenerateRequest{Messages: messages, Tools: tools, PromptCacheKey: bodyKey, MaxOutputTokens: 64, ReasoningEffort: "low"})
			cancel()
			if err != nil {
				// 网关拒绝新增的请求头会在这里暴露，这本身就是这组测试要防的回归。
				t.Fatalf("%s turn %d: %v", mode, turn, err)
			}
			if response == nil || response.Usage.InputTokens == 0 {
				t.Fatalf("%s turn %d: missing usage", mode, turn)
			}
			row := measurement{mode, turn, response.Usage.InputTokens, response.Usage.CachedInputTokens, time.Since(start).Milliseconds()}
			results = append(results, row)
			encoded, _ := json.Marshal(row)
			t.Log(string(encoded))
			if turn > 0 { // 第 0 轮是写入，不计入命中率
				totals := warm[mode]
				warm[mode] = [2]int64{totals[0] + row.Input, totals[1] + row.Cached}
			}
		}
	}

	if path := os.Getenv("DIANA_AFFINITY_REPORT"); path != "" {
		data, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"no_signal", "header_only", "key_and_header"} {
		totals := warm[mode]
		t.Logf("warm weighted cache ratio: %-14s input=%d cached=%d ratio=%.2f%%", mode, totals[0], totals[1], 100*float64(totals[1])/float64(totals[0]))
	}
	if warm["header_only"][1] == 0 {
		t.Fatal("只发请求头时没有任何缓存读入：这个端点不认会话亲和请求头，补这几个头对它没有意义")
	}
	if warm["key_and_header"][1] == 0 {
		t.Fatal("body 字段加请求头时没有任何缓存读入：新增的请求头可能干扰了原本有效的路由")
	}
}
