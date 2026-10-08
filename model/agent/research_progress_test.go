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
func TestRunnerNudgesReadingAndBouncesUnreadNegation(t *testing.T) {
	tool := &recordingSearchTool{output: researchProgressSearchOutput}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search","input":{"query":"gemini-image-2.1"}}`,
		`{"action":"tool","tool":"web_search","input":{"queries":["\"gemini-image-2.1\"","gemini image 2.1 release"]}}`,
		`{"action":"final","content":"查了一圈，Google 官方根本没有叫 gemini-image-2.1 的模型"}`,
		`{"action":"final","content":"没查实：搜到了 Nano Banana 2.1 的官方文档页，但这次没读到发布日期"}`,
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 8, ProtocolRepairLimit: 3}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "gemini-image-2.1是今天刚发布的？"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resp.Text, "没查实") || len(client.requests) != 4 {
		t.Fatalf("text=%q requests=%d", resp.Text, len(client.requests))
	}

	first := requestContent(client.requests[1])
	if !strings.Contains(first, "已搜索 1 次（gemini-image-2.1）；还没读过任何页面") {
		t.Fatalf("progress missing after first search:\n%s", first)
	}
	if strings.Contains(first, `"providers"`) || strings.Contains(first, `"budget"`) {
		t.Fatalf("diagnostics should be stripped from the model view:\n%s", first)
	}
	if strings.Contains(first, "再换关键词多半") {
		t.Fatal("should not nudge after a single search")
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

	repair := requestContent(client.requests[3])
	if !strings.Contains(repair, "没读过任何页面，草稿却下了否定结论") || !strings.Contains(repair, "ai.google.dev") {
		t.Fatalf("negation repair missing:\n%s", repair)
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

	// 读过页面：否定有依据，不打回。
	progress := collectResearchProgress([]Step{
		{Tool: WebSearchToolName, Input: map[string]any{"query": "x"}, Output: researchProgressSearchOutput},
		{Tool: browserRenderToolName, Output: `{"url":"https://ai.google.dev/x","requested_url":"https://ai.google.dev/x","title":"Doc","text":"正文"}`},
	})
	if progress.searchedWithoutReading() {
		t.Fatalf("read page not detected: %#v", progress)
	}

	// 只读过一条推文：仍算没核实过。
	progress = collectResearchProgress([]Step{
		{Tool: WebSearchToolName, Input: map[string]any{"query": "x"}, Output: researchProgressSearchOutput},
		{Tool: browserRenderToolName, Output: `{"url":"https://x.com/a/status/1","requested_url":"https://x.com/a/status/1","title":"Post","text":"正文"}`},
	})
	if !progress.searchedWithoutReading() {
		t.Fatalf("social read should not count: %#v", progress)
	}
}

func TestResearchNegationPatternTargetsDenials(t *testing.T) {
	for _, text := range []string{"Google 官方根本没有这个模型", "目前还没发布", "没搜到相关消息", "你大概看串了"} {
		if !researchNegationPattern.MatchString(text) {
			t.Errorf("should match %q", text)
		}
	}
	for _, text := range []string{"Nano Banana 2.1 是 10 月 7 日发布的", "价格是每张 0.04 美元"} {
		if researchNegationPattern.MatchString(text) {
			t.Errorf("should not match %q", text)
		}
	}
}
