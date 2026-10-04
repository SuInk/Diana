// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
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
		`{"action":"tool","tool":"web_search","input":{"query":"verify identity","claims":[{"id":"identity","statement":"实体身份是否成立"},{"id":"local_state","statement":"指定条件下状态如何"}],"claim_ids":["identity"]}}`,
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
	completeReply := "简单说：湖南米粉更突出汤和码子，江西米粉更突出粉本身和拌炒风味。[diana-line]" +
		"1. 湖南常见汤粉、盖码粉。[diana-line]" +
		"2. 江西常见拌粉、炒粉和汤粉。[diana-line]" +
		"3. 两省内部都有很多地方流派，不能用单一口味概括。"
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search","input":{"query":"湖南米粉 江西米粉 区别"}}`,
		`{"reply":"` + completeReply + `"}`,
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

func TestRunnerDoesNotSpendRepairRoundOnUnsupportedClaim(t *testing.T) {
	searchResult, _ := json.Marshal(webSearchResult{Status: "no_results", StopReason: "all_queries_exhausted"})
	tool := &recordingSearchTool{output: string(searchResult)}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search","input":{"query":"verify state","claims":[{"id":"state","statement":"状态是否成立"}],"claim_ids":["state"]}}`,
		`{"action":"final","content":"确定存在。","claims":[{"id":"state","status":"supported","summary":"确定存在","evidence":[]}]}`,
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 2}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "核验状态"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "确定存在。" || len(client.requests) != 2 {
		t.Fatalf("response=%#v requests=%d", resp, len(client.requests))
	}
	if resp.Claims[0].Status != ClaimStatusInsufficient {
		t.Fatalf("无证据的 supported 仍要在账本里降级留痕：%#v", resp.Claims)
	}
}

func TestRunnerAcceptsSearchedURLWhenEvidenceMetadataNeedsNormalization(t *testing.T) {
	searchResult, _ := json.Marshal(webSearchResult{
		Status: "ok", StopReason: "sufficient_evidence", Sources: []string{"https://official.example/record"}, Content: "official record",
	})
	tool := &recordingSearchTool{output: string(searchResult)}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search","input":{"query":"verify statement","claims":[{"id":"c1","statement":"该表述是否成立"}],"claim_ids":["c1"]}}`,
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
		`{"action":"tool","tool":"web_search","input":{"query":"first gap","claims":[{"id":"known","statement":"第一项事实"},{"id":"gap","statement":"第二项事实"}],"claim_ids":["known"]}}`,
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
		`{"action":"tool","tool":"web_search","input":{"query":"Diana latest","ignored":"history"}}`,
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

func TestRunnerPromptRequiresSearchForSpecificProductOpinions(t *testing.T) {
	runner, err := NewRunner(&scriptedClient{}, Config{MaxSteps: 3}, NewToolRegistry(&recordingSearchTool{}))
	if err != nil {
		t.Fatal(err)
	}
	prompt := runner.systemPrompt()
	for _, expected := range []string{"具体商品", "口碑", "味道", "必须搜索", "不要凭印象编造亲身体验"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("search guidance missing %q: %s", expected, prompt)
		}
	}
}

