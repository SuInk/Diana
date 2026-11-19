// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// Replays private production requests against the current model. Tool calls are
// recorded only: this test never dispatches tools or sends a chat message.
func TestLivePiEnvironmentReplay(t *testing.T) {
	path := os.Getenv("DIANA_PI_REPLAY_SAMPLE")
	if path == "" {
		t.Skip("set DIANA_PI_REPLAY_SAMPLE to a private production sample")
	}
	client := liveAgentClient(t)
	var sample struct {
		Request     llm.GenerateRequest `json:"request"`
		Environment string              `json:"environment"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &sample); err != nil {
		t.Fatal(err)
	}
	if sample.Environment == "" {
		t.Fatal("missing environment")
	}
	last := -1
	for i, m := range sample.Request.Messages {
		if m.Role == llm.RoleSystem && m.Content == sample.Environment {
			last = i
		}
	}
	if last < 0 {
		t.Fatal("environment not present in production request")
	}
	env := llm.Message{Role: llm.RoleSystem, Content: speakerToolPromptMarker + "\n" + sample.Environment}
	// The first request is identical in both arms. The second appends a fresh
	// user turn and either repeats the environment (old) or uses the real filter.
	base := sample.Request
	base.Messages = append([]llm.Message(nil), base.Messages...)
	base.Messages[last] = env
	base.MaxOutputTokens = 1024
	type result struct {
		Arm        string                `json:"arm"`
		Round      int                   `json:"round"`
		DurationMS int64                 `json:"duration_ms"`
		Response   *llm.GenerateResponse `json:"response"`
	}
	var results []result
	for _, optimized := range []bool{false, true} {
		arm := "baseline"
		if optimized {
			arm = "optimized"
		}
		req := base
		req.PromptCacheKey = "diana-pi-replay-" + arm + "-" + time.Now().Format("20060102150405")
		for round := 0; round < 3; round++ {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			started := time.Now()
			resp, err := client.Generate(ctx, req)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			results = append(results, result{arm, round, time.Since(started).Milliseconds(), resp})
			t.Logf("arm=%s round=%d model=%s elapsed_ms=%d input=%d cached=%d output=%d calls=%d", arm, round, resp.Model, results[len(results)-1].DurationMS, resp.Usage.InputTokens, resp.Usage.CachedInputTokens, resp.Usage.OutputTokens, len(resp.ToolCalls))
			if strings.TrimSpace(resp.Text) == "" && len(resp.ToolCalls) == 0 {
				t.Fatal("empty response")
			}
			if round > 0 {
				for _, call := range resp.ToolCalls {
					if call.Name != finalizeToolName || sendText(call) == "" {
						t.Fatalf("summary-only follow-up must answer without extra tool work: %s", call.Name)
					}
				}
			}
			prefix := transcriptAfter(req, resp, resp.Text)
			appended := []llm.Message{{Role: llm.RoleUser, Content: "请用一句话概括你刚才的回答，不执行其他操作。"}}
			volatile := []llm.Message{env}
			if optimized {
				volatile = continuationVolatile(prefix, appended, volatile)
			}
			req.Messages = append(prefix, volatile...)
			req.Messages = append(req.Messages, appended...)
			time.Sleep(2 * time.Second)
		}
	}
	if path := os.Getenv("DIANA_PI_REPLAY_RESULTS"); path != "" {
		raw, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
