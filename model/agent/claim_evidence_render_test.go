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

func TestClaimEvidenceLedgerAcceptsRenderedPageAsFirstPartyEvidence(t *testing.T) {
	ledger := newClaimEvidenceLedger()
	ledger.prepareSearch(map[string]any{
		"claims":    []any{map[string]any{"id": "c1", "statement": "官方页面写了什么"}},
		"claim_ids": []any{"c1"},
	})
	searchResult, _ := json.Marshal(webSearchResult{Status: "no_results", StopReason: "all_queries_exhausted"})
	ledger.observeSearch(string(searchResult), nil)
	page, _ := json.Marshal(RenderedPage{
		RequestedURL: "https://official.example/page",
		URL:          "https://official.example/page",
		Title:        "官方页面",
		Text:         "页面正文",
	})
	if metadata := ledger.observeRenderedPage(string(page), nil); metadata == nil {
		t.Fatal("已渲染页面没有被登记为可引用来源")
	}
	updates := []ClaimUpdate{{
		ID: "c1", Status: ClaimStatusSupported, Summary: "官方页面直接写明",
		Evidence: []ClaimEvidence{{URL: "https://official.example/page", Relation: "supports"}},
	}}
	if reason, ok := ledger.validateFinal(updates); !ok {
		t.Fatalf("已渲染页面被判成无效证据：%s", reason)
	}
	evidence := ledger.traces()[0].Evidence
	if len(evidence) != 1 || evidence[0].SourceType != "first_party" || evidence[0].Distance != "direct" {
		t.Fatalf("evidence=%#v", evidence)
	}
}

func TestClaimEvidenceLedgerIgnoresEmptyRenderedPage(t *testing.T) {
	ledger := newClaimEvidenceLedger()
	ledger.prepareSearch(map[string]any{
		"claims":    []any{map[string]any{"id": "c1", "statement": "页面写了什么"}},
		"claim_ids": []any{"c1"},
	})
	searchResult, _ := json.Marshal(webSearchResult{Status: "no_results", StopReason: "all_queries_exhausted"})
	ledger.observeSearch(string(searchResult), nil)
	page, _ := json.Marshal(RenderedPage{RequestedURL: "https://blank.example/", URL: "https://blank.example/"})
	if metadata := ledger.observeRenderedPage(string(page), nil); metadata != nil {
		t.Fatalf("空白页面不应成为证据来源：%#v", metadata)
	}
	updates := []ClaimUpdate{{
		ID: "c1", Status: ClaimStatusSupported, Summary: "声称已确认",
		Evidence: []ClaimEvidence{{URL: "https://blank.example/", Relation: "supports"}},
	}}
	if reason, ok := ledger.validateFinal(updates); ok || !strings.Contains(reason, "blank.example") {
		t.Fatalf("reason=%q ok=%v", reason, ok)
	}
}

func TestClaimEvidenceLedgerBindingFailureNamesRejectedSourceAndAlternatives(t *testing.T) {
	ledger := newClaimEvidenceLedger()
	ledger.prepareSearch(map[string]any{
		"claims":    []any{map[string]any{"id": "c1", "statement": "待验证事实"}},
		"claim_ids": []any{"c1"},
	})
	searchResult, _ := json.Marshal(webSearchResult{
		Status: "ok", StopReason: "sufficient_evidence", Sources: []string{"https://searched.example/record"},
	})
	ledger.observeSearch(string(searchResult), nil)
	updates := []ClaimUpdate{{
		ID: "c1", Status: ClaimStatusSupported, Summary: "声称已确认",
		Evidence: []ClaimEvidence{{URL: "https://invented.example/fact", Relation: "supports"}},
	}}
	reason, ok := ledger.validateFinal(updates)
	if ok {
		t.Fatal("未检索来源不应通过校验")
	}
	if !strings.Contains(reason, "invented.example") || !strings.Contains(reason, "searched.example") {
		t.Fatalf("校验失败信息既要点名被拒来源，也要给出可引用来源：%q", reason)
	}
}

