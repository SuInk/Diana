// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

func TestPartialJSONStringField(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
		ok        bool
	}{
		{``, "", false},
		{`{"silent":false,"te`, "", false},
		{`{"text"`, "", false},
		{`{"text":`, "", false},
		{`{"text":"`, "", true},
		{`{"text":"你好`, "你好", true},
		{`{"text":"第一行\n第二`, "第一行\n第二", true},
		{`{"text":"a\`, "a", true},
		{`{"text":"a\u4f`, "a", true},
		{`{"text":"a你b`, "a你b", true},
		{`{"text":"a\ud83d\ude`, "a", true},
		{`{"text":"a😀"}`, "a😀", true},
		{`{"silent":false,"sticker":"","task_state":null,"text":"嗯`, "嗯", true},
		{`{"extra":{"text":"内层","n":[1,"]"]},"text":"外层`, "外层", true},
		{`{"silent":tru`, "", false},
		{`{"text":123}`, "", false},
	} {
		got, ok := partialJSONStringField(tc.raw, "text")
		if got != tc.want || ok != tc.ok {
			t.Errorf("partialJSONStringField(%q) = %q, %v; want %q, %v", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}

func sendInputProgress(id, arguments string) llm.ChatEvent {
	return llm.ChatEvent{Type: llm.ChatEventProgress, ToolInput: &llm.ToolInputProgress{ID: id, Name: agent.FinalizeToolName, Arguments: arguments}}
}

// 线上 TG 私聊：正文走 send，草稿却只接普通文本，推出去的是会被退回的纯文本和复核
// 模型的 JSON，露出来又消失。带 send 的请求只认 send 的 text。
func TestReplyPreviewStreamsSendTextNotPlainText(t *testing.T) {
	observer := &recordingDeltaObserver{}
	ctx := withLLMUsagePurpose(withTextDeltaObserver(context.Background(), observer), PurposeReply)
	req := llm.GenerateRequest{Tools: []llm.ToolDefinition{{Name: agent.FinalizeToolName}, {Name: "tools_execute"}}}
	events := make(chan llm.ChatEvent, 8)
	for _, event := range []llm.ChatEvent{
		textDelta("我先查一下"),
		{Type: llm.ChatEventProgress, ToolInput: &llm.ToolInputProgress{ID: "other", Name: "tools_execute", Arguments: `{"text":"不是正文"}`}},
		sendInputProgress("fc_1", `{"silent":false,"text":"你`),
		sendInputProgress("fc_2", `{"text":"第二个 send"}`),
		sendInputProgress("fc_1", `{"silent":false,"text":"你好"}`),
		{Type: llm.ChatEventDone},
	} {
		events <- event
	}
	close(events)
	if _, err := accumulateChatEvents(ctx, req, events); err != nil {
		t.Fatal(err)
	}
	if len(observer.texts) != 2 || observer.texts[0] != "你" || observer.texts[1] != "你好" {
		t.Fatalf("previewed %q", observer.texts)
	}
}

// 复核、审核这类旁路调用和主回复共用 ctx，输出只给程序读，不能推成草稿。
func TestReplyPreviewSkipsHiddenAndSideCalls(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"hidden":  llm.WithHiddenOutput(withLLMUsagePurpose(context.Background(), PurposeReply)),
		"purpose": withLLMUsagePurpose(context.Background(), "reply_send_audit"),
	} {
		t.Run(name, func(t *testing.T) {
			observer := &recordingDeltaObserver{}
			events := make(chan llm.ChatEvent, 2)
			events <- textDelta(`{"unfinished":"","missing":""}`)
			events <- llm.ChatEvent{Type: llm.ChatEventDone}
			close(events)
			if _, err := accumulateChatEvents(withTextDeltaObserver(ctx, observer), llm.GenerateRequest{}, events); err != nil {
				t.Fatal(err)
			}
			if len(observer.texts) != 0 {
				t.Fatalf("side call previewed %q", observer.texts)
			}
		})
	}
}

// 不走 Agent 的回复没有 send，正文就是普通文本，照旧推草稿。
func TestReplyPreviewStreamsPlainTextWithoutSendTool(t *testing.T) {
	observer := &recordingDeltaObserver{}
	ctx := withLLMUsagePurpose(withTextDeltaObserver(context.Background(), observer), PurposeReply)
	events := make(chan llm.ChatEvent, 2)
	events <- textDelta("你好")
	events <- llm.ChatEvent{Type: llm.ChatEventDone}
	close(events)
	if _, err := accumulateChatEvents(ctx, llm.GenerateRequest{}, events); err != nil {
		t.Fatal(err)
	}
	if len(observer.texts) != 1 || observer.texts[0] != "你好" {
		t.Fatalf("previewed %q", observer.texts)
	}
}

type countingDraftChannel struct{ texts []string }

func (c *countingDraftChannel) SendTextDraft(_ context.Context, msg OutgoingMessage, _ int64) error {
	c.texts = append(c.texts, msg.Text)
	return nil
}

// QQ 私聊流式的帧就是正式消息，send 的正文还没过复核，不能流过去。
func TestReplyPreviewSkipsSendTextOnQQStream(t *testing.T) {
	channel := &countingDraftChannel{}
	draft := &replyDraft{channel: channel, qqStream: true, maxRunes: replyDraftMaxRunes}
	ctx := withLLMUsagePurpose(withTextDeltaObserver(context.Background(), draft), PurposeReply)
	events := make(chan llm.ChatEvent, 3)
	events <- textDelta("普通文本也不推")
	events <- sendInputProgress("fc_1", `{"text":"这句还没过复核，不能先发"}`)
	events <- llm.ChatEvent{Type: llm.ChatEventDone}
	close(events)
	req := llm.GenerateRequest{Tools: []llm.ToolDefinition{{Name: agent.FinalizeToolName}}}
	if _, err := accumulateChatEvents(ctx, req, events); err != nil {
		t.Fatal(err)
	}
	if len(channel.texts) != 0 {
		t.Fatalf("qq stream published %q", channel.texts)
	}
}
