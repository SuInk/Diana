// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

const renderFinalizeTestTable = "| 名次 | 模型 | 得分 |\n|---|---|---:|\n| 1 | A | 100% |\n| 2 | B | 96% |"

// renderFinalizeLLMProvider 在收尾时按需填 render；sawField 记下收尾工具是否带了这个字段。
type renderFinalizeLLMProvider struct {
	capturingLLMProvider
	table    string
	sawField bool
}

func (p *renderFinalizeLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	response, err := p.capturingLLMProvider.Generate(ctx, req)
	if err != nil || response.Text != p.reply {
		return response, err
	}
	for _, tool := range req.Tools {
		if tool.Name != "agent_finalize" {
			continue
		}
		properties, _ := tool.Parameters["properties"].(map[string]any)
		_, p.sawField = properties[renderFinalizeFieldName]
		arguments := map[string]any{"content": response.Text}
		if p.sawField && p.table != "" {
			arguments[renderFinalizeFieldName] = p.table
		}
		response.ToolCalls = []llm.ToolCall{{ID: "finalize", Name: tool.Name, Arguments: arguments}}
		response.Text = ""
	}
	return response, nil
}

func stubFinalizeRenderPNG(t *testing.T, png []byte, err error) *[]string {
	t.Helper()
	var rendered []string
	previous := finalizeRenderPNG
	finalizeRenderPNG = func(_ *Runtime, _ context.Context, _ MessageEvent, format, content, _ string) ([]byte, error) {
		rendered = append(rendered, format+":"+content)
		return png, err
	}
	t.Cleanup(func() { finalizeRenderPNG = previous })
	return &rendered
}

func renderFinalizeRuntime(t *testing.T, provider *renderFinalizeLLMProvider, browser bool) (*Runtime, *recordingChannel) {
	t.Helper()
	channel := &recordingChannel{}
	// 默认插件表里「网页渲染」是开着的；空插件表等于它没装。
	plugins := NewPluginManager()
	if browser {
		plugins = NewDefaultPluginManager()
	}
	rt := NewRuntime(BotConfig{AgentEnabled: true}.WithDefaults(), channel, plugins, nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	return rt, channel
}

// 模型收尾时填了表格：正文先发，紧跟一张表格图；表格内容不进正文。
func TestReplyFinalizeRenderFollowsText(t *testing.T) {
	withFastSendTiming(t)
	rendered := stubFinalizeRenderPNG(t, []byte("png"), nil)
	provider := &renderFinalizeLLMProvider{capturingLLMProvider: capturingLLMProvider{reply: "前几名见榜单图"}, table: renderFinalizeTestTable}
	rt, channel := renderFinalizeRuntime(t, provider, true)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", MessageID: "rank", RawMessage: "来个榜单看看"}
	if _, err := rt.replyTo(context.Background(), event, event.RawMessage); err != nil {
		t.Fatal(err)
	}
	if !provider.sawField {
		t.Fatal("agent_finalize did not offer the render field")
	}
	if len(*rendered) != 1 || (*rendered)[0] != renderFormatMarkdown+":"+renderFinalizeTestTable {
		t.Fatalf("rendered = %#v", *rendered)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 2 || !strings.Contains(sent[0].Text, "前几名见榜单图") || strings.Contains(sent[0].Text, "96%") || len(sent[1].ImageURLs) != 1 {
		t.Fatalf("sent = %#v", sent)
	}
}

// 画不出来时表格退回正文发文字，数据不能跟着图一起丢。
func TestReplyFinalizeRenderFailureFallsBackToText(t *testing.T) {
	withFastSendTiming(t)
	stubFinalizeRenderPNG(t, nil, errors.New("chrome missing"))
	provider := &renderFinalizeLLMProvider{capturingLLMProvider: capturingLLMProvider{reply: "前几名如下"}, table: renderFinalizeTestTable}
	rt, channel := renderFinalizeRuntime(t, provider, true)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", MessageID: "rank", RawMessage: "来个榜单看看"}
	if _, err := rt.replyTo(context.Background(), event, event.RawMessage); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, message := range channel.sentSnapshot() {
		if len(message.ImageURLs) != 0 {
			t.Fatalf("image sent after render failure: %#v", message)
		}
		text.WriteString(message.Text + "\n")
	}
	if !strings.Contains(text.String(), "前几名如下") || !strings.Contains(text.String(), "2 | B | 96%") || strings.Contains(text.String(), "---") {
		t.Fatalf("table lost on fallback: %q", text.String())
	}
}

// 「网页渲染」插件关着时画不出来，就不给模型这个字段。
func TestReplyFinalizeRenderFieldNeedsBrowserPlugin(t *testing.T) {
	withFastSendTiming(t)
	rendered := stubFinalizeRenderPNG(t, []byte("png"), nil)
	provider := &renderFinalizeLLMProvider{capturingLLMProvider: capturingLLMProvider{reply: "好的"}, table: renderFinalizeTestTable}
	rt, channel := renderFinalizeRuntime(t, provider, false)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", MessageID: "hi", RawMessage: "在吗"}
	if _, err := rt.replyTo(context.Background(), event, event.RawMessage); err != nil {
		t.Fatal(err)
	}
	if provider.sawField || len(*rendered) != 0 || len(channel.sentSnapshot()) != 1 {
		t.Fatalf("sawField=%v rendered=%v sent=%#v", provider.sawField, *rendered, channel.sentSnapshot())
	}
}

// 没有发送挂点的调用路径（事件触发、后台任务）不出图，表格直接接在正文后面另起一条。
func TestApplyFinalizeRenderWithoutHolderAppendsTable(t *testing.T) {
	rendered := stubFinalizeRenderPNG(t, []byte("png"), nil)
	rt := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	got := rt.applyFinalizeRender(context.Background(), MessageEvent{}, "结论", renderFinalizeTestTable)
	want := "结论[diana-msg]名次 | 模型 | 得分[diana-line]1 | A | 100%[diana-line]2 | B | 96%"
	if got != want || len(*rendered) != 0 {
		t.Fatalf("got %q rendered=%v", got, *rendered)
	}
	if got := rt.applyFinalizeRender(context.Background(), MessageEvent{}, "结论", "  "); got != "结论" {
		t.Fatalf("empty table changed text: %q", got)
	}
}
