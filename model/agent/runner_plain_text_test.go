// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestRunnerPlainTextContinuesUnfinishedWork(t *testing.T) {
	for _, afterTool := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_tools", true: "after_tool"}[afterTool], func(t *testing.T) {
			responses := []string{}
			if afterTool {
				responses = append(responses, `{"action":"tool","tool":"web_search","input":{"query":"WePlay 2026"}}`)
			}
			responses = append(responses, "[搜索 WePlay 2026 参展商名单]",
				`{"action":"tool","tool":"web_search","input":{"query":"WePlay 2026 参展商名单"}}`,
				`{"action":"final","content":"已核实的展商信息"}`)
			client := &scriptedClient{responses: responses}
			tool := &recordingSearchTool{output: "已核实的展商信息"}
			runner, err := NewRunner(client, Config{MaxSteps: 4, ProtocolRepairLimit: 1}, NewToolRegistry(tool))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "有哪些游戏展商"}}})
			if err != nil {
				t.Fatal(err)
			}
			wantCalls := 1
			if afterTool {
				wantCalls++
			}
			if tool.calls != wantCalls || resp.Text != "已核实的展商信息" || resp.FinishReason != "final" || resp.ModelTurns != wantCalls+2 {
				t.Fatalf("response=%#v tool calls=%d", resp, tool.calls)
			}
			retry := client.requests[wantCalls].Messages
			if retry[len(retry)-2].Role != llm.RoleAssistant || retry[len(retry)-2].Content != "[搜索 WePlay 2026 参展商名单]" || !strings.Contains(retry[len(retry)-1].Content, "不要重复已经成功执行的操作") {
				t.Fatal("retry did not preserve the response and continuation guidance")
			}
		})
	}
}

