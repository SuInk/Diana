// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 线上真实事故：模型没走 function calling，把 agent_finalize 信封当普通文本输出，
// 整段 JSON 被原样发到群里。这里锁住「信封在正文位置也要按收尾解码」。
func TestRunnerDecodesFinalizeEnvelopeEmittedAsPlainText(t *testing.T) {
	client := &scriptedClient{responses: []string{
		`{"content":"主人是然然最亲近的人呀。[diana-msg]你的好感度是 100。","silent":false,"silent_reason":null,"task_state":null,"claims":null}`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 3}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "我们算什么关系"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "主人是然然最亲近的人呀。[diana-msg]你的好感度是 100。" {
		t.Fatalf("Text = %q", resp.Text)
	}
}

// 用户要求输出的 JSON 不能被当成信封吞掉：多一个陌生键就按正文原样返回。
func TestRunnerKeepsUserRequestedJSONAsPlainText(t *testing.T) {
	payload := `{"content":"hi","silent":false,"extra":1}`
	client := &scriptedClient{responses: []string{payload}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 3}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "给我一段 JSON"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != payload {
		t.Fatalf("Text = %q", resp.Text)
	}
}

func TestFinalizeEnvelopeFromTextRejectsNonEnvelopes(t *testing.T) {
	for _, text := range []string{
		`{"content":"只有正文"}`,
		`{"action":"final","content":"走老路径"}`,
		`就是一句普通回复`,
		`{"tool":"reminder","arguments":{}}`,
	} {
		if _, ok := finalizeEnvelopeFromText(text); ok {
			t.Fatalf("unexpectedly decoded %q as finalize envelope", text)
		}
	}
	action, ok := finalizeEnvelopeFromText("```json\n{\"content\":\"\",\"silent\":true,\"silent_reason\":\"对方已经道别\"}\n```")
	if !ok || !action.Silent || action.SilentReason != "对方已经道别" || action.Content != "" {
		t.Fatalf("action=%#v ok=%v", action, ok)
	}
}
