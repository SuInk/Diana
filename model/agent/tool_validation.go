// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

func snapshotToolSchema(tool Tool) (map[string]any, error) {
	schema := map[string]any{"type": "object", "additionalProperties": true}
	if typed, ok := tool.(ToolInputSchema); ok {
		if provided := typed.InputSchema(); provided != nil {
			schema = provided
		}
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var snapshot map[string]any
	err = json.Unmarshal(raw, &snapshot)
	return snapshot, err
}

// Validate with a full JSON Schema implementation. External references fail closed:
// loading a contract must never cause network requests to untrusted schema URLs.
func validateToolInput(schema map[string]any, input map[string]any) error {
	raw, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("inputSchema 无法编码")
	}
	compiler := jsonschema.NewCompiler()
	compiler.LoadURL = func(string) (io.ReadCloser, error) { return nil, fmt.Errorf("external schema references are disabled") }
	if err := compiler.AddResource("https://diana.invalid/tool.json", bytes.NewReader(raw)); err != nil {
		return fmt.Errorf("inputSchema 无效")
	}
	compiled, err := compiler.Compile("https://diana.invalid/tool.json")
	if err != nil {
		return fmt.Errorf("inputSchema 无效或含不可解析的外部引用")
	}
	raw, err = json.Marshal(input)
	if err != nil {
		return fmt.Errorf("input 必须是 JSON 对象")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("input 必须是 JSON 对象")
	}
	if err := compiled.Validate(value); err != nil {
		var validation *jsonschema.ValidationError
		if !errors.As(err, &validation) {
			return fmt.Errorf("input schema validation failed")
		}
		// Do not return validator messages: enum/const/type messages can contain
		// actual input values, secrets, or private IDs restored by the local proxy.
		var reasons []string
		var visit func(*jsonschema.ValidationError)
		visit = func(e *jsonschema.ValidationError) {
			if len(reasons) >= 8 {
				return
			}
			if len(e.Causes) > 0 {
				for _, child := range e.Causes {
					visit(child)
				}
				return
			}
			reason := fmt.Sprintf("input%s: 不符合 %s 约束", e.InstanceLocation, e.KeywordLocation)
			if hint := validationHint(schema, value, e); hint != "" {
				reason += "，" + hint
			}
			reasons = append(reasons, reason)
		}
		visit(validation)
		return fmt.Errorf("%s（参照 tool_search 返回的 inputSchema 改好后重新调用）", strings.Join(reasons, "; "))
	}
	return nil
}

// validationHint 把校验失败翻译成「该怎么改」。弱一些的模型（线上 devin/swe-2）只看到
// 「不符合 /additionalProperties 约束」时不知道错在哪，连撞几次就放弃，回用户「工具调用
// 失败」。这里只说出参数名和 schema 自己写的取值，不回显模型填的值：值里可能有本地代理
// 还原出的私人 ID 或密钥。参数名本身也来自模型，截断后再用。
func validationHint(schema map[string]any, instance any, e *jsonschema.ValidationError) string {
	keyword, node := schemaKeywordNode(schema, e.AbsoluteKeywordLocation)
	if node == nil {
		return ""
	}
	object, _ := jsonPointerValue(instance, e.InstanceLocation).(map[string]any)
	switch keyword {
	case "required":
		var missing []string
		for _, name := range stringList(node["required"]) {
			if _, ok := object[name]; !ok {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			return "缺少必填参数 " + strings.Join(missing, "、")
		}
	case "additionalProperties":
		if object == nil {
			return ""
		}
		properties, _ := node["properties"].(map[string]any)
		var patterns []*regexp.Regexp
		if raw, ok := node["patternProperties"].(map[string]any); ok {
			for pattern := range raw {
				if re, err := regexp.Compile(pattern); err == nil {
					patterns = append(patterns, re)
				}
			}
		}
		var extra []string
		for name := range object {
			if _, ok := properties[name]; ok || matchesAny(patterns, name) {
				continue
			}
			extra = append(extra, hintName(name))
		}
		if len(extra) == 0 {
			return ""
		}
		sort.Strings(extra)
		hint := "多了不支持的参数 " + strings.Join(extra, "、") + "，去掉它们"
		if allowed := schemaPropertyNames(properties); len(allowed) > 0 {
			hint += "；可用参数只有 " + strings.Join(allowed, "、")
		}
		return hint
	case "enum":
		values, _ := node["enum"].([]any)
		if len(values) == 0 || len(values) > 20 {
			return ""
		}
		var options []string
		for _, value := range values {
			raw, _ := json.Marshal(value)
			options = append(options, string(raw))
		}
		return "只能取 " + strings.Join(options, "、")
	case "type":
		switch expected := node["type"].(type) {
		case string:
			return "应为 " + expected + " 类型"
		case []any:
			var types []string
			for _, item := range expected {
				if text, ok := item.(string); ok {
					types = append(types, text)
				}
			}
			if len(types) > 0 {
				return "应为 " + strings.Join(types, " 或 ") + " 类型"
			}
		}
	}
	return ""
}

// schemaKeywordNode 按校验器给出的绝对关键字位置（已解析 $ref）找回出错关键字所在的
// schema 对象。只认本地这份 schema，外部引用在编译阶段就已拒绝。
func schemaKeywordNode(schema map[string]any, location string) (string, map[string]any) {
	_, fragment, ok := strings.Cut(location, "#")
	if !ok {
		return "", nil
	}
	tokens := strings.Split(strings.TrimPrefix(fragment, "/"), "/")
	if len(tokens) == 0 || tokens[0] == "" {
		return "", nil
	}
	keyword := unescapePointer(tokens[len(tokens)-1])
	node, _ := jsonPointerValue(schema, "/"+strings.Join(tokens[:len(tokens)-1], "/")).(map[string]any)
	return keyword, node
}

func jsonPointerValue(value any, pointer string) any {
	if pointer == "" || pointer == "/" {
		return value
	}
	for _, token := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		token = unescapePointer(token)
		switch current := value.(type) {
		case map[string]any:
			value = current[token]
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(current) {
				return nil
			}
			value = current[index]
		default:
			return nil
		}
	}
	return value
}

func unescapePointer(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
}

func stringList(value any) []string {
	var result []string
	switch items := value.(type) {
	case []any:
		for _, item := range items {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
	case []string:
		result = append(result, items...)
	}
	return result
}

func schemaPropertyNames(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func matchesAny(patterns []*regexp.Regexp, name string) bool {
	for _, re := range patterns {
		if re.MatchString(name) {
			return true
		}
	}
	return false
}

func hintName(name string) string {
	if runes := []rune(name); len(runes) > 40 {
		return string(runes[:40]) + "…"
	}
	return name
}
