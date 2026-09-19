// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolInputJSONSchemaValidation(t *testing.T) {
	tests := []struct{ name, schema, valid, invalid string }{
		{"required", `{"type":"object","required":["x"]}`, `{"x":1}`, `{}`},
		{"integer", `{"properties":{"x":{"type":"integer"}}}`, `{"x":1}`, `{"x":1.5}`},
		{"enum", `{"properties":{"x":{"enum":[1,2]}}}`, `{"x":1}`, `{"x":"1"}`},
		{"const", `{"properties":{"x":{"const":"yes"}}}`, `{"x":"yes"}`, `{"x":"PRIVATE-SECRET"}`},
		{"additional", `{"properties":{"x":{}},"additionalProperties":false}`, `{"x":1}`, `{"extra":1}`},
		{"nested", `{"properties":{"a":{"type":"array","items":{"type":"object","properties":{"x":{"type":"integer"}}}}}}`, `{"a":[{"x":1}]}`, `{"a":[{"x":"PRIVATE-SECRET"}]}`},
		{"minLength", `{"properties":{"x":{"minLength":2}}}`, `{"x":"ok"}`, `{"x":"a"}`},
		{"maxLength", `{"properties":{"x":{"maxLength":2}}}`, `{"x":"ok"}`, `{"x":"PRIVATE-SECRET"}`},
		{"minimum", `{"properties":{"x":{"minimum":1}}}`, `{"x":1}`, `{"x":0}`},
		{"maximum", `{"properties":{"x":{"maximum":1}}}`, `{"x":1}`, `{"x":2}`},
		{"minItems", `{"properties":{"x":{"minItems":1}}}`, `{"x":[1]}`, `{"x":[]}`},
		{"maxItems", `{"properties":{"x":{"maxItems":1}}}`, `{"x":[1]}`, `{"x":[1,2]}`},
		{"anyOf", `{"properties":{"x":{"anyOf":[{"type":"integer"},{"const":"yes"}]}}}`, `{"x":1}`, `{"x":false}`},
		{"oneOf", `{"properties":{"x":{"oneOf":[{"type":"integer"},{"minimum":1}]}}}`, `{"x":0}`, `{"x":1}`},
		{"allOf", `{"properties":{"x":{"allOf":[{"type":"integer"},{"minimum":1}]}}}`, `{"x":1}`, `{"x":0}`},
		{"ref", `{"$defs":{"count":{"type":"integer"}},"properties":{"x":{"$ref":"#/$defs/count"}}}`, `{"x":1}`, `{"x":false}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var schema, valid, invalid map[string]any
			for raw, dst := range map[string]*map[string]any{tt.schema: &schema, tt.valid: &valid, tt.invalid: &invalid} {
				if err := json.Unmarshal([]byte(raw), dst); err != nil {
					t.Fatal(err)
				}
			}
			if err := validateToolInput(schema, valid); err != nil {
				t.Fatal(err)
			}
			err := validateToolInput(schema, invalid)
			if err == nil || strings.Contains(err.Error(), "PRIVATE-SECRET") {
				t.Fatalf("unsafe/missing error: %v", err)
			}
			if tt.name == "nested" && !strings.Contains(err.Error(), "/a/0/x") {
				t.Fatalf("missing field path: %v", err)
			}
		})
	}
	if err := validateToolInput(map[string]any{"$ref": "http://127.0.0.1/secret"}, map[string]any{}); err == nil {
		t.Fatal("external ref allowed")
	}
}
