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

func TestRunnerPreservesSupportedClaimWhenAnotherIsUnconfirmed(t *testing.T) {
	searchResult, _ := json.Marshal(webSearchResult{
		Status: "ok", StopReason: "sufficient_evidence", Sources: []string{"https://source.example/record"}, Content: "source material",
	})
	tool := &recordingSearchTool{output: string(searchResult)}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search.search","input":{"query":"verify identity","claims":[{"id":"identity","statement":"实体身份是否成立"},{"id":"local_state","statement":"指定条件下状态如何"}],"claim_ids":["identity"]}}`,
		`{"action":"final","content":"已确认实体身份（来源：https://source.example/record）。指定条件下的状态尚未确认。","claims":[{"id":"identity","status":"supported","summary":"来源确认了实体身份","evidence":[{"url":"https://source.example/record","relation":"supports","source_type":"official_record","distance":"direct","strength":"high"}]},{"id":"local_state","status":"not_searched","summary":"尚未检索"}]}`,
	}}
	var events []RunEvent
	runner, err := NewRunner(client, Config{MaxSteps: 2}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "同时核验两个独立问题"}},
		Observer: func(_ context.Context, event RunEvent) { events = append(events, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Text, "已确认") || !strings.Contains(resp.Text, "尚未确认") {
		t.Fatalf("response=%#v", resp)
	}
	if len(resp.Claims) != 2 || resp.Claims[0].Status != ClaimStatusSupported || resp.Claims[1].Status != ClaimStatusNotSearched {
		t.Fatalf("claims=%#v", resp.Claims)
	}
	if len(tool.input) != 1 || tool.input["query"] != "verify identity" {
		t.Fatalf("execution input leaked protocol metadata: %#v", tool.input)
	}
	foundTrace := false
	for _, event := range events {
		if event.Phase == RunPhaseToolCompleted && event.Metadata["claim_count"] == 2 {
			foundTrace = true
		}
	}
	if !foundTrace {
		t.Fatalf("claim observability missing: %#v", events)
	}
}

func TestRunnerUnwrapsReplyCompatibilityJSONAfterSearch(t *testing.T) {
	tool := &recordingSearchTool{output: "湖南和江西米粉资料"}
	completeReply := "简单说：湖南米粉更突出汤和码子，江西米粉更突出粉本身和拌炒风味。\n" +
		"1. 湖南常见汤粉、盖码粉。\n" +
		"2. 江西常见拌粉、炒粉和汤粉。\n" +
		"3. 两省内部都有很多地方流派，不能用单一口味概括。"
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search.search","input":{"query":"湖南米粉 江西米粉 区别"}}`,
		`{"reply":"` + strings.ReplaceAll(completeReply, "\n", `\n`) + `"}`,
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 2}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "湖南米粉和江西米粉有什么区别"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != completeReply {
		t.Fatalf("response text = %q, want complete unwrapped reply", resp.Text)
	}
	if resp.FinishReason != "final" || tool.calls != 1 {
		t.Fatalf("response=%#v calls=%d", resp, tool.calls)
	}
}

func TestRunnerRepairsFinalThatClaimsUnsupportedFact(t *testing.T) {
	searchResult, _ := json.Marshal(webSearchResult{Status: "no_results", StopReason: "all_queries_exhausted"})
	tool := &recordingSearchTool{output: string(searchResult)}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search.search","input":{"query":"verify state","claims":[{"id":"state","statement":"状态是否成立"}],"claim_ids":["state"]}}`,
		`{"action":"final","content":"确定存在。","claims":[{"id":"state","status":"supported","summary":"确定存在","evidence":[]}]}`,
		`{"action":"final","content":"当前检索不足，暂时无法确认。","claims":[{"id":"state","status":"insufficient","summary":"没有找到足够证据"}]}`,
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 2}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "核验状态"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "当前检索不足，暂时无法确认。" || len(client.requests) != 3 || resp.Claims[0].Status != ClaimStatusInsufficient {
		t.Fatalf("response=%#v requests=%d", resp, len(client.requests))
	}
	if correction := client.requests[2].Messages[len(client.requests[2].Messages)-1].Content; !strings.Contains(correction, "缺少有效") || !strings.Contains(correction, "证据账本") {
		t.Fatalf("repair prompt=%q", correction)
	}
}

