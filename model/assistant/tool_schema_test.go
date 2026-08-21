// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

// dianaAgentTools 列出所有随 Agent 一起注册的内置工具，供整体性断言使用。
func dianaAgentTools(t *testing.T) []agent.Tool {
	t.Helper()
	runtime := NewRuntime(BotConfig{BotAccount: "42", OwnerID: "1"}, nilChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "1"}
	policy := RelationshipPolicy{Owner: true, AllowImageGeneration: true, AllowImageEditing: true}
	return []agent.Tool{
		newDianaChatHistoryTool(runtime, event),
		newDianaHistoryImagesTool(runtime, event),
		newDianaOneBotGroupTool(runtime, event),
		newDianaRelationshipTool(runtime, event),
		newDianaImageTool(runtime, event, policy),
		newDianaTasksTool(runtime, event),
		newDianaReminderTool(runtime, event),
		newDianaScheduleTool(runtime, event),
		newDianaRSSWatchTool(runtime, event),
		newDianaLLMConfigTool(runtime, event),
		newDianaConfigTool(runtime),
		&dianaCapabilitiesTool{plugin: NewCapabilityKnowledgePlugin()},
	}
}

func TestEveryAgentToolDeclaresInputSchema(t *testing.T) {
	// 没有 schema 的工具会退回到 {"type":"object","additionalProperties":true}，
	// 参数契约就只能塞进 Description 的散文里，provider 端的校验也完全用不上。
	for _, tool := range dianaAgentTools(t) {
		typed, ok := tool.(agent.ToolInputSchema)
		if !ok {
			t.Errorf("%s 没有实现 InputSchema", tool.Name())
			continue
		}
		schema := typed.InputSchema()
		if schema == nil {
			t.Errorf("%s 的 InputSchema 返回 nil", tool.Name())
			continue
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok || len(properties) == 0 {
			t.Errorf("%s 的 schema 没有声明任何参数", tool.Name())
			continue
		}
		for name, raw := range properties {
			property, ok := raw.(map[string]any)
			if !ok {
				t.Errorf("%s 的参数 %s 不是对象", tool.Name(), name)
				continue
			}
			if _, ok := property["type"].(string); !ok {
				t.Errorf("%s 的参数 %s 没有声明 type", tool.Name(), name)
			}
			// 参数说明是模型唯一能读到的字段级文档，缺了等于又把契约推回散文里。
			if description, _ := property["description"].(string); strings.TrimSpace(description) == "" {
				t.Errorf("%s 的参数 %s 没有写说明", tool.Name(), name)
			}
		}
	}
}

func TestAgentToolDescriptionsSurviveCompaction(t *testing.T) {
	// 提示词里的工具清单会把描述压到 agentToolDescriptionBudget 字。超了就会被
	// 截断，而 compactToolDescription 优先保 input: 之后的部分，被砍掉的正好是
	// 开头「什么时候该用我」——原生 function calling 那条路又是全量发送，
	// 同一段文字在两个通道行为不一致。
	for _, tool := range dianaAgentTools(t) {
		if length := len([]rune(tool.Description())); length > agent.ToolDescriptionBudget {
			t.Errorf("%s 的描述 %d 字，超过 %d 字预算，会在提示词清单里被截断",
				tool.Name(), length, agent.ToolDescriptionBudget)
		}
	}
}

func TestAgentToolDescriptionsDropInlineJSONExamples(t *testing.T) {
	// 参数格式一旦回到散文里的内联 JSON，schema 就又变成了摆设。
	for _, tool := range dianaAgentTools(t) {
		description := tool.Description()
		if strings.Contains(strings.ToLower(description), "input:") {
			t.Errorf("%s 的描述里还留着 input: 内联示例，应该改到 InputSchema", tool.Name())
		}
	}
}
