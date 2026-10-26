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
	client := &scriptedClient{responses: []string{payload, payload}}
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

// 线上 10-22：swe-2 没走 function calling，把收尾调用写成标签拼在正文后面，整串标签
// 跟着正文发进了群。标签外的正文才是完整回复，标签里的 content 只是它开头的半句。
func TestRunnerStripsInlineFinalizeTagFromPlainText(t *testing.T) {
	body := "哈哈应该不至于只看手速，风控大概看的是整体行为模式，比如 24 小时秒回、发言频率太规律、协议端特征这些"
	client := &scriptedClient{responses: []string{
		body + `[agent_finalize content="哈哈应该不至于只看手速" render="" silent="false" silent_reason="" sticker="" sticker_order="" task_state=""/>]`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 3}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "回消息快了也会封号?"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != body {
		t.Fatalf("Text = %q", resp.Text)
	}
}

// 10-24 漏出去的信封带着收尾扩展字段和已撤掉的 render；只认自带字段时整段 JSON 会被当正文。
func TestRunnerDecodesEnvelopeWithDeclaredAndRetiredFields(t *testing.T) {
	client := &scriptedClient{responses: []string{
		`{"content":"主人不是我随叫随到的 NPC 喵，有事你直接跟我说就行","render":null,"silent":false,"silent_reason":null,"sticker":null,"sticker_order":null,"task_state":null}`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 3, FinalizeFields: []FinalizeField{{Name: "sticker"}, {Name: "sticker_order"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "叫你主人来"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "主人不是我随叫随到的 NPC 喵，有事你直接跟我说就行" {
		t.Fatalf("Text = %q", resp.Text)
	}
}

func TestInlineFinalizeAction(t *testing.T) {
	for _, tc := range []struct {
		text, content string
		silent        bool
	}{
		{"懂，GPT那直给风格写代码确实省心[agent_finalize][/agent_finalize]", "懂，GPT那直给风格写代码确实省心", false},
		{`<agent_finalize content="好的[diana-msg]马上" silent="false"/>`, "好的[diana-msg]马上", false},
		{`[agent_finalize content="" silent="true" silent_reason="没人叫我"/>]`, "", true},
		// 标签外有正文时不听标签里的 silent：正文已经写出来了，吞掉才是丢回复。
		{`收到[agent_finalize silent="true"/>]`, "收到", false},
		{`值里有括号也不截断[agent_finalize content="a]b>c"/>]`, "值里有括号也不截断", false},
	} {
		action, ok := parseAction(tc.text)
		if !ok || action.Action != "final" || action.Content != tc.content || action.Silent != tc.silent {
			t.Fatalf("%q => ok=%v %#v", tc.text, ok, action)
		}
	}
	for _, text := range []string{
		"agent_finalize 是收尾工具的名字",
		"用 [agent] 标签包起来",
		"finalize 之后再说",
	} {
		if _, _, ok := StripInlineFinalizeTags(text); ok {
			t.Fatalf("%q misread as inline finalize tag", text)
		}
	}
}