// 只回放普通文本后的模型决策，不执行外部工具、不发群消息。
// 私有 bundle 保留生产 request/response；模型配置由 liveAgentClient 注入。
func TestLivePlainTextTraceContinuation(t *testing.T) {
	path := os.Getenv("DIANA_PLAIN_TEXT_REPLAY_BUNDLE")
	if path == "" {
		t.Skip("set DIANA_PLAIN_TEXT_REPLAY_BUNDLE to a private production trace bundle")
	}
	live := liveAgentClient(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var bundle struct {
		Request  llm.GenerateRequest  `json:"request"`
		Response llm.GenerateResponse `json:"response"`
	}
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Request.Messages) < 2 {
		t.Fatal("trace has no conversation")
	}
	client := &plainTextReplayClient{live: live, recorded: &bundle.Response, tools: bundle.Request.Tools}
	runner, err := NewRunner(client, Config{MaxSteps: 4}, NewToolRegistry(&recordingSearchTool{output: "回放不执行搜索"}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	_, err = runner.Run(ctx, Request{Messages: bundle.Request.Messages[1:]})
	if err != nil {
		t.Fatal(err)
	}
	if client.resumed == nil || len(client.resumed.ToolCalls) == 0 {
		t.Fatalf("model did not resume tool calling: %+v", client.resumed)
	}
	for _, call := range client.resumed.ToolCalls {
		t.Logf("model=%s resumed tool=%s", client.resumed.Model, call.Name)
	}
}

type plainTextReplayClient struct {
	live     LLMClient
	recorded *llm.GenerateResponse
	resumed  *llm.GenerateResponse
	tools    []llm.ToolDefinition
	calls    int
}

func (c *plainTextReplayClient) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.calls++
	if c.calls == 1 {
		return c.recorded, nil
	}
	if c.calls == 2 {
		req.Tools = c.tools
		resp, err := c.live.Generate(ctx, req)
		c.resumed = resp
		// 只验证真实续跑决策，随后用固定收尾终止隔离回放，不执行返回的工具。
		if err != nil {
			return nil, err
		}
		return &llm.GenerateResponse{Text: `{"action":"final","content":"回放结束"}`}, nil
	}
	return &llm.GenerateResponse{Text: `{"action":"final","content":"回放结束"}`}, nil
}

func TestPlainTextNeedsRepairOnlyForBareActions(t *testing.T) {
	for text, want := range map[string]bool{
		"":                       true,
		"[搜索 WePlay 2026 参展商名单]": true,
		"【查询天气】":                 true,
		"腿子不发，想要图自己找去~":          false,
		"我查一下":                   false,
		"<diana-quote id=\"im_message_1\"/>好呀": false,
		"[1] 第一条 [2] 第二条":                      false,
		"（笑）这个还行":                              false,
	} {
		if got := plainTextNeedsRepair(text); got != want {
			t.Errorf("plainTextNeedsRepair(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestRunnerPlainTextRetryIsBoundedAcrossTools(t *testing.T) {
	client := &scriptedClient{responses: []string{
		"[查一下 WePlay]", `{"action":"tool","tool":"web_search","input":{"query":"WePlay"}}`, "暂未查到完整名单",
	}}
	tool := &recordingSearchTool{output: "暂无完整名单"}
	runner, err := NewRunner(client, Config{MaxSteps: 4}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "查一下 WePlay 展商"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ModelTurns != 3 || resp.FinishReason != "plain_text" || resp.Text != "暂未查到完整名单" || tool.calls != 1 {
		t.Fatalf("response=%#v calls=%d", resp, tool.calls)
	}
}

// 普通纯文本直接作为最终回复，不进补试，也就不会被续跑提示带偏成「更正如下」。
// 只有空文本或整段括号的占位动作才补试，它们本身没有可沿用的内容，用第二遍的回答。
func TestRunnerPlainTextRetryOnlyForPlaceholders(t *testing.T) {
	for _, tc := range []struct {
		first, second, want string
		turns               int
	}{
		{"我这边现在出不了，签发链还没定", "[对方指出我回复的方向不对，更正如下：] [DAIAN_REPLY_SINGLE]你问得挺快", "我这边现在出不了，签发链还没定", 1},
		{"[搜索 WePlay 参展商名单]", "暂时没查到完整名单", "暂时没查到完整名单", 2},
		{"【查询天气】", "今天晴", "今天晴", 2},
		{"", "今天晴", "今天晴", 2},
	} {
		client := &scriptedClient{responses: []string{tc.first, tc.second}}
		runner, err := NewRunner(client, Config{MaxSteps: 4}, NewToolRegistry(&recordingSearchTool{}))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "现在能出吗"}}})
		if err != nil {
			t.Fatal(err)
		}
		if resp.ModelTurns != tc.turns || resp.FinishReason != "plain_text" || resp.Text != tc.want {
			t.Fatalf("first=%q: response=%#v", tc.first, resp)
		}
	}
}

// plainTextClient 每次都只回同一段纯文本，记下收到的请求数。
type plainTextClient struct {
	text     string
	requests int
}

func (c *plainTextClient) Generate(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.requests++
	return &llm.GenerateResponse{Text: c.text}, nil
}

// send 带表情包这类附加字段时，纯文本同样直接当最终回复，不为补字段打回重写。
func TestRunnerPlainTextAcceptedWhenSendHasExtraFields(t *testing.T) {
	sticker := FinalizeField{Name: "sticker", Description: "配表情包的关键词"}
	for name, fields := range map[string][]FinalizeField{"no extra fields": nil, "sticker": {sticker}} {
		t.Run(name, func(t *testing.T) {
			client := &plainTextClient{text: "好耶"}
			runner, err := NewRunner(client, Config{MaxSteps: 4, FinalizeFields: fields}, NewToolRegistry())
			if err != nil {
				t.Fatal(err)
			}
			resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "夸夸我"}}})
			if err != nil {
				t.Fatal(err)
			}
			if client.requests != 1 || resp.Text != "好耶" || resp.FinishReason != "plain_text" {
				t.Fatalf("requests=%d response=%#v", client.requests, resp)
			}
		})
	}
}

// mixedReplyClient 按顺序回预先写好的响应：纯文本或原生调用。
type mixedReplyClient struct {
	responses []*llm.GenerateResponse
	requests  []llm.GenerateRequest
}

func (c *mixedReplyClient) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.requests = append(c.requests, req)
	next := c.responses[0]
	if len(c.responses) > 1 {
		c.responses = c.responses[1:]
	}
	return next, nil
}

// 主回复只认 send：纯文本先退回一次，退回后用 send 发的就是最终回复。
func TestRunnerPlainTextNotSentBouncesOnce(t *testing.T) {
	client := &mixedReplyClient{responses: []*llm.GenerateResponse{
		{Text: "在画了，画好自动发出来"},
		{ToolCalls: []llm.ToolCall{{ID: "s1", Name: finalizeToolName, Arguments: map[string]any{"text": "好，这就画"}}}},
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 4, PlainTextNotSent: true}, NewToolRegistry(&recordingSearchTool{}))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "画一个我的"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Silent || resp.Text != "好，这就画" || len(client.requests) != 2 {
		t.Fatalf("requests=%d response=%#v", len(client.requests), resp)
	}
	second := client.requests[1].Messages
	if len(second) < 2 || second[len(second)-2].Content != "在画了，画好自动发出来" || second[len(second)-1].Content != plainTextNotSentPrompt {
		t.Fatalf("bounce not appended: %#v", second)
	}
}

