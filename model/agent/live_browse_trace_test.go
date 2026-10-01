// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 用真实模型、真实搜索和沙箱浏览器回放一轮线上请求，看模型顺着网页找答案的过程。
// 轨迹文件是 debug-traces 里第一步 *-reply.json 的 request.messages（JSON 数组），
// 第一条是当时 Runner 生成的系统提示词，回放时去掉，由当前代码重新生成。默认跳过：
//
//	DIANA_LIVE_LLM=1 DIANA_TEST_LLM_PROVIDER=gemini \
//	DIANA_TEST_LLM_BASE_URL=... DIANA_TEST_LLM_MODEL=gemini-3.8-flash-low DIANA_TEST_LLM_API_KEY=... \
//	DIANA_LIVE_TRACE=/path/messages.json DIANA_LIVE_RUNS=3 DIANA_LIVE_MAX_STEPS=32 \
//	go test ./model/agent/ -run TestLiveBrowseTraceReplay -v -timeout 30m
func TestLiveBrowseTraceReplay(t *testing.T) {
	client := liveAgentClient(t)
	path := strings.TrimSpace(os.Getenv("DIANA_LIVE_TRACE"))
	if path == "" {
		t.Skip("DIANA_LIVE_TRACE is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var recorded []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(data, &recorded); err != nil {
		t.Fatal(err)
	}
	if len(recorded) < 2 {
		t.Fatal("trace has no messages after the runner prompt")
	}
	messages := make([]llm.Message, 0, len(recorded)-1)
	for _, item := range recorded[1:] {
		messages = append(messages, llm.Message{Role: llm.Role(item.Role), Content: item.Content})
	}
	runs, _ := strconv.Atoi(os.Getenv("DIANA_LIVE_RUNS"))
	if runs <= 0 {
		runs = 1
	}
	// 主人对话的步数上限是 MaxAllowedSteps，群成员按机器人配置，按轨迹里的身份传。
	maxSteps, _ := strconv.Atoi(os.Getenv("DIANA_LIVE_MAX_STEPS"))
	if maxSteps <= 0 {
		maxSteps = DefaultMaxSteps
	}
	renderer := NewSandboxedHeadlessBrowser(SandboxedBrowserConfig{Window: BrowserWindowHidden})
	var providers []WebSearchProviderConfig
	for _, engine := range []string{"google", "bing", "duckduckgo", "baidu"} {
		providers = append(providers, WebSearchProviderConfig{Name: engine, Type: WebSearchProviderSearchEngine, Tool: engine, MaxResults: 5})
	}
	for run := 1; run <= runs; run++ {
		search, err := NewWebSearchTool(WebSearchToolOptions{
			Config:         WebSearchConfig{Providers: providers},
			Timeout:        60 * time.Second,
			MaxOutputChars: DefaultMaxToolOutputChars,
			Renderer:       renderer,
		})
		if err != nil {
			t.Fatal(err)
		}
		runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: maxSteps, ToolTimeoutMS: 90_000}, NewToolRegistry(search, NewBrowserRenderTool(renderer)))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		resp, err := runner.Run(ctx, Request{Messages: messages})
		cancel()
		if err != nil {
			t.Logf("run %d: error %v", run, err)
			continue
		}
		var log strings.Builder
		for _, step := range resp.Steps {
			input, _ := json.Marshal(step.Input)
			fmt.Fprintf(&log, "  step %d %s %s", step.Index, step.Tool, truncateText(string(input), 200))
			if step.Error != "" {
				fmt.Fprintf(&log, " ERR %s", truncateText(step.Error, 120))
			}
			log.WriteString("\n")
		}
		t.Logf("run %d: finish=%s steps=%d\n%s  final: %s", run, resp.FinishReason, len(resp.Steps), log.String(), truncateText(resp.Text, 600))
	}
}
