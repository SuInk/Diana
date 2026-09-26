// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// 线上模型拿技能名 "bot-protocol" 去 tools_load，撞「不存在」后接着猜工具名。
// 技能和工具都是按需加载的能力，tools_load 应当直接给出 SKILL.md。
func TestToolsLoadReturnsSkillBodyForSkillName(t *testing.T) {
	registry := NewToolRegistry(&countingTool{name: "common"}, &countingTool{name: "platform_query"})
	registry.SetSkills([]SkillMetadata{{Name: "bot-protocol", Description: "协议路由", Content: "群查询用 platform_query"}})
	loader := newDeferredToolLoader(registry, []string{"common"})

	out, err := loader.Run(context.Background(), map[string]any{"names": []any{"bot-protocol", "platform_query"}})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Loaded []struct{ Name string } `json:"loaded"`
		Skills []struct {
			Name    string `json:"name"`
			Content string `json:"content"`
		} `json:"skills"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Skills) != 1 || result.Skills[0].Name != "bot-protocol" || !strings.Contains(result.Skills[0].Content, "platform_query") {
		t.Fatalf("skills = %+v", result.Skills)
	}
	if len(result.Loaded) != 1 || result.Loaded[0].Name != "platform_query" {
		t.Fatalf("loaded = %+v", result.Loaded)
	}
	if _, loaded := loader.loaded["bot-protocol"]; loaded {
		t.Fatal("skill must not be registered as a loaded tool")
	}

	// 技能不能被 tools_execute 当工具跑，报错要指回 tools_load。
	_, err = loader.dispatch(llmAction{Tool: ToolsExecuteToolName, Input: map[string]any{"name": "bot-protocol", "input": map[string]any{}}})
	if err == nil || !strings.Contains(err.Error(), "是技能") {
		t.Fatalf("execute skill error = %v", err)
	}
}
