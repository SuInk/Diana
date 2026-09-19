// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
			reasons = append(reasons, fmt.Sprintf("input%s: 不符合 %s 约束（参照 tools.load 的 inputSchema）", e.InstanceLocation, e.KeywordLocation))
		}
		visit(validation)
		return fmt.Errorf("%s", strings.Join(reasons, "; "))
	}
	return nil
}
