// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"encoding/json"
	"sort"
	"strings"
)

// 按需目录里每个工具带一行参数签名，模型照着就能直接 tools_execute 或在 codemode 里
// 调用，不必先 tool_search 取 inputSchema。写法照 pi-codemode 的 TypeScript 声明，
// 但压成一行、不带字段说明：目录在缓存前缀里，要稳定也要短。
//
// 签名超过 catalogSignatureMaxRunes 的工具（bot_config、subscription 这类几十个字段的）
// 不展开，标一句让模型先 tool_search。

const catalogSignatureMaxRunes = 600

// catalogSignatureOverflow 是签名放不下时目录里写的那句。
const catalogSignatureOverflow = "参数多，先 tool_search 取定义"

// catalogSignatureFreeForm 是没有声明字段、什么参数都收的工具在目录里写的那句。
const catalogSignatureFreeForm = "参数不限"

// toolSignature 把工具的 inputSchema 渲染成「(a: string, b?: number)」。没有参数时是
// 「()」，放不下时是「(参数多，先 tool_search 取定义)」。
func toolSignature(tool Tool) string {
	schema, err := snapshotToolSchema(tool)
	if err != nil {
		return "(" + catalogSignatureOverflow + ")"
	}
	return schemaSignature(schema)
}

// schemaSignature 只认对象 schema 的顶层字段：必填在前、可选在后，各自按名字排序，
// 这样同一份 schema 每次渲染出来都一样。
func schemaSignature(schema map[string]any) string {
	return schemaSignatureLimit(schema, catalogSignatureMaxRunes)
}

// schemaSignatureLimit 是 schemaSignature 加上可调的长度上限，离线核对时用。
func schemaSignatureLimit(schema map[string]any, maxRunes int) string {
	properties, _ := schema["properties"].(map[string]any)
	if len(properties) == 0 {
		// 没声明字段又允许任意字段的，多半是没写 schema 的插件工具：参数不限。
		if additional, ok := schema["additionalProperties"]; ok && additional != false {
			return "(" + catalogSignatureFreeForm + ")"
		}
		return "()"
	}
	required := schemaRequired(schema)
	parts := make([]string, 0, len(properties))
	for _, name := range orderedProperties(properties, required) {
		part := propertyKey(name)
		if !required[name] {
			part += "?"
		}
		parts = append(parts, part+": "+schemaType(properties[name], 0))
	}
	signature := "(" + strings.Join(parts, ", ") + ")"
	if len([]rune(signature)) > maxRunes {
		return "(" + catalogSignatureOverflow + ")"
	}
	return signature
}

// schemaTypeDepthLimit 是嵌套对象展开到第几层就只写 object。
const schemaTypeDepthLimit = 2

// schemaType 把一个 JSON Schema 写成 TypeScript 类型。看不懂的一律 unknown，
// 不猜：签名错了比没有更糟。
func schemaType(raw any, depth int) string {
	schema, ok := raw.(map[string]any)
	if !ok {
		return "unknown"
	}
	if value, ok := schema["const"]; ok {
		return jsonLiteral(value)
	}
	if values, ok := schema["enum"].([]any); ok && len(values) > 0 {
		literals := make([]string, 0, len(values))
		for _, value := range values {
			literals = append(literals, jsonLiteral(value))
		}
		return typeUnion(literals)
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		if variants, ok := schema[key].([]any); ok && len(variants) > 0 {
			types := make([]string, 0, len(variants))
			for _, variant := range variants {
				types = append(types, schemaType(variant, depth))
			}
			return typeUnion(types)
		}
	}
	if parts, ok := schema["allOf"].([]any); ok && len(parts) > 0 {
		types := make([]string, 0, len(parts))
		for _, part := range parts {
			if rendered := schemaType(part, depth); rendered != "unknown" {
				types = append(types, parenthesizeUnion(rendered))
			}
		}
		if len(types) == 0 {
			return "unknown"
		}
		return strings.Join(types, " & ")
	}
	switch kind := schema["type"].(type) {
	case []any:
		types := make([]string, 0, len(kind))
		for _, entry := range kind {
			single := cloneDeferredInput(schema).(map[string]any)
			single["type"] = entry
			types = append(types, schemaType(single, depth))
		}
		return typeUnion(types)
	case string:
		switch kind {
		case "string":
			return "string"
		case "number", "integer":
			return "number"
		case "boolean":
			return "boolean"
		case "null":
			return "null"
		case "array":
			return arrayType(schema, depth)
		case "object":
			return objectType(schema, depth)
		}
		return "unknown"
	case nil:
		if _, ok := schema["properties"]; ok {
			return objectType(schema, depth)
		}
		if _, ok := schema["items"]; ok {
			return arrayType(schema, depth)
		}
	}
	return "unknown"
}

