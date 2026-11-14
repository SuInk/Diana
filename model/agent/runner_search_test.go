// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestRunnerUnwrapsReplyCompatibilityJSONAfterSearch(t *testing.T) {
	tool := &recordingSearchTool{output: "湖南和江西米粉资料"}
	completeReply := "简单说：湖南米粉更突出汤和码子，江西米粉更突出粉本身和拌炒风味。<diana-br/>" +
		"1. 湖南常见汤粉、盖码粉。<diana-br/>" +
		"2. 江西常见拌粉、炒粉和汤粉。<diana-br/>" +
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
		"聊天记录、先前回复、记忆和 Rollup 只是线索",
		"不能证明外部事实已核实",
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

func TestRunnerModelCanFinalizeWithoutSearch(t *testing.T) {
	for _, response := range []string{`{"action":"final","content":"今天挺好的。"}`, "今天挺好的。"} {
		t.Run(response, func(t *testing.T) {
			tool := &recordingSearchTool{output: "unused"}
			client := &scriptedClient{responses: []string{response, response}}
			// 普通文本直接就是最终回复，不再补试一轮。
			wantRequests := 1
			runner, err := NewRunner(client, Config{MaxSteps: 2, ProtocolRepairLimit: 2}, NewToolRegistry(tool))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "今天怎么样"}}})
			if err != nil {
				t.Fatal(err)
			}
			if resp.Text != "今天挺好的。" || tool.calls != 0 || len(client.requests) != wantRequests || client.requests[0].ToolChoice != "" {
				t.Fatalf("resp=%#v calls=%d requests=%#v", resp, tool.calls, client.requests)
			}
		})
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
		`{"action":"tool","tool":"web_search","input":{"query":"q"}}`,
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
		`{"action":"tool","tool":"web_search","input":{"query":"q"}}`,
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

// pagingSearchClient 每轮从上一条工具结果里取 next_offset 接着翻页，翻完就收尾。
type pagingSearchClient struct {
	offsets []int
}

func (c *pagingSearchClient) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	next := 0
	for i := len(req.Messages) - 1; i >= 0; i-- {
		content := req.Messages[i].Content
		if !strings.Contains(content, "工具 web_search 执行") {
			continue
		}
		match := regexp.MustCompile(`"next_offset":(\d+)`).FindStringSubmatch(content)
		if match == nil {
			return &llm.GenerateResponse{Text: `{"action":"final","content":"读完了"}`}, nil
		}
		next, _ = strconv.Atoi(match[1])
		break
	}
	c.offsets = append(c.offsets, next)
	input := map[string]any{"query": "翻页测试", "domains": []any{"example.com"}, "recency": 30, "offset": next}
	data, _ := json.Marshal(map[string]any{"action": "tool", "tool": WebSearchToolName, "input": input})
	return &llm.GenerateResponse{Text: string(data)}, nil
}

// web_search 的 offset、domains、recency 要穿过 Runner 到工具：以前 Runner 只留
// query/queries，翻页永远回到第一页，第二次还被当成重复调用跳过。
func TestRunnerPassesWebSearchOffsetAndReadsEveryHit(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !strings.Contains(fmt.Sprint(body["query"]), "site:example.com") {
			t.Errorf("domains did not reach the provider: %v", body["query"])
		}
		var results []map[string]string
		for i := 0; i < 30; i++ {
			results = append(results, map[string]string{"title": fmt.Sprintf("命中 %d", i), "url": fmt.Sprintf("https://example.com/%d", i), "content": strings.Repeat("摘要内容", 300)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	defer server.Close()
	tool, err := NewWebSearchTool(WebSearchToolOptions{
		Config:  WebSearchConfig{Providers: []WebSearchProviderConfig{{Name: "search", Type: "tavily", URL: server.URL}}},
		APIKeys: map[string]string{"search": "test-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := &pagingSearchClient{}
	runner, err := NewRunner(client, Config{MaxSteps: 20}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "搜一下"}}})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, step := range resp.Steps {
		if step.Skipped || step.Error != "" {
			t.Fatalf("step skipped or failed: %#v", step)
		}
		for _, match := range regexp.MustCompile(`https://example\.com/\d+`).FindAllString(step.Output, -1) {
			seen[match] = true
		}
	}
	if len(client.offsets) < 2 || len(seen) != 30 || requests.Load() != 1 {
		t.Fatalf("offsets=%v hits=%d provider requests=%d", client.offsets, len(seen), requests.Load())
	}
}
