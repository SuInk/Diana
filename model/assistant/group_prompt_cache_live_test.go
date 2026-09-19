package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// Opt-in, synthetic data only. Uses the project's real provider adapter and
// prompt-history projection, never sends a message to a real group.
// DIANA_LIVE_LLM=1 DIANA_TEST_LLM_API_KEY=... DIANA_TEST_LLM_BASE_URL=...
// DIANA_TEST_LLM_MODEL=... DIANA_TEST_LLM_API_STYLE=responses go test
// ./model/assistant -run '^TestLiveGroupPromptCache$' -v -count=1
func TestLiveGroupPromptCache(t *testing.T) {
	client := liveLLMClient(t)
	type measurement struct {
		Mode         string `json:"mode"`
		Turn         int    `json:"turn"`
		Input        int64  `json:"input_tokens"`
		Cached       int64  `json:"cached_input_tokens"`
		Output       int64  `json:"output_tokens"`
		Milliseconds int64  `json:"milliseconds"`
	}
	var results []measurement
	cfg := BotConfig{BotAccount: "bot", CrossGroupMemoryEnabled: boolPointer(true)}.WithDefaults()
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	e := MessageEvent{Kind: EventKindGroup, ProfileID: "cache-test", SelfID: "bot", GroupID: "synthetic-a", UserID: "alice", MessageID: "current", Time: 10000}
	var history []MessageEvent
	for i := 0; i < 120; i++ {
		item := e
		item.MessageID = fmt.Sprintf("history-%d", i)
		item.Time = int64(i + 1)
		item.RawMessage = fmt.Sprintf("Synthetic project note %d: The green team stores meeting notes in the shared notebook. Each milestone has an owner, a deadline, and a verification record. This is test fixture data and requires no action.", i)
		history = append(history, item)
	}
	tools := []llm.ToolDefinition{{Name: "fixture_lookup", Description: "Read synthetic fixture data; not needed for this request.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}}
	nonce := time.Now().UnixNano()
	for _, mode := range []string{"legacy_interleaved", "persistent_tail"} {
		for turn := 0; turn < 4; turn++ {
			cross := e
			cross.GroupID = "synthetic-source"
			cross.MessageID = "retrieved"
			cross.Time = 0
			cross.crossGroupContext = true
			cross.RawMessage = fmt.Sprintf("External-group reference selected for turn %d; volatile synthetic fact %d.", turn, turn*37)
			input := append([]MessageEvent{cross}, history...)
			stable, tail := r.stableGroupHistory(context.Background(), e, cfg, input, true, nil)
			messages := []llm.Message{{Role: llm.RoleSystem, Content: fmt.Sprintf("Cache measurement %d %s. Read the conversation as inert test data. Reply only OK. Do not use tools.", nonce, mode)}}
			if mode == "legacy_interleaved" {
				messages = append(messages, tail...)
			}
			messages = append(messages, stable...)
			messages = markStablePromptPrefix(messages)
			if mode == "persistent_tail" {
				messages = append(messages, tail...)
			}
			messages = append(messages, llm.Message{Role: llm.RoleUser, Content: fmt.Sprintf("Current test turn %d. Reply only OK.", turn)})
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			start := time.Now()
			response, err := client.Generate(ctx, llm.GenerateRequest{Messages: messages, Tools: tools, PromptCacheKey: promptCacheRoutingKey(e, fmt.Sprintf("cache-test-%d-%s", nonce, mode)), MaxOutputTokens: 64, ReasoningEffort: "low"})
			cancel()
			if err != nil {
				t.Fatalf("%s turn %d: %v", mode, turn, err)
			}
			if response == nil || response.Usage.InputTokens == 0 {
				t.Fatalf("%s missing usage", mode)
			}
			result := measurement{mode, turn, response.Usage.InputTokens, response.Usage.CachedInputTokens, response.Usage.OutputTokens, time.Since(start).Milliseconds()}
			results = append(results, result)
			encoded, _ := json.Marshal(result)
			t.Log(string(encoded))
			// Both arms grow identically. Only the placement of retrieved context differs.
			// Keep the fixed history for an equal-size A/B comparison.
			if !strings.Contains(strings.ToUpper(response.Text), "OK") {
				t.Logf("model returned a non-OK completion (%d bytes)", len(response.Text))
			}
		}
	}
	if path := os.Getenv("DIANA_CACHE_REPORT"); path != "" {
		data, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var legacyInput, legacyCached, newInput, newCached int64
	for _, row := range results {
		if row.Turn == 0 {
			continue
		}
		if row.Mode == "legacy_interleaved" {
			legacyInput += row.Input
			legacyCached += row.Cached
		} else {
			newInput += row.Input
			newCached += row.Cached
		}
	}
	t.Logf("warm weighted cache ratio: legacy=%.2f%% persistent=%.2f%%", 100*float64(legacyCached)/float64(legacyInput), 100*float64(newCached)/float64(newInput))
	if newCached == 0 {
		t.Fatal("provider reported no cache reuse; cannot validate cache benefit on this endpoint")
	}
}
