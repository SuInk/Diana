// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// asideClient 每一步发回一段正文和一组原生调用，用完后只 send「好了」。
type asideClient struct {
	texts    []string
	steps    [][]llm.ToolCall
	requests int
}

func (c *asideClient) Generate(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error) {
	index := c.requests
	c.requests++
	if index < len(c.steps) {
		return &llm.GenerateResponse{Text: c.texts[index], ToolCalls: c.steps[index]}, nil
	}
	return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{ID: "send-last", Name: finalizeToolName, Arguments: map[string]any{"text": "好了"}}}}, nil
}

func searchCall(id, query string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: WebSearchToolName, Arguments: map[string]any{"query": query}}
}

// 检索工具旁边写的短话先当中途发言发出，再跑检索；后面换个说法报进度也照发，
// 同一句不发第二遍。
func TestAsideTextBesideSearchSentAsInterim(t *testing.T) {
	var events []string
	client := &asideClient{
		texts: []string{"我去查一下", "我去查一下", "这页被挡了，换个来源"},
		steps: [][]llm.ToolCall{{searchCall("s1", "a")}, {searchCall("s2", "b")}, {searchCall("s3", "c")}},
	}
	search := &orderTool{name: WebSearchToolName, events: &events}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 6, Interim: interimRecorder(&events)}, NewToolRegistry(search))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "帮我查查"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := "send:我去查一下|run:web_search|run:web_search|send:这页被挡了，换个来源|run:web_search"
	if got := strings.Join(events, "|"); got != want || resp.Text != "好了" {
		t.Fatalf("events = %q, text = %q", got, resp.Text)
	}
}

// 不发的情况：带副作用的工具、同一步已经调了 send、长段复盘、括号动作描述、内部标记。
func TestAsideTextNotSentWhenUnsafe(t *testing.T) {
	long := strings.Repeat("上一条我凭印象断言了", 7)
	for name, tc := range map[string]struct {
		text  string
		calls []llm.ToolCall
		want  string
	}{
		"side effect":  {text: "我去定个提醒", calls: []llm.ToolCall{{ID: "r1", Name: "remind", Arguments: map[string]any{}}}, want: "run:remind"},
		"send present": {text: "旁白", calls: []llm.ToolCall{{ID: "x", Name: finalizeToolName, Arguments: map[string]any{"text": "我去查"}}, searchCall("s1", "a")}, want: "send:我去查|run:web_search"},
		"long":         {text: long, calls: []llm.ToolCall{searchCall("s1", "a")}, want: "run:web_search"},
		"bracket":      {text: "[搜索 参展商名单]", calls: []llm.ToolCall{searchCall("s1", "a")}, want: "run:web_search"},
		"marker":       {text: "diana-finalize 不需要补充", calls: []llm.ToolCall{searchCall("s1", "a")}, want: "run:web_search"},
		"multi line":   {text: "我去查\n顺便看看", calls: []llm.ToolCall{searchCall("s1", "a")}, want: "run:web_search"},
	} {
		t.Run(name, func(t *testing.T) {
			var events []string
			client := &asideClient{texts: []string{tc.text}, steps: [][]llm.ToolCall{tc.calls}}
			registry := NewToolRegistry(&orderTool{name: WebSearchToolName, events: &events}, &orderTool{name: "remind", events: &events})
			runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 4, Interim: interimRecorder(&events)}, registry)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "帮我查查"}}}); err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(events, "|"); got != tc.want {
				t.Fatalf("events = %q, want %q", got, tc.want)
			}
		})
	}
}