func TestRunnerCitesRenderedPageWithoutRepairEvenWhenOutputIsTruncated(t *testing.T) {
	searchResult, _ := json.Marshal(webSearchResult{Status: "no_results", StopReason: "all_queries_exhausted"})
	searchTool := &recordingSearchTool{output: string(searchResult)}
	page, _ := json.Marshal(RenderedPage{
		RequestedURL: "https://official.example/commit/abc123",
		URL:          "https://official.example/commit/abc123",
		Title:        "提交详情",
		Text:         strings.Repeat("提交正文。", 200),
	})
	renderTool := &recordingRenderTool{output: string(page)}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search.search","input":{"query":"latest commit","claims":[{"id":"commit","statement":"最新提交做了什么"}],"claim_ids":["commit"]}}`,
		`{"action":"tool","tool":"browser_render","input":{"url":"https://official.example/commit/abc123"}}`,
		`{"action":"final","content":"最新提交把环境代理收成了可发版状态。","claims":[{"id":"commit","status":"supported","summary":"提交页直接写明","evidence":[{"url":"https://official.example/commit/abc123","relation":"supports"}]}]}`,
	}}
	// MaxToolOutputChars 会把渲染结果截成非法 JSON；证据登记必须读未截断的原始结果。
	runner, err := NewRunner(client, Config{MaxSteps: 3, MaxToolOutputChars: 80}, NewToolRegistry(searchTool, renderTool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "最新一个 commit 做了什么"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 3 {
		t.Fatalf("证据绑定不该触发协议修复，requests=%d", len(client.requests))
	}
	if resp.Text != "最新提交把环境代理收成了可发版状态。" {
		t.Fatalf("response=%#v", resp)
	}
	if len(resp.Claims) != 1 || resp.Claims[0].Status != ClaimStatusSupported {
		t.Fatalf("claims=%#v", resp.Claims)
	}
	if evidence := resp.Claims[0].Evidence; len(evidence) != 1 || evidence[0].SourceType != "first_party" {
		t.Fatalf("evidence=%#v", evidence)
	}
}

func TestRunnerRepairsFinalThatLeaksInternalProtocolTerms(t *testing.T) {
	client := &scriptedClient{responses: []string{
		`{"action":"final","content":"本次证据账本没有收录对应页面，暂不作结论。"}`,
		`{"action":"final","content":"最新提交把环境代理收成了可发版状态。"}`,
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 2}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "最新一个 commit 做了什么"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "最新提交把环境代理收成了可发版状态。" || len(client.requests) != 2 {
		t.Fatalf("response=%#v requests=%d", resp, len(client.requests))
	}
	correction := client.requests[1].Messages[len(client.requests[1].Messages)-1].Content
	if !strings.Contains(correction, "内部协议词") || !strings.Contains(correction, "证据账本") {
		t.Fatalf("repair prompt=%q", correction)
	}
}

func TestWebSearchCandidatesRelaxUnsupportedOperators(t *testing.T) {
	candidates, err := webSearchCandidates(map[string]any{"query": "site:github.com/owner/repo latest commit"}, 4)
	if err != nil {
		t.Fatal(err)
	}
	var relaxed string
	for _, candidate := range candidates {
		if candidate.Strategy == "operators_relaxed" {
			relaxed = candidate.Query
		}
	}
	if relaxed == "" {
		t.Fatalf("缺少去算子候选：%#v", candidates)
	}
	if strings.Contains(relaxed, "site:") || !strings.Contains(relaxed, "github.com/owner/repo") {
		t.Fatalf("query=%q", relaxed)
	}
}

func TestRunnerAdvisoryEvidenceLedgerRecordsWithoutBlockingFinal(t *testing.T) {
	searchResult, _ := json.Marshal(webSearchResult{Status: "no_results", StopReason: "all_queries_exhausted"})
	tool := &recordingSearchTool{output: string(searchResult)}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search.search","input":{"query":"verify state","claims":[{"id":"state","statement":"状态是否成立"}],"claim_ids":["state"]}}`,
		`{"action":"final","content":"状态成立。","claims":[{"id":"state","status":"supported","summary":"确定存在","evidence":[]}]}`,
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 2, EvidenceLedgerAdvisory: true}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "核验状态"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "状态成立。" || len(client.requests) != 2 {
		t.Fatalf("宽松模式不应触发协议修复：response=%#v requests=%d", resp, len(client.requests))
	}
	// 仍然结算并留痕：没有来源的结论照样降级为 insufficient，只是不再拦截回复。
	if len(resp.Claims) != 1 || resp.Claims[0].Status != ClaimStatusInsufficient {
		t.Fatalf("claims=%#v", resp.Claims)
	}
}

type recordingRenderTool struct {
	output string
	calls  int
	input  map[string]any
}

func (t *recordingRenderTool) Name() string { return browserRenderToolName }
func (t *recordingRenderTool) Description() string {
	return `input: {"url":"https://example.com"}`
}
func (t *recordingRenderTool) Run(_ context.Context, input map[string]any) (string, error) {
	t.calls++
	t.input = input
	return t.output, nil
}
