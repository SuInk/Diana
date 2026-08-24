// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

// Agent 侧工具参数的 JSON Schema 辅助构造，命名与 model/assistant/tool_schema.go
// 保持一致。
//
// 这些工具此前只有一个宽松的 {"type":"object","additionalProperties":true}，参数
// 契约全靠 Description 里的 `input: {...}` 散文示例。结果是参数名要模型从中文里
// 读，provider 的原生约束解码完全用不上，MCP 工具上游返回的合法 schema 还会被
// 描述长度预算截断。声明 schema 之后，参数由 provider 校验，Description 只留
// 「什么时候该用我」。
func toolObjectSchema(required []string, properties map[string]any) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// toolEmptySchema 描述不接受参数的工具。
func toolEmptySchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{})
}

func toolStringParam(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func toolEnumParam(description string, values ...string) map[string]any {
	property := map[string]any{"type": "string", "description": description}
	if len(values) > 0 {
		property["enum"] = values
	}
	return property
}

func toolBoolParam(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

func toolIntParam(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func toolStringArrayParam(description string, values ...string) map[string]any {
	items := map[string]any{"type": "string"}
	if len(values) > 0 {
		items["enum"] = values
	}
	return map[string]any{"type": "array", "description": description, "items": items}
}

func toolArrayParam(description string, items map[string]any) map[string]any {
	return map[string]any{"type": "array", "description": description, "items": items}
}

// toolStringMapParam 描述 env、headers 这类自由键值对。
func toolStringMapParam(description string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"description":          description,
		"additionalProperties": map[string]any{"type": "string"},
	}
}
