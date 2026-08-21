// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strconv"

// 工具参数的 JSON Schema 辅助构造。
//
// 这些工具此前一个都没有声明 schema，参数契约只能塞进 Description 的散文里，
// 靠内联 JSON 举例代替。结果是模型要从中文里读格式、provider 端的原生校验完全
// 用不上（任何输入都收），填错要一路走到 Go 代码才被拒。
//
// 声明 schema 之后，参数名、类型、枚举和取值范围由 provider 校验，Description
// 只留「什么时候该用我」。取值边界一律引用同一份常量，避免散文、schema 和校验
// 代码各写一份魔法数字然后各自漂移。

// toolObjectSchema 组装对象型参数 schema。required 允许为空。
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

func toolStringParam(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func toolEnumParam(description string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": description, "enum": values}
}

func toolBoolParam(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

func toolNumberParam(description string, minimum, maximum float64) map[string]any {
	return map[string]any{"type": "number", "description": description, "minimum": minimum, "maximum": maximum}
}

func toolIntParam(description string, minimum, maximum int) map[string]any {
	return map[string]any{"type": "integer", "description": description, "minimum": minimum, "maximum": maximum}
}

func toolStringArrayParam(description string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": description,
		"items":       map[string]any{"type": "string"},
	}
}

// toolItemsParam 描述批量创建用的 items 数组。
func toolItemsParam(description string, maxItems int, required []string, properties map[string]any) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": description,
		"maxItems":    maxItems,
		"items":       toolObjectSchema(required, properties),
	}
}

// itoa 让 schema 里的取值上限直接引用常量，不用在文案里重抄一遍数字。
func itoa(value int) string {
	return strconv.Itoa(value)
}
