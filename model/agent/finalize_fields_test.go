// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 调用方追加的收尾字段出现在工具定义里，模型填了就原样带回；没声明的字段和空值不带，
// 也不能覆盖信封自带的字段。
func TestFinalizeFieldsRoundTrip(t *testing.T) {
	fields := []FinalizeField{{Name: "sticker", Description: "配一张表情包的关键词"}, {Name: "content", Description: "不许覆盖"}}
	definition := finalizeToolDefinition(false, fields...)
	properties, _ := definition.Parameters["properties"].(map[string]any)
	if _, ok := properties["sticker"]; !ok {
		t.Fatalf("sticker missing from finalize schema: %#v", properties)
	}
	if content, _ := properties["content"].(map[string]any); content["description"] == "不许覆盖" {
		t.Fatal("caller field overrode the built-in content field")
	}

	client := &silentFinalizeClient{arguments: map[string]any{
		"content": "嘿嘿，被你发现了", "sticker": " 得意 叉腰 ", "mood": "happy", "silent": false,
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 2, FinalizeFields: fields[:1]}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "你是不是偷吃了"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "嘿嘿，被你发现了" || len(resp.FinalizeFields) != 1 || resp.FinalizeFields["sticker"] != "得意 叉腰" {
		t.Fatalf("resp = %#v", resp)
	}
}

// 静默收尾时调用方字段照样交出去：「不说话、只回一张表情包」就是这样表达的。
func TestFinalizeFieldsKeptOnSilentFinish(t *testing.T) {
	client := &silentFinalizeClient{arguments: map[string]any{"content": "", "silent": true, "sticker": "晚安"}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 2, FinalizeFields: []FinalizeField{{Name: "sticker"}}}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "晚安"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Silent || resp.Text != "" || resp.FinalizeFields["sticker"] != "晚安" {
		t.Fatalf("resp = %#v", resp)
	}
}