// 这条规则是「该搜没搜」的唯一防线：证据账本只有在模型已经调过 web_search
// 之后才会 active，不搜就直接按 plain_text 收口，一点校验都不过。线上真实
// case：群里问某个开源项目有没有现成的非阻塞 subagent 实现，模型凭印象答了
// 「生态里没有」，实际有四个包；它踩的正是「当前上下文已经足够」这个例外——
// 那个群当天一直在聊这个项目，上下文里全是相关讨论，但没有一条是核实过的。
func TestRunnerPromptRequiresSearchForEcosystemAndAvailabilityClaims(t *testing.T) {
	runner, err := NewRunner(&scriptedClient{}, Config{MaxSteps: 3}, NewToolRegistry(&recordingSearchTool{}))
	if err != nil {
		t.Fatal(err)
	}
	prompt := runner.systemPrompt()
	for _, expected := range []string{
		// 「有没有现成实现」这类问题要明确落进典型场景，不能靠模型自己归类。
		"开源项目或服务是否支持某项能力",
		"有没有现成实现或插件",
		"当前版本与 API 现状",
		// 堵住把聊天记录当已核实事实的漏洞。
		"聊天记录里讨论过这个话题不等于其中的事实已经核实",
		"不能拿来替代检索",
		// 讲原理和断言现状要分开。
		"讲原理可以直接答",
	} {
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

func TestRunnerKeepsFinalReplyWhenEvidenceDoesNotBind(t *testing.T) {
	searchResult, _ := json.Marshal(webSearchResult{
		Status: "ok", StopReason: "sufficient_evidence", Sources: []string{"https://source.example/record"}, Content: "source material",
	})
	tool := &recordingSearchTool{output: string(searchResult)}
	reply := "查到了，这场演出改到下周六晚上七点。"
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search","input":{"query":"演出时间","claims":[{"id":"showtime","statement":"演出时间是否变更"}],"claim_ids":["showtime"]}}`,
		`{"action":"final","content":"` + reply + `","claims":[{"id":"showtime","status":"supported","summary":"官方公告写明改期","evidence":[{"url":"https://source.example/record?utm_source=chat","relation":"supports","source_type":"official_record","distance":"direct","strength":"high"}]}]}`,
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 2}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "演出改期了吗"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != reply {
		t.Fatalf("证据绑不上时不该替换正文：%q", resp.Text)
	}
	if len(resp.Claims) != 1 || resp.Claims[0].Status != ClaimStatusInsufficient || len(resp.Claims[0].Evidence) != 0 {
		t.Fatalf("账本仍要如实留痕：%#v", resp.Claims)
	}
	if strings.Contains(resp.Text, "尚未确认") {
		t.Fatalf("内部账本文案泄漏到正文：%q", resp.Text)
	}
}

func TestRunnerModelCanFinalizeWithoutSearch(t *testing.T) {
	for _, response := range []string{`{"action":"final","content":"今天挺好的。"}`, "今天挺好的。"} {
		t.Run(response, func(t *testing.T) {
			tool := &recordingSearchTool{output: "unused"}
			client := &scriptedClient{responses: []string{response}}
			runner, err := NewRunner(client, Config{MaxSteps: 2, ProtocolRepairLimit: 2}, NewToolRegistry(tool))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "今天怎么样"}}})
			if err != nil {
				t.Fatal(err)
			}
			if resp.Text != "今天挺好的。" || tool.calls != 0 || len(client.requests) != 1 || client.requests[0].ToolChoice != "" {
				t.Fatalf("resp=%#v calls=%d requests=%#v", resp, tool.calls, client.requests)
			}
		})
	}
}

// claims 侧早就按 allowedSources 过滤证据 URL 了，但那只清洗内部账本——用户看到
// 的是正文。线上实测抓到过：检索返回的内容跟问题无关，模型照样在正文里写了一个
// 看起来很合理、实际从没检索到的 go.dev 链接，而同一轮的 claim 已经被降级成
// insufficient。内部账本说「没证据」，正文却在附来源。
func TestRunnerRepairsFinalCitingUnsearchedSource(t *testing.T) {
	searchResult, _ := json.Marshal(webSearchResult{
		Status: "ok", StopReason: "sufficient_evidence",
		Sources: []string{"https://real.example/a"}, Content: "检索到的真实资料",
	})
	tool := &recordingSearchTool{output: string(searchResult)}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search","input":{"query":"q","claims":[{"id":"c1","statement":"某事是否成立"}],"claim_ids":["c1"]}}`,
		// 正文引了一个从没检索到的链接。
		`{"action":"final","content":"结论如此。来源：https://fabricated.example/b"}`,
		`{"action":"final","content":"这一点我没有查到可用来源。"}`,
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 3, ProtocolRepairLimit: 3}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "问一件事"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(resp.Text, "fabricated.example") {
		t.Fatalf("没检索到的链接被原样发出去了: %q", resp.Text)
	}
	if resp.Text != "这一点我没有查到可用来源。" {
		t.Fatalf("resp=%#v", resp)
	}
}

