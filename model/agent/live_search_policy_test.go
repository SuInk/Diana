// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 回放线上两种提问，验证回复模型自行选择检索；默认跳过，不发送聊天消息。
func TestLiveSearchPolicyCodeMode(t *testing.T) {
	client := liveAgentClient(t)
	for _, tc := range []struct {
		question string
		search   bool
	}{
		{"嘉然你觉得你需要code mode吗", true},
		{"你都没调研确认啥是Code Mode 就判断它不薄了", true},
		{"Code Mode 是什么意思？", true},
		{"Memoh 有没有支持并行调研的现成实现？", true},
		{"我准备花一个月迁移开发工作流，现在选 OpenCode 还是 Codex CLI 更合适？", true},
		{"DeepSeek v4.1 flash API 现在多少钱？", true},
		{"不要联网，只根据下面这段材料归纳：Code Mode 用代码组合工具调用。", false},
		{"2 加 2 等于多少？", false},
	} {
		t.Run(tc.question, func(t *testing.T) {
			search, err := NewWebSearchTool(WebSearchToolOptions{Config: DefaultWebSearchConfig(), Timeout: 30 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			runner, err := NewRunner(client, Config{MaxSteps: 6, ToolTimeoutMS: 35000}, NewToolRegistry(search))
			if err != nil {
				t.Fatal(err)
			}
			defer runner.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			resp, err := runner.Run(ctx, Request{Messages: []llm.Message{
				{Role: llm.RoleSystem, Content: "你是聊天机器人嘉然，今天是 2026-10-03。评估自己的 Agent 工具调用运行时。"},
				{Role: llm.RoleUser, Content: tc.question},
			}})
			if err != nil {
				t.Fatal(err)
			}
			searches := 0
			for _, step := range resp.Steps {
				if step.Tool == WebSearchToolName {
					searches++
					t.Logf("search=%v error=%s", step.Input, step.Error)
				}
			}
			t.Logf("model=%s searches=%d finish=%s reply=%s", resp.Model, searches, resp.FinishReason, truncateText(resp.Text, 500))
			if tc.search && searches == 0 {
				t.Fatal("该查证的场景未主动检索")
			}
			if !tc.search && searches != 0 {
				t.Fatal("明确不联网或稳定算术场景不应搜索")
			}
		})
	}
}
