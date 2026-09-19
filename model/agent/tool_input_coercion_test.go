// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"reflect"
	"testing"
)

func TestCoerceToolInputArrays(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"message_ids":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"media_indexes": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			"detail":        map[string]any{"type": "string"},
		},
	}
	cases := []struct {
		name  string
		input map[string]any
		want  map[string]any
	}{
		{"json string", map[string]any{"message_ids": `["im_message_1", "im_message_2"]`}, map[string]any{"message_ids": []any{"im_message_1", "im_message_2"}}},
		// mimo-v2.5 实际输出过的畸形值。
		{"unbalanced bracket", map[string]any{"message_ids": `["im_message_7a59dbad666f"]]`}, map[string]any{"message_ids": []any{"im_message_7a59dbad666f"}}},
		{"bare scalar", map[string]any{"message_ids": "im_message_1"}, map[string]any{"message_ids": []any{"im_message_1"}}},
		{"integers", map[string]any{"media_indexes": "[2, 1]"}, map[string]any{"media_indexes": []any{float64(2), float64(1)}}},
		{"non-array property untouched", map[string]any{"detail": "[high]"}, map[string]any{"detail": "[high]"}},
		{"real array untouched", map[string]any{"message_ids": []any{"a"}}, map[string]any{"message_ids": []any{"a"}}},
		{"bad integer left for validation", map[string]any{"media_indexes": "first"}, map[string]any{"media_indexes": "first"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := coerceToolInputArrays(schema, tc.input); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestCoerceToolInputArraysDoesNotMutateOriginal(t *testing.T) {
	schema := map[string]any{"properties": map[string]any{"names": map[string]any{"type": "array"}}}
	input := map[string]any{"names": "remote_image"}
	got := coerceToolInputArrays(schema, input)
	if input["names"] != "remote_image" {
		t.Fatalf("original input mutated: %#v", input)
	}
	if err := validateToolInput((&deferredToolLoader{}).InputSchema(), got); err != nil {
		t.Fatalf("coerced tools_load input still invalid: %v", err)
	}
}