// 检索真正返回的来源当然可以引，别把正常引用也拦了。
func TestRunnerAllowsFinalCitingSearchedSource(t *testing.T) {
	searchResult, _ := json.Marshal(webSearchResult{
		Status: "ok", StopReason: "sufficient_evidence",
		Sources: []string{"https://real.example/a"}, Content: "检索到的真实资料",
	})
	tool := &recordingSearchTool{output: string(searchResult)}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search","input":{"query":"q","claims":[{"id":"c1","statement":"某事是否成立"}],"claim_ids":["c1"]}}`,
		`{"action":"final","content":"结论如此（来源：https://real.example/a）。后面还有一句中文。"}`,
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 3, ProtocolRepairLimit: 3}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "问一件事"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Text, "https://real.example/a") {
		t.Fatalf("检索到的来源被误拦: %q", resp.Text)
	}
}

// 用户自己贴的链接，模型复述不算编造来源。
func TestRunnerAllowsFinalCitingLinkFromConversation(t *testing.T) {
	searchResult, _ := json.Marshal(webSearchResult{
		Status: "ok", StopReason: "sufficient_evidence",
		Sources: []string{"https://real.example/a"}, Content: "资料",
	})
	tool := &recordingSearchTool{output: string(searchResult)}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search","input":{"query":"q","claims":[{"id":"c1","statement":"某事"}],"claim_ids":["c1"]}}`,
		`{"action":"final","content":"你发的那个 https://user-posted.example/page 我看过了。"}`,
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 3, ProtocolRepairLimit: 3}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "看下 https://user-posted.example/page 这个"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Text, "user-posted.example") {
		t.Fatalf("用户贴的链接被误拦: %q", resp.Text)
	}
}

// 本轮压根没检索时不做这项校验：闲聊里提一句网址不该被当成伪造来源。
func TestRunnerLeavesCitationsAloneWithoutSearch(t *testing.T) {
	tool := &recordingSearchTool{output: "unused"}
	client := &scriptedClient{responses: []string{
		`{"action":"final","content":"官网是 https://example.com 那个。"}`,
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 3, ProtocolRepairLimit: 3}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "他们官网是啥"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Text, "example.com") {
		t.Fatalf("没检索的轮次也被拦了: %q", resp.Text)
	}
}

func TestExtractCitationURLsStopsAtChineseText(t *testing.T) {
	// 用「非空白」匹配会把链接后面整句中文吞进来，导致检索到的来源也被判成没检索到。
	got := extractCitationURLs("见 https://a.example/b。另外 https://c.example/d（备用）还有一句。")
	want := []string{"https://a.example/b", "https://c.example/d"}
	if len(got) != len(want) {
		t.Fatalf("got=%#v want=%#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("got=%#v want=%#v", got, want)
		}
	}
}

