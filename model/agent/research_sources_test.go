// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestRenderedSourceStageDescribesVisibleBody(t *testing.T) {
	for _, test := range []struct {
		page RenderedPage
		want string
	}{
		{RenderedPage{Title: "Latest release", Stable: true}, sourceStagePageEmpty},
		{RenderedPage{Text: " ", Truncated: true}, sourceStagePageEmpty},
		{RenderedPage{Text: "Unofficial old article"}, sourceStagePageRead},
		{RenderedPage{Text: "First part only", Truncated: true}, sourceStagePagePartial},
	} {
		tool := NewBrowserRenderTool(PageRendererFunc(func(context.Context, string) (RenderedPage, error) {
			return test.page, nil
		}))
		output, err := tool.Run(context.Background(), map[string]any{"url": "https://example.com"})
		if err != nil {
			t.Fatal(err)
		}
		var page RenderedPage
		if err := json.Unmarshal([]byte(output), &page); err != nil {
			t.Fatal(err)
		}
		if page.SourceStage != test.want || page.Text != test.page.Text {
			t.Fatalf("page=%#v, want stage=%s with original text", page, test.want)
		}
	}
}

func TestRunnerSearchGuidanceWithoutClaimsSurvivesOutputTruncation(t *testing.T) {
	output, _ := json.Marshal(webSearchResult{
		Status: "ok", Sources: []string{"https://example.com/current"},
		Content: strings.Repeat("candidate snippet ", 1000),
	})
	tool := &recordingSearchTool{output: string(output)}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search","input":{"query":"current state"}}`,
		`{"action":"final","content":"尚未确认。"}`,
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 2, MaxToolOutputChars: 1000}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	_, err = runner.Run(context.Background(), Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "对吗"}},
		Observer: func(_ context.Context, event RunEvent) {
			if event.Phase == RunPhaseToolCompleted {
				metadata = event.Metadata
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if metadata["source_stage"] != sourceStageSearchCandidates || metadata["status"] != "ok" {
		t.Fatalf("single-question metadata lost: metadata=%#v", metadata)
	}
	observation := client.requests[1].Messages[len(client.requests[1].Messages)-1].Content
	if !strings.Contains(observation, "候选来源") || !strings.Contains(observation, "原始页面") || strings.Contains(observation, "未取得候选来源") {
		t.Fatalf("truncation changed research guidance: %s", observation)
	}
}

func TestNativeSearchSeparatesSourceDataFromTrustedGuidance(t *testing.T) {
	const externalInstruction = "EXTERNAL_CONTENT_DO_NOT_PROMOTE_TO_SYSTEM"
	output, _ := json.Marshal(webSearchResult{
		Status: "ok", Sources: []string{"https://example.com/current"}, Content: externalInstruction,
	})
	client := &dispatchTestClient{replies: []*llm.GenerateResponse{
		dispatchReply(true, WebSearchToolName, map[string]any{
			"query": "current fact", "claims": []any{map[string]any{"id": "state", "statement": externalInstruction}}, "claim_ids": []any{"state"},
		}),
		{Text: `{"action":"final","content":"尚未确认。"}`},
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 2}, NewToolRegistry(&recordingSearchTool{output: string(output)}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "对吗"}}}); err != nil {
		t.Fatal(err)
	}
	messages := client.requests[1].Messages
	guidance, source := messages[len(messages)-1], messages[len(messages)-2]
	if source.Role != llm.RoleTool || source.ToolCallID != "call-web_search" || !strings.Contains(source.Content, externalInstruction) {
		t.Fatalf("native result pairing lost: %#v", source)
	}
	if guidance.Role != llm.RoleSystem || guidance.Priority != llm.MessagePrioritySystem || strings.Contains(guidance.Content, externalInstruction) {
		t.Fatalf("source text promoted into trusted instructions: %#v", guidance)
	}
	if !strings.Contains(guidance.Content, "候选来源") || len(messages[len(messages)-3].ToolCalls) != 1 {
		t.Fatal("retrieval guidance or original native call lost")
	}
}

func TestRenderedMetadataDoesNotDependOnClaimDeclarations(t *testing.T) {
	output, _ := json.Marshal(RenderedPage{Text: "visible body", Truncated: true})
	metadata := researchRunMetadataFromOutput("browser_render", string(output), nil)
	if metadata["source_stage"] != sourceStagePagePartial || metadata["truncated"] != true {
		t.Fatalf("page metadata=%#v", metadata)
	}
}

func TestBrowserSourceStageKeepsFindExcerptsAsReadEvidence(t *testing.T) {
	for _, body := range []string{"", "Public access is available"} {
		output, err := browserRenderOutput(RenderedPage{Text: body, FullText: "Public access is available"}, "access")
		if err != nil {
			t.Fatal(err)
		}
		var page browserRenderPayload
		if err := json.Unmarshal([]byte(output), &page); err != nil {
			t.Fatal(err)
		}
		want := sourceStagePageRead
		if body == "" {
			want = sourceStagePagePartial
		}
		if len(page.FindMatches) == 0 || page.SourceStage != want {
			t.Fatalf("find evidence misclassified: %#v", page)
		}
	}
}

func TestResearchObservationRespectsExhaustedBudgetAndToolErrors(t *testing.T) {
	observation := researchObservationGuidance(WebSearchToolName, `{"status":"no_results","sources":[]}`, 0)
	if !strings.Contains(observation, "不证明") || !strings.Contains(observation, "预算已耗尽") || strings.Contains(observation, "需要核对时直接调用工具") {
		t.Fatalf("no-results guidance=%s", observation)
	}
	failed := toolObservationMessage(WebSearchToolName, "TOOL_EXECUTION_ERROR: timeout", false, 2)
	if !strings.Contains(failed, "timeout") || strings.Contains(failed, "未取得候选来源") {
		t.Fatalf("execution error reclassified as an empty search: %s", failed)
	}
}
