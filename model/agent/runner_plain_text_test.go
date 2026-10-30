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
		"":                             true,
		"[搜索 WePlay 2026 参展商名单]":       true,
		"【查询天气】":                       true,
		"腿子不发，想要图自己找去~":                false,
		"我查一下":                         false,
		"[diana-reply:im_message_1]好呀": false,
		"[1] 第一条 [2] 第二条":              false,
		"（笑）这个还行":                      false,
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

// plainThenSendClient 先按 replies 依次回纯文本，用完后回一个 send 调用。
type plainThenSendClient struct {
	replies  []string
	send     map[string]any
	requests []llm.GenerateRequest
	sendErr  error
}

func (c *plainThenSendClient) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.requests = append(c.requests, req)
	if index := len(c.requests) - 1; index < len(c.replies) {
		return &llm.GenerateResponse{Text: c.replies[index]}, nil
	}
	if c.sendErr != nil {
		return nil, c.sendErr
	}
	return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{ID: "call-send", Name: finalizeToolName, Arguments: c.send}}}, nil
}

// send 带附加字段（表情包）时纯文本收尾也直接放行，不为补字段打回重写。
func TestRunnerPlainTextAcceptedEvenWhenSendHasExtraFields(t *testing.T) {
	sticker := FinalizeField{Name: "sticker", Description: "配表情包的关键词"}
	for name, fields := range map[string][]FinalizeField{"no extra fields": nil, "with sticker": {sticker}} {
		t.Run(name, func(t *testing.T) {
			client := &plainThenSendClient{replies: []string{"好耶"}, send: map[string]any{"text": "好耶", "sticker": "得意 叉腰"}}
			runner, err := NewRunner(client, Config{MaxSteps: 4, FinalizeFields: fields}, NewToolRegistry())
			if err != nil {
				t.Fatal(err)
			}
			resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "夸夸我"}}})
			if err != nil {
				t.Fatal(err)
			}
			if len(client.requests) != 1 || resp.Text != "好耶" || resp.FinishReason != "plain_text" || len(resp.FinalizeFields) != 0 {
				t.Fatalf("requests=%d response=%#v", len(client.requests), resp)
			}
		})
	}
}
