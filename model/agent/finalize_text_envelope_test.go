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
		`{"content":"未声明的字段","sticker":"x"}`,
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
	if resp.Text != "主人不是我随叫随到的 NPC 喵，有事你直接跟我说就行" || len(resp.FinalizeFields) != 0 {
		t.Fatalf("Text = %q fields = %#v", resp.Text, resp.FinalizeFields)
	}
	if len(client.requests) != 1 {
		t.Fatalf("model calls = %d, want 1 (no protocol repair)", len(client.requests))
	}
}

// 语音通话里 gpt-5.6-terra 每轮都把信封当正文出，扩展字段带值时也要一次解码，
// 值照常交给调用方，不能再多调一次模型做协议修复。
func TestRunnerDecodesEnvelopeWithDeclaredFieldValues(t *testing.T) {
	client := &scriptedClient{responses: []string{
		`{"content":"晚安啦","render":null,"silent":false,"silent_reason":null,"sticker":"晚安 挥手","sticker_order":1,"task_state":null}`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 3, FinalizeFields: []FinalizeField{{Name: "sticker"}, {Name: "sticker_order"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "我去睡了"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "晚安啦" || len(resp.FinalizeFields) != 1 || resp.FinalizeFields["sticker"] != "晚安 挥手" {
		t.Fatalf("Text = %q fields = %#v", resp.Text, resp.FinalizeFields)
	}
	if len(client.requests) != 1 {
		t.Fatalf("model calls = %d, want 1 (no protocol repair)", len(client.requests))
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

// 线上 10-27 swe-2：收尾标签成对包着整个信封。剥完标签剩下的 JSON 原来被当正文，
// 再按 [diana-msg] 拆成「{"content": "第一段」和「第二段", "silent": false, …}」两条发进群里。
func TestRunnerDecodesFinalizeEnvelopeWrappedInInlineTags(t *testing.T) {
	client := &scriptedClient{responses: []string{
		`[agent_finalize]{"content": "早上好呀[diana-msg]今天也要加油", "silent": false, "silent_reason": "", "sticker": "", "task_state": null}[/agent_finalize]`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 3, FinalizeFields: []FinalizeField{{Name: "sticker"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "早"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "早上好呀[diana-msg]今天也要加油" || resp.Silent {
		t.Fatalf("Text = %q silent = %v", resp.Text, resp.Silent)
	}
}

func TestInlineFinalizeTagsWrappingEnvelope(t *testing.T) {
	declared := []FinalizeField{{Name: "sticker"}}
	action, ok := parseAction(`[agent_finalize]{"content":"","silent":true,"silent_reason":"没人叫我","sticker":""}[/agent_finalize]`, declared...)
	if !ok || action.Action != "final" || !action.Silent || action.Content != "" || action.SilentReason != "没人叫我" {
		t.Fatalf("silent envelope => ok=%v %#v", ok, action)
	}
	action, ok = parseAction(`[agent_finalize]{"content":"晚安","silent":false,"sticker":"晚安 挥手"}[/agent_finalize]`, declared...)
	if !ok || action.Content != "晚安" || action.Fields["sticker"] != "晚安 挥手" {
		t.Fatalf("sticker envelope => ok=%v %#v", ok, action)
	}
}
