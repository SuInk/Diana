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