// 退回后还是纯文本就复核一次：说了却没做（一个工具没调就说在画）按静默结束，原因写进
// SilentReason；占位文本的那次修复也算退回过，不会退两次。
func TestRunnerPlainTextNotSentDropsUnfinishedSecondPlainText(t *testing.T) {
	for _, first := range []string{"在画了，画好自动发出来", "[搜索 WePlay 参展商名单]"} {
		client := &mixedReplyClient{responses: []*llm.GenerateResponse{{Text: first}, {Text: "马上就画好"}, {Text: `{"unfinished":"马上就画好"}`}}}
		runner, err := NewRunner(client, Config{MaxSteps: 4, PlainTextNotSent: true}, NewToolRegistry(&recordingSearchTool{}))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "画一个我的"}}})
		if err != nil {
			t.Fatal(err)
		}
		if !resp.Silent || resp.Text != "" || resp.FinishReason != "plain_text_not_sent" || resp.SilentReason != plainTextUnfinishedReason+"马上就画好" || len(client.requests) != 3 {
			t.Fatalf("first=%q requests=%d response=%#v", first, len(client.requests), resp)
		}
		review := client.requests[2]
		if len(review.Tools) != 0 || !strings.Contains(review.Messages[1].Content, "本轮没有调用任何工具") || !strings.Contains(review.Messages[1].Content, "画一个我的") {
			t.Fatalf("review request = %#v", review)
		}
	}
}

// 退回后还是纯文本、复核没发现说了没做的：照常发出。11-08 生产里这样被吞的全是
// 正常回复（接话、总结、查完资料的结论）。复核输出看不懂时同样放行。
func TestRunnerPlainTextNotSentSendsCheckedSecondPlainText(t *testing.T) {
	for _, verdict := range []string{`{"unfinished":""}`, "看不懂的输出"} {
		client := &mixedReplyClient{responses: []*llm.GenerateResponse{{Text: "这扭得，看来是加上了"}, {Text: "这扭得可真欢，看来好友加上了"}, {Text: verdict}}}
		runner, err := NewRunner(client, Config{MaxSteps: 4, PlainTextNotSent: true}, NewToolRegistry(&recordingSearchTool{}))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "[图片]"}}})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Silent || resp.Text != "这扭得可真欢，看来好友加上了" || resp.FinishReason != "final" || len(client.requests) != 3 {
			t.Fatalf("verdict=%q requests=%d response=%#v", verdict, len(client.requests), resp)
		}
	}
}

// 退回后写的是空文本：没有可复核的正文，直接静默，不多花一次复核调用。
func TestRunnerPlainTextNotSentDropsEmptySecondPlainText(t *testing.T) {
	client := &mixedReplyClient{responses: []*llm.GenerateResponse{{Text: "在画了"}, {Text: "  "}}}
	runner, err := NewRunner(client, Config{MaxSteps: 4, PlainTextNotSent: true}, NewToolRegistry(&recordingSearchTool{}))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "画一个我的"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Silent || resp.SilentReason != plainTextNotSentReason || len(client.requests) != 2 {
		t.Fatalf("requests=%d response=%#v", len(client.requests), resp)
	}
}

// 没开 PlainTextNotSent 的调用方（子任务等）照旧拿纯文本当结果。
func TestRunnerPlainTextKeptWithoutPlainTextNotSent(t *testing.T) {
	client := &mixedReplyClient{responses: []*llm.GenerateResponse{{Text: "子任务结论"}}}
	runner, err := NewRunner(client, Config{MaxSteps: 4}, NewToolRegistry(&recordingSearchTool{}))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "查一下"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Silent || resp.Text != "子任务结论" || resp.FinishReason != "plain_text" || len(client.requests) != 1 {
		t.Fatalf("requests=%d response=%#v", len(client.requests), resp)
	}
}

// 拼历史用的发言调用：一条 send 调用配一条「已发送」，调用 ID 带历史前缀。
func TestHistoryReplyMessagesPairsCallAndResult(t *testing.T) {
	messages := HistoryReplyMessages("abc", " 早上好 ")
	if len(messages) != 2 || !llm.IsHistoryReplyMessage(messages[0]) || !llm.IsHistoryReplyMessage(messages[1]) {
		t.Fatalf("messages = %#v", messages)
	}
	call := messages[0].ToolCalls[0]
	if call.Name != finalizeToolName || sendText(call) != "早上好" || messages[1].ToolCallID != call.ID || messages[1].Content != historyReplyResult {
		t.Fatalf("messages = %#v", messages)
	}
	if HistoryReplyMessages("abc", "  ") != nil {
		t.Fatal("empty reply must render nothing")
	}
}