// 检索过后的终稿复核：打回后的第二稿也复核，最多复核两次，第三稿放行。没检索过的轮次不复核。
func TestRunnerFinalReviewSendsBackUnsupportedNegationOnce(t *testing.T) {
	searchResult, _ := json.Marshal(webSearchResult{
		Status: "ok", StopReason: "sufficient_evidence",
		Sources: []string{"https://vendor.example/"}, Content: "官网首页列出 Model 6",
	})
	tool := &recordingSearchTool{output: string(searchResult)}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search","input":{"query":"Model 6.1 发布"}}`,
		`{"action":"final","content":"官网只有 Model 6，6.1 根本没发，你这图是 P 的。"}`,
		`{"action":"final","content":"我这边暂时没查到 6.1 的官方消息，可能还没收录。"}`,
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 3, ProtocolRepairLimit: 3}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	var reviewed []string
	var evidenceTools []string
	resp, err := runner.Run(context.Background(), Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "6.1 发布了"}},
		FinalReview: func(_ context.Context, draft string, evidence []Step) string {
			reviewed = append(reviewed, draft)
			for _, step := range evidence {
				evidenceTools = append(evidenceTools, step.Tool)
			}
			if strings.Contains(draft, "P 的") {
				return "复核发现：没查到不等于不存在"
			}
			return ""
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 第二稿同样过一遍复核，复核放行才发出去。
	if len(reviewed) != 2 || !strings.Contains(reviewed[0], "P 的") || !strings.Contains(reviewed[1], "没查到") {
		t.Fatalf("reviewed=%q", reviewed)
	}
	if len(evidenceTools) != 2 || evidenceTools[0] != webSearchToolName {
		t.Fatalf("evidence=%v", evidenceTools)
	}
	if !strings.Contains(resp.Text, "没查到") {
		t.Fatalf("第一稿被放行了: %q", resp.Text)
	}

	// 复核一直打回时最多复核两次，第三稿放行，不卡在修复循环里。
	client = &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search","input":{"query":"Model 6.1"}}`,
		`{"action":"final","content":"第一稿"}`,
		`{"action":"final","content":"第二稿"}`,
		`{"action":"final","content":"第三稿"}`,
	}}
	runner, err = NewRunner(client, Config{MaxSteps: 3, ProtocolRepairLimit: 3}, NewToolRegistry(&recordingSearchTool{output: string(searchResult)}))
	if err != nil {
		t.Fatal(err)
	}
	rejections := 0
	resp, err = runner.Run(context.Background(), Request{
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "6.1 发布了"}},
		FinalReview: func(context.Context, string, []Step) string { rejections++; return "打回" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if rejections != maxFinalReviewsPerAgentRun || resp.Text != "第三稿" {
		t.Fatalf("rejections=%d text=%q", rejections, resp.Text)
	}

	client = &scriptedClient{responses: []string{`{"action":"final","content":"没有这回事"}`}}
	runner, err = NewRunner(client, Config{MaxSteps: 3, ProtocolRepairLimit: 3}, NewToolRegistry(&recordingSearchTool{output: string(searchResult)}))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	if _, err := runner.Run(context.Background(), Request{
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "x"}},
		FinalReview: func(context.Context, string, []Step) string { calls++; return "打回" },
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("没检索过也去复核了: calls=%d", calls)
	}
}

type nativeReviewDraftClient struct{ requests []llm.GenerateRequest }

func (c *nativeReviewDraftClient) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.requests = append(c.requests, req)
	if len(c.requests) == 1 {
		return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{ID: "search", Name: WebSearchToolName, Arguments: map[string]any{"query": "feature"}}}}, nil
	}
	draft := "未验证的具体断言"
	if len(c.requests) > 2 {
		draft = "该具体能力本次未确认"
	}
	return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{ID: "final", Name: finalizeToolName, Arguments: map[string]any{"content": draft}}}}, nil
}
func TestRunnerFinalReviewPreservesNativeDraftForRevision(t *testing.T) {
	for _, maxSteps := range []int{1, 3} {
		t.Run(fmt.Sprintf("steps_%d", maxSteps), func(t *testing.T) {
			client := &nativeReviewDraftClient{}
			runner, err := NewRunner(client, Config{MaxSteps: maxSteps}, NewToolRegistry(&recordingSearchTool{output: `{"status":"ok","content":"项目介绍","sources":["https://example.com/docs"]}`}))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "查能力"}}, FinalReview: func(_ context.Context, draft string, _ []Step) string {
				if draft == "未验证的具体断言" {
					return "删掉这条缺证据的断言"
				}
				return ""
			}})
			if err != nil {
				t.Fatal(err)
			}
			if resp.Text != "该具体能力本次未确认" {
				t.Fatalf("答复=%s", resp.Text)
			}
			found := false
			for _, m := range client.requests[2].Messages {
				if m.Role == llm.RoleAssistant && m.Content == "未验证的具体断言" {
					found = true
				}
			}
			if !found {
				t.Fatal("原生 finalize 的草稿丢失，下一轮看不到要修改的正文")
			}
		})
	}
}
