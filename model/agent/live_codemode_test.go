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

type codeModeLiveClient struct {
	llm.LLMClient
	Calls      int
	LiveCalls  int
	Replay     []llm.ToolCall
	ForceFresh bool
	Outputs    []string
}

func (c *codeModeLiveClient) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.Calls++
	if len(c.Replay) > 0 {
		call := c.Replay[0]
		c.Replay = c.Replay[1:]
		if c.ForceFresh {
			call.Arguments["fresh"] = true
		}
		return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{call}}, nil
	}
	c.LiveCalls++
	response, err := c.LLMClient.Generate(ctx, req)
	if response != nil {
		c.Outputs = append(c.Outputs, response.Text)
		if c.ForceFresh {
			for i := range response.ToolCalls {
				if response.ToolCalls[i].Name == codeModeToolName {
					response.ToolCalls[i].Arguments["fresh"] = true
				}
			}
		}
	}
	return response, err
}

// By default all model decisions and browser reads are real. An explicit TRACE
// instead replays recorded script calls before live finalization to isolate cache
// behavior; injected responses are counted separately. No chat sender is registered.
func TestLiveCodeModeReplay(t *testing.T) {
	if os.Getenv("DIANA_CODEMODE_REPLAY") != "1" {
		t.Skip("explicit live replay only")
	}
	urls := []string{
		"https://daily.dev/posts/day-6-reset-hj5q4m3rz",
		"https://daily.dev/posts/never-slept-better-and-feeling-reseted-brand-new-me-and-brand-new-usage-for-all-chatgpt-work-and-co-k949hlchn",
		"https://daily.dev/posts/this-celebration-is-moved-to-tomorrow-as-the-button-was-already-pressed-today--irijyduoe",
	}
	mode := os.Getenv("DIANA_CODEMODE_ARM")
	if mode != "ordinary" && mode != "codemode" {
		t.Fatal("DIANA_CODEMODE_ARM must be ordinary or codemode")
	}
	registry := NewToolRegistry(NewBrowserRenderTool(nil))
	defer registry.Close()
	if mode == "codemode" {
		registry.Register(testCodeModeTool(t))
	}
	client := &codeModeLiveClient{LLMClient: liveAgentClient(t)}
	tracePath := os.Getenv("DIANA_CODEMODE_TRACE")
	if tracePath != "" {
		if mode != "codemode" {
			t.Fatal("recorded script replay requires codemode arm")
		}
		raw, err := os.ReadFile(tracePath)
		if err != nil {
			t.Fatal(err)
		}
		var trace struct {
			Observed []Step `json:"observed"`
		}
		if err = json.Unmarshal(raw, &trace); err != nil {
			t.Fatal(err)
		}
		for _, step := range trace.Observed {
			if step.Tool == codeModeToolName && step.Error == "" {
				client.Replay = append(client.Replay, llm.ToolCall{ID: fmt.Sprintf("recorded-%d", len(client.Replay)+1), Name: codeModeToolName, Arguments: step.Input})
			}
		}
		if len(client.Replay) == 0 {
			t.Fatal("no recorded scripts")
		}
		client.ForceFresh = os.Getenv("DIANA_CODEMODE_FRESH") == "1"
	}
	runner, err := NewRunner(client, Config{MaxSteps: 12, ToolTimeoutMS: 60000}, registry)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	seen := map[string]bool{}
	scriptUsed := false
	fullTitleSeen := false
	var observed []Step
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	start := time.Now()
	prompt := "读取以下三个公开网页，按给定顺序逐页列出：页面标题、URL、正文开头最多120个字符（原文摘录，不要改写）。三页都要实际读取，不要猜测；如果没有正文，明确说明。只返回这些字段，不要发送聊天消息。请尽量批量处理并减少不必要的中间内容。\n" + strings.Join(urls, "\n")
	if mode == "codemode" && os.Getenv("DIANA_CODEMODE_REQUIRE_SCRIPT") == "1" {
		prompt += "\n本次明确使用 codemode 代码编排模式：通过脚本调用读取工具，在脚本中筛选需要的字段后返回，避免把不相关的整页内容交回模型。"
	}
	response, runErr := runner.Run(ctx, Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: prompt}},
		Hooks: RunHooks{
			BeforeToolCall: func(_ context.Context, call ToolCallContext) error {
				if call.Tool == codeModeToolName {
					return nil
				}
				if call.Tool != browserRenderToolName {
					return fmt.Errorf("tool outside replay scope")
				}
				for _, url := range browserRenderURLs(call.Input) {
					allowed := false
					for _, want := range urls {
						if url == want {
							allowed = true
						}
					}
					if !allowed {
						return fmt.Errorf("URL outside replay scope")
					}
				}
				return nil
			},
			AfterToolCall: func(_ context.Context, step Step) {
				mu.Lock()
				defer mu.Unlock()
				observed = append(observed, step)
				if step.Error != "" {
					return
				}
				if step.Tool == codeModeToolName {
					scriptUsed = true
				}
				if step.Tool == browserRenderToolName {
					for _, page := range browserRenderPages(step.Output) {
						if strings.Contains(page.Title, "Happy Thursday") {
							fullTitleSeen = true
						}
						if page.Title != "" && len(page.Text) > 100 {
							seen[page.RequestedURL] = true
							seen[page.URL] = true
						}
					}
				}
			},
			BeforeFinish: func(_ context.Context, candidate FinishCandidate) (FinishDecision, error) {
				mu.Lock()
				defer mu.Unlock()
				for _, url := range urls {
					if !seen[url] {
						return FinishDecision{Feedback: "尚未读取全部页面，请读取缺失页面后再收尾。"}, nil
					}
				}
				if fullTitleSeen && !strings.Contains(candidate.Text, "Happy Thursday") {
					return FinishDecision{Feedback: "最终回复没有保留工具返回的完整页面标题；请按已读取的 title 字段核对标题，不要自行截短。"}, nil
				}
				return FinishDecision{}, nil
			},
		},
	})
	report := map[string]any{"arm": mode, "elapsed_ms": time.Since(start).Milliseconds(), "live_model_calls": client.LiveCalls, "recorded_model_responses": client.Calls - client.LiveCalls, "force_fresh": client.ForceFresh, "model_outputs": client.Outputs, "script_used": scriptUsed, "response": response, "observed": observed}
	if runErr != nil {
		report["error"] = runErr.Error()
	}
	if path := os.Getenv("DIANA_CODEMODE_RESULTS"); path != "" {
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if runErr != nil {
		t.Fatal(runErr)
	}
	if mode == "codemode" && !scriptUsed {
		t.Fatal("model did not exercise Code Mode; not a valid Code Mode replay")
	}
	if !fullTitleSeen || !strings.Contains(response.Text, "Happy Thursday") {
		t.Fatal("expected full public-page title was not verified")
	}
	t.Logf("arm=%s model=%s live_calls=%d recorded_responses=%d elapsed_ms=%d usage=%+v", mode, response.Model, client.LiveCalls, client.Calls-client.LiveCalls, time.Since(start).Milliseconds(), response.Usage)
}
