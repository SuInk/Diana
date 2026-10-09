// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

const researchProgressSearchOutput = `{
  "status": "ok",
  "stop_reason": "candidate_sources_found",
  "source_stage": "search_candidates",
  "query": "gemini-image-2.1",
  "results": [
    {"title": "Google Flow Backend Updated with Nano Banana 2.1", "url": "https://www.linkedin.com/posts/nano-banana"},
    {"title": "Qwen-Image-2.1 Benchmark", "url": "https://www.remio.ai/post/qwen-image-2-1"},
    {"title": "Gemini Nano Banana 2.1 - Google AI for Developers", "url": "https://ai.google.dev/gemini-api/docs/models/gemini-nano-banana-2.1"}
  ],
  "providers": [{"provider": "tavily", "type": "tavily", "status": "success", "attempts": 1}],
  "attempts": [{"provider": "tavily", "status": "success"}],
  "budget": {"max_queries": 4, "queries_used": 1}
}`

func requestContent(req llm.GenerateRequest) string {
	var builder strings.Builder
	for _, message := range req.Messages {
		builder.WriteString(message.Content)
		builder.WriteString("\n")
	}
	return builder.String()
}

// 线上 10-07：连搜几次一页没读，最后回「官方根本没有」。
func TestRunnerNudgesReadingWithoutBlockingFinalAnswer(t *testing.T) {
	tool := &recordingSearchTool{output: researchProgressSearchOutput}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search","input":{"query":"gemini-image-2.1"}}`,
		`{"action":"tool","tool":"web_search","input":{"queries":["\"gemini-image-2.1\"","gemini image 2.1 release"]}}`,
		`{"action":"final","content":"查了一圈，Google 官方根本没有叫 gemini-image-2.1 的模型"}`,
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 8, ProtocolRepairLimit: 3}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "gemini-image-2.1是今天刚发布的？"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Text, "官方根本没有") || len(client.requests) != 3 {
		t.Fatalf("text=%q requests=%d", resp.Text, len(client.requests))
	}

	first := requestContent(client.requests[1])
	if !strings.Contains(first, "已搜索 1 次（gemini-image-2.1）；还没读过任何页面") {
		t.Fatalf("progress missing after first search:\n%s", first)
	}
	if strings.Contains(first, `"providers"`) || strings.Contains(first, `"budget"`) {
		t.Fatalf("diagnostics should be stripped from the model view:\n%s", first)
	}
	if !strings.Contains(first, "browser_render") {
		t.Fatalf("should nudge reading right after the first search:\n%s", first)
	}

	second := requestContent(client.requests[2])
	nudge := second[strings.LastIndex(second, "本轮调研进度"):]
	if !strings.Contains(nudge, "已搜索 2 次") || !strings.Contains(nudge, "browser_render") {
		t.Fatalf("nudge missing:\n%s", nudge)
	}
	official := strings.Index(nudge, "ai.google.dev")
	other := strings.Index(nudge, "remio.ai")
	if official < 0 || other < 0 || official > other {
		t.Fatalf("first-party looking candidate should come first:\n%s", nudge)
	}
	if strings.Contains(nudge, "linkedin.com") {
		t.Fatalf("social hosts should not be suggested:\n%s", nudge)
	}

}

func TestRunnerKeepsNegationAfterReadingOrWithoutSearch(t *testing.T) {
	// 没搜过：不归这条规则管。
	client := &scriptedClient{responses: []string{`{"action":"final","content":"没有这种说法"}`}}
	runner, err := NewRunner(client, Config{MaxSteps: 4}, NewToolRegistry(&recordingSearchTool{}))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "是真的吗"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "没有这种说法" || len(client.requests) != 1 {
		t.Fatalf("text=%q requests=%d", resp.Text, len(client.requests))
	}

}

func TestResearchProgressRecordsReadsWithoutMinimum(t *testing.T) {
	for _, url := range []string{"https://ai.google.dev/x", "https://x.com/a/status/1"} {
		progress := collectResearchProgress([]Step{
			{Tool: WebSearchToolName, Input: map[string]any{"query": "x"}, Output: researchProgressSearchOutput},
			{Tool: browserRenderToolName, Output: `{"url":"` + url + `","text":"正文"}`},
		})
		note := progress.note()
		if len(progress.read) != 1 || !strings.Contains(note, url) {
			t.Fatalf("actual read missing: %s", note)
		}
		if strings.Contains(note, "下结论前再打开") || !strings.Contains(note, "不必凑页面数量") {
			t.Fatalf("unexpected page quota: %s", note)
		}
	}
}