func arrayType(schema map[string]any, depth int) string {
	items, ok := schema["items"]
	if !ok {
		return "unknown[]"
	}
	if tuple, ok := items.([]any); ok {
		types := make([]string, 0, len(tuple))
		for _, item := range tuple {
			types = append(types, schemaType(item, depth))
		}
		return "[" + strings.Join(types, ", ") + "]"
	}
	return parenthesizeUnion(schemaType(items, depth)) + "[]"
}

func objectType(schema map[string]any, depth int) string {
	properties, _ := schema["properties"].(map[string]any)
	if len(properties) == 0 {
		return "object"
	}
	if depth >= schemaTypeDepthLimit {
		return "object"
	}
	required := schemaRequired(schema)
	members := make([]string, 0, len(properties))
	for _, name := range orderedProperties(properties, required) {
		member := propertyKey(name)
		if !required[name] {
			member += "?"
		}
		members = append(members, member+": "+schemaType(properties[name], depth+1))
	}
	return "{ " + strings.Join(members, "; ") + " }"
}

func schemaRequired(schema map[string]any) map[string]bool {
	required := map[string]bool{}
	switch names := schema["required"].(type) {
	case []any:
		for _, name := range names {
			if text, ok := name.(string); ok {
				required[text] = true
			}
		}
	case []string:
		for _, name := range names {
			required[name] = true
		}
	}
	return required
}

// orderedProperties 必填在前、可选在后，各组按名字排序。
func orderedProperties(properties map[string]any, required map[string]bool) []string {
	var head, tail []string
	for name := range properties {
		if required[name] {
			head = append(head, name)
		} else {
			tail = append(tail, name)
		}
	}
	sort.Strings(head)
	sort.Strings(tail)
	return append(head, tail...)
}

func propertyKey(name string) string {
	for index, r := range name {
		letter := r == '_' || r == '$' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z'
		if !letter && !(index > 0 && r >= '0' && r <= '9') {
			return jsonLiteral(name)
		}
	}
	if name == "" {
		return `""`
	}
	return name
}

func jsonLiteral(value any) string {
	raw, err := marshalCodeModeJSON(value)
	if err != nil {
		return "unknown"
	}
	return raw
}

func typeUnion(types []string) string {
	seen := map[string]bool{}
	unique := make([]string, 0, len(types))
	for _, entry := range types {
		if entry == "unknown" {
			return "unknown"
		}
		if !seen[entry] {
			seen[entry] = true
			unique = append(unique, entry)
		}
	}
	if len(unique) == 0 {
		return "never"
	}
	return strings.Join(unique, " | ")
}

// parenthesizeUnion 给最外层是联合的类型加括号，放进数组或交叉类型时才不改意思：
// 「{ a } | { b }[]」读出来是「a 或 b 的数组」。只看括号和引号之外的「 | 」，
// 「{ a: "x" | "y" }」这种对象内部的联合不用加。
func parenthesizeUnion(rendered string) string {
	if topLevelUnion(rendered) {
		return "(" + rendered + ")"
	}
	return rendered
}

func topLevelUnion(rendered string) bool {
	depth, quoted, escaped := 0, false, false
	for index, char := range rendered {
		switch {
		case quoted:
			switch {
			case escaped:
				escaped = false
			case char == '\\':
				escaped = true
			case char == '"':
				quoted = false
			}
		case char == '"':
			quoted = true
		case char == '{' || char == '[' || char == '(':
			depth++
		case char == '}' || char == ']' || char == ')':
			depth--
		case char == '|' && depth == 0 && strings.HasPrefix(rendered[index:], "| ") && index > 0 && rendered[index-1] == ' ':
			return true
		}
	}
	return false
}

// signatureFromJSON 给测试和离线核对用：从一份 inputSchema 的 JSON 文本渲染签名。
func signatureFromJSON(raw []byte) (string, error) {
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return "", err
	}
	return schemaSignature(schema), nil
}