func TestRunnerAcceptsSearchedURLWhenEvidenceMetadataNeedsNormalization(t *testing.T) {
	searchResult, _ := json.Marshal(webSearchResult{
		Status: "ok", StopReason: "sufficient_evidence", Sources: []string{"https://official.example/record"}, Content: "official record",
	})
	tool := &recordingSearchTool{output: string(searchResult)}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search.search","input":{"query":"verify statement","claims":[{"id":"c1","statement":"该表述是否成立"}],"claim_ids":["c1"]}}`,
		`{"action":"final","content":"对，这个表述有官方记录支持。","claims":[{"id":"c1","status":"supported","summary":"官方记录支持该表述","evidence":[{"url":"https://official.example/record","relation":"direct","source_type":"官方记录","distance":"primary","strength":"strong"}]}]}`,
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 2}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "这个说法对吗"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "对，这个表述有官方记录支持。" || len(client.requests) != 2 || len(resp.Claims) != 1 || resp.Claims[0].Status != ClaimStatusSupported {
		t.Fatalf("response=%#v requests=%d", resp, len(client.requests))
	}
}

func TestRunnerFinalizesFromClaimLedgerAfterToolBudget(t *testing.T) {
	searchResult, _ := json.Marshal(webSearchResult{
		Status: "budget_exhausted", StopReason: "provider_call_budget_exhausted", Sources: []string{"https://evidence.example/item"}, Content: "partial evidence",
	})
	tool := &recordingSearchTool{output: string(searchResult)}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search.search","input":{"query":"first gap","claims":[{"id":"known","statement":"第一项事实"},{"id":"gap","statement":"第二项事实"}],"claim_ids":["known"]}}`,
		`{"action":"final","content":"第一项已有来源支持；第二项仍未检索。","claims":[{"id":"known","status":"supported","summary":"第一项已确认","evidence":[{"url":"https://evidence.example/item","relation":"supports","source_type":"primary_reporting","distance":"direct","strength":"medium"}]},{"id":"gap","status":"not_searched","summary":"未检索"}]}`,
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 1}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "核验两项事实"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.FinishReason != "tool_budget_exhausted" || tool.calls != 1 || len(resp.Claims) != 2 {
		t.Fatalf("response=%#v calls=%d", resp, tool.calls)
	}
	finalPrompt := client.requests[1].Messages[len(client.requests[1].Messages)-1].Content
	if !strings.Contains(finalPrompt, "禁止再调用任何工具") || !strings.Contains(finalPrompt, "逐主张证据账本") || !strings.Contains(finalPrompt, "不得暴露 claim ID") {
		t.Fatalf("finalization prompt=%q", finalPrompt)
	}
}

func TestRunnerSynthesizesFinalReplyAfterToolBudget(t *testing.T) {
	tool := &recordingSearchTool{output: "result"}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search.search","input":{"query":"Diana latest","ignored":"history"}}`,
		`{"action":"final","content":"整理后的答案"}`,
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 1}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "查一下"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "整理后的答案" || tool.calls != 1 {
		t.Fatalf("response=%#v calls=%d", resp, tool.calls)
	}
	if _, exists := tool.input["ignored"]; exists {
		t.Fatalf("search input was not minimized: %#v", tool.input)
	}
}

func TestRunnerLimitsWebSearchCalls(t *testing.T) {
	tool := &recordingSearchTool{output: "result"}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search.search","input":{"query":"one"}}`,
		`{"action":"tool","tool":"web_search.search","input":{"query":"two"}}`,
		`{"action":"tool","tool":"web_search.search","input":{"query":"three"}}`,
		`{"action":"tool","tool":"web_search.search","input":{"query":"four"}}`,
		`{"action":"final","content":"done"}`,
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 5}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "search"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "done" || tool.calls != maxWebSearchCallsPerAgentRun {
		t.Fatalf("response=%#v calls=%d", resp, tool.calls)
	}
	if len(resp.Steps) != 4 || !strings.Contains(resp.Steps[3].Error, "最多执行") {
		t.Fatalf("steps = %#v", resp.Steps)
	}
}

func TestRunnerPromptRequiresSearchForSpecificProductOpinions(t *testing.T) {
	runner, err := NewRunner(&scriptedClient{}, Config{MaxSteps: 3}, NewToolRegistry(&recordingSearchTool{}))
	if err != nil {
		t.Fatal(err)
	}
	prompt := runner.systemPrompt()
	for _, expected := range []string{"具体商品", "口碑", "味道", "先调用 web_search.search 再回答", "不要凭印象编造亲身体验"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("search guidance missing %q: %s", expected, prompt)
		}
	}
}

type recordingSearchTool struct {
	output string
	calls  int
	input  map[string]any
}

func (t *recordingSearchTool) Name() string { return WebSearchToolName }
func (t *recordingSearchTool) Description() string {
	return `input: {"query":"search terms"}`
}
func (t *recordingSearchTool) Run(_ context.Context, input map[string]any) (string, error) {
	t.calls++
	t.input = input
	return t.output, nil
}
