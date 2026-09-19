// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"encoding/json"
	"strconv"
	"strings"
)

// coerceToolInputArrays 修正顶层数组参数被模型写成字符串的情况：
// "[\"a\"]"、括号不配对的 "[\"a\"]]"、或只传一个元素 "a"。
// 返回新 map，不改动 provider 历史里原始的参数对象。
func coerceToolInputArrays(schema map[string]any, input map[string]any) map[string]any {
	properties, _ := schema["properties"].(map[string]any)
	if len(properties) == 0 || len(input) == 0 {
		return input
	}
	var out map[string]any
	for key, value := range input {
		text, ok := value.(string)
		if !ok {
			continue
		}
		property, _ := properties[key].(map[string]any)
		if property == nil || property["type"] != "array" {
			continue
		}
		itemType := ""
		if items, ok := property["items"].(map[string]any); ok {
			itemType, _ = items["type"].(string)
		}
		coerced, ok := stringToArray(text, itemType)
		if !ok {
			continue
		}
		if out == nil {
			out = make(map[string]any, len(input))
			for k, v := range input {
				out[k] = v
			}
		}
		out[key] = coerced
	}
	if out == nil {
		return input
	}
	return out
}

func stringToArray(text, itemType string) ([]any, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, false
	}
	var decoded []any
	if json.Unmarshal([]byte(text), &decoded) == nil {
		return decoded, true
	}
	var parts []string
	if strings.HasPrefix(text, "[") {
		inner := strings.Trim(text, "[] \t\r\n")
		for _, part := range strings.Split(inner, ",") {
			if part = strings.Trim(strings.TrimSpace(part), `"'`); part != "" {
				parts = append(parts, part)
			}
		}
	} else if !strings.HasPrefix(text, "{") {
		parts = []string{text}
	}
	if len(parts) == 0 {
		return nil, false
	}
	out := make([]any, 0, len(parts))
	for _, part := range parts {
		switch itemType {
		case "integer", "number":
			number, err := strconv.ParseFloat(part, 64)
			if err != nil {
				return nil, false
			}
			out = append(out, number)
		case "", "string":
			out = append(out, part)
		default:
			return nil, false
		}
	}
	return out, true
}
