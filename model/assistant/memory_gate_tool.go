// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"

	"github.com/SuInk/diana/model/llm"
)

const (
	memorySubmitToolName = "memory.submit"
	// 门控最多 5 条；摘要最多 6 条外加固定的一条 thread 便签。
	memoryGateMaxCandidates    = 5
	memorySummaryMaxCandidates = 7
)

// memoryCandidateSchema 描述一条候选记忆。存储层要校验的枚举全部写进 schema，
// 于是非法的 kind 或 visibility 在解码阶段就写不出来，而不是一路走到 Go 代码
// 才被拒绝——记忆作业跑在后台，被拒绝就是静默丢一条记忆。
func memoryCandidateSchema(actions, kinds, sourceTypes, visibilities []string) map[string]any {
	return toolObjectSchema(
		[]string{"action", "key", "kind", "topic", "source_type", "confidence", "importance", "visibility", "sensitive"},
		map[string]any{
			"action":         toolEnumParam("upsert 新增、确认或更新；forget 只用于明确要求忘记", actions...),
			"key":            toolStringParam("稳定且颗粒度足够细的记忆键，例如 preference.food.spicy"),
			"kind":           toolEnumParam("记忆类型", kinds...),
			"topic":          toolStringParam("简短主题"),
			"entity":         toolStringParam("记忆涉及的实体，可选"),
			"content":        toolStringParam("自包含、无歧义的第三人称事实；forget 时可为空"),
			"evidence":       toolStringParam("不超过 60 字的最小证据片段"),
			"source_type":    toolEnumParam("来源类型", sourceTypes...),
			"confidence":     toolNumberParam("0 到 1；inferred 必须不低于 0.90", 0, 1),
			"importance":     toolNumberParam("0 到 1", 0, 1),
			"visibility":     toolEnumParam("可见范围", visibilities...),
			"sensitive":      toolBoolParam("医疗、心理、财务、身份凭证、住址、联系方式、隐私关系等为 true"),
			"retention_days": toolIntParam("保留天数，0 表示不过期", 0, 3650),
		},
	)
}

// memorySubmitTool 组装一次记忆作业的提交工具。
func memorySubmitTool(description string, maxItems int, candidate map[string]any) llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        memorySubmitToolName,
		Description: description,
		Parameters: toolObjectSchema([]string{"memories"}, map[string]any{
			"memories": map[string]any{
				"type":        "array",
				"description": "候选记忆列表；没有候选时提交空数组",
				"maxItems":    maxItems,
				"items":       candidate,
			},
		}),
	}
}

func memoryGateSubmitTool() llm.ToolDefinition {
	return memorySubmitTool(
		"提交本次记忆门控的候选长期记忆。没有值得记住的内容时提交空数组。",
		memoryGateMaxCandidates,
		memoryCandidateSchema(
			[]string{string(MemoryActionUpsert), string(MemoryActionForget)},
			[]string{string(MemoryKindFact), string(MemoryKindPreference), string(MemoryKindEpisode), string(MemoryKindInstruction)},
			[]string{string(MemorySourceExplicit), string(MemorySourceInferred)},
			[]string{string(MemoryVisibilitySession), string(MemoryVisibilityUser)},
		),
	)
}

func memorySummarySubmitTool() llm.ToolDefinition {
	return memorySubmitTool(
		"提交整合后的会话摘要，以及固定的一条 thread 会话状态便签。",
		memorySummaryMaxCandidates,
		memoryCandidateSchema(
			[]string{string(MemoryActionUpsert)},
			[]string{string(MemoryKindSummary), string(MemoryKindThread)},
			[]string{string(MemorySourceSummary)},
			[]string{string(MemoryVisibilitySession)},
		),
	)
}

// generateMemoryCandidates 请求结构化提交，并把结果还原成旧的信封形状，让一个
// 解码器同时覆盖两种协议。记忆档可能指向不支持 function calling 的小模型，因此
// 工具请求被拒绝时退回自由文本信封，而不是丢掉整个记忆作业。
func generateMemoryCandidates(ctx context.Context, client LLMProvider, messages []llm.Message, tool llm.ToolDefinition) (string, error) {
	response, err := client.Generate(ctx, llm.GenerateRequest{
		Messages:   messages,
		Tools:      []llm.ToolDefinition{tool},
		ToolChoice: tool.Name,
	})
	if err != nil {
		response, err = client.Generate(ctx, llm.GenerateRequest{Messages: messages})
		if err != nil {
			return "", err
		}
	}
	if len(response.ToolCalls) > 0 && response.ToolCalls[0].Name == tool.Name {
		if raw, marshalErr := json.Marshal(response.ToolCalls[0].Arguments); marshalErr == nil {
			return string(raw), nil
		}
	}
	return response.Text, nil
}
