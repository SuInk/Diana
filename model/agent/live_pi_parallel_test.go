// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// Pin the production three-call envelope, then let the current model handle
// actual browser results and any unexecuted calls. No chat channel is attached.
type piParallelReplayClient struct {
	llm.LLMClient
	first    []llm.ToolCall
	requests int
}

func (c *piParallelReplayClient) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.requests++
	if c.requests == 1 {
		return &llm.GenerateResponse{ToolCalls: c.first}, nil
	}
	return c.LLMClient.Generate(ctx, req)
}

func TestLivePiParallelReplay(t *testing.T) {
	if os.Getenv("DIANA_PI_PARALLEL_REPLAY") != "1" {
		t.Skip("explicit live browser and model replay only")
	}
	urls := []string{
		"https://daily.dev/posts/day-6-reset-hj5q4m3rz",
		"https://daily.dev/posts/never-slept-better-and-feeling-reseted-brand-new-me-and-brand-new-usage-for-all-chatgpt-work-and-co-k949hlchn",
		"https://daily.dev/posts/this-celebration-is-moved-to-tomorrow-as-the-button-was-already-pressed-today--irijyduoe",
	}
	client := &piParallelReplayClient{LLMClient: liveAgentClient(t)}
	for i, url := range urls {
		client.first = append(client.first, llm.ToolCall{ID: fmt.Sprintf("replay-%d", i), Name: ToolsExecuteToolName, Arguments: map[string]any{"name": browserRenderToolName, "input": map[string]any{"url": url}}})
	}
	tool := NewBrowserRenderTool(nil)
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), CoreTools: []string{"common"}, MaxSteps: 8, ToolTimeoutMS: 60000}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	var mu sync.Mutex
	seen := map[string]bool{}
	var observed []Step
	hooks := RunHooks{
		BeforeToolCall: func(_ context.Context, call ToolCallContext) error {
			if call.Tool != browserRenderToolName {
				return fmt.Errorf("replay only permits browser_render")
			}
			allowed := func(url string) bool {
				for _, want := range urls {
					if url == want {
						return true
					}
				}
				return false
			}
			if !allowed(stringFromInput(call.Input, "url")) {
				return fmt.Errorf("URL outside replay scope")
			}
			if extra, ok := call.Input["urls"]; ok {
				raw, _ := json.Marshal(extra)
				var values []string
				if json.Unmarshal(raw, &values) != nil {
					return fmt.Errorf("invalid URL list")
				}
				for _, url := range values {
					if !allowed(url) {
						return fmt.Errorf("URL outside replay scope")
					}
				}
			}
			return nil
		},
		AfterToolCall: func(_ context.Context, step Step) {
			mu.Lock()
			defer mu.Unlock()
			observed = append(observed, step)
			t.Logf("tool=%s duration_ms=%d error=%s", step.Tool, step.DurationMS, step.Error)
			if step.Error != "" {
				return
			}
			for _, page := range browserRenderPages(step.Output) {
				for _, url := range urls {
					if (page.URL == url || page.RequestedURL == url) && len(page.Text) > 100 && page.Title != "" {
						seen[url] = true
					}
				}
			}
		},
		BeforeFinish: func(_ context.Context, candidate FinishCandidate) (FinishDecision, error) {
			mu.Lock()
			defer mu.Unlock()
			if len(seen) != len(urls) {
				return FinishDecision{Feedback: "三页尚未全部成功读取，请继续读取尚未完成的页面，不要提前收尾。"}, nil
			}
			return FinishDecision{}, nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	started := time.Now()
	resp, err := runner.Run(ctx, Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "读取下面三个网页，逐页列出标题和一句话概括。三页都必须读取；没有成功读取的页面要继续读取，不要假定先前同时请求的调用已经完成。不要发送群消息。\n" + strings.Join(urls, "\n")}}, Hooks: hooks})
	if err != nil {
		if path := os.Getenv("DIANA_PI_PARALLEL_RESULTS"); path != "" {
			raw, _ := json.MarshalIndent(observed, "", "  ")
			if writeErr := os.WriteFile(path+".failure.json", raw, 0600); writeErr != nil {
				t.Fatal(writeErr)
			}
		}
		t.Fatal(err)
	}
	if len(seen) != 3 || resp.Text == "" {
		t.Fatalf("incomplete replay: read=%d finish=%s", len(seen), resp.FinishReason)
	}
	t.Logf("model=%s requests=%d live_model_requests=%d steps=%d elapsed_ms=%d input=%d cached=%d output=%d", resp.Model, client.requests, client.requests-1, len(resp.Steps), time.Since(started).Milliseconds(), resp.Usage.InputTokens, resp.Usage.CachedInputTokens, resp.Usage.OutputTokens)
	if path := os.Getenv("DIANA_PI_PARALLEL_RESULTS"); path != "" {
		raw, err := json.MarshalIndent(resp, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
