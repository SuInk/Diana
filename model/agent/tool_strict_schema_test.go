// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"testing"
)

func TestSchemaAllowsStrictModeRequiresClosedAndFullyRequiredSchema(t *testing.T) {
	closedAndRequired := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{"a": map[string]any{"type": "string"}},
		"required":             []string{"a"},
	}
	if !schemaAllowsStrictMode(closedAndRequired) {
		t.Fatal("禁止额外字段且全部必填的 schema 应当允许 strict")
	}

	// 绝大多数工具都有可选参数。这类 schema 照样要发出去（模型仍能看到参数名、
	// 类型和取值范围），但不能声明 strict——声明了 provider 会拒绝整个请求。
	optionalParam := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"a": map[string]any{"type": "string"},
			"b": map[string]any{"type": "string"},
		},
		"required": []string{"a"},
	}
	if schemaAllowsStrictMode(optionalParam) {
		t.Fatal("有可选参数时不能声明 strict")
	}

	openSchema := map[string]any{
		"type":                 "object",
		"additionalProperties": true,
		"properties":           map[string]any{"a": map[string]any{"type": "string"}},
		"required":             []string{"a"},
	}
	if schemaAllowsStrictMode(openSchema) {
		t.Fatal("允许额外字段时不能声明 strict")
	}

	if schemaAllowsStrictMode(map[string]any{"type": "object", "additionalProperties": false}) {
		t.Fatal("没有 properties 时不能声明 strict")
	}
}

func TestDefinitionsKeepSchemaWithoutClaimingStrict(t *testing.T) {
	// 回归：此前只要工具给了 schema 就无条件 strict=true。工具几乎都有可选参数，
	// 那样发出去会被 provider 直接拒掉，比没有 schema 更糟。
	registry := NewToolRegistry(optionalParamTool{})
	definitions := registry.Definitions()
	if len(definitions) != 1 {
		t.Fatalf("definitions = %#v", definitions)
	}
	if definitions[0].Strict {
		t.Fatal("含可选参数的 schema 不应声明 strict")
	}
	properties, ok := definitions[0].Parameters["properties"].(map[string]any)
	if !ok || len(properties) != 2 {
		t.Fatalf("schema 没有原样发出：%#v", definitions[0].Parameters)
	}
}

type optionalParamTool struct{}

func (optionalParamTool) Name() string        { return "stub.tool" }
func (optionalParamTool) Description() string { return "测试用工具。" }

func (optionalParamTool) Run(context.Context, map[string]any) (string, error) { return "", nil }

func (optionalParamTool) InputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"required_param": map[string]any{"type": "string", "description": "必填。"},
			"optional_param": map[string]any{"type": "string", "description": "可选。"},
		},
		"required": []string{"required_param"},
	}
}
