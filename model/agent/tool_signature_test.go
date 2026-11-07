// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestSchemaSignatureRendersCompactTypeScript(t *testing.T) {
	cases := []struct {
		name   string
		schema string
		want   string
	}{
		{"required first then optional, both sorted", `{"type":"object","properties":{"urls":{"type":"array","items":{"type":"string"}},"url":{"type":"string"},"find":{"type":"string"}},"required":["url"]}`, "(url: string, find?: string, urls?: string[])"},
		{"enum and integer", `{"type":"object","properties":{"action":{"type":"string","enum":["view","send"]},"count":{"type":"integer","minimum":1}},"required":["action"]}`, `(action: "view" | "send", count?: number)`},
		{"nested object and array of objects", `{"type":"object","properties":{"items":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"},"n":{"type":"number"}},"required":["id"]}},"opts":{"type":"object","properties":{"deep":{"type":"boolean"}}}}}`, "(items?: { id: string; n?: number }[], opts?: { deep?: boolean })"},
		{"anyOf union in array", `{"type":"object","properties":{"v":{"type":"array","items":{"anyOf":[{"type":"string"},{"type":"number"}]}}}}`, "(v?: (string | number)[])"},
		{"type list and const", `{"type":"object","properties":{"a":{"type":["string","null"]},"b":{"const":3}}}`, "(a?: string | null, b?: 3)"},
		{"quoted key", `{"type":"object","properties":{"x-y":{"type":"string"}}}`, `("x-y"?: string)`},
		{"no fields", `{"type":"object","properties":{},"additionalProperties":false}`, "()"},
		{"free form", `{"type":"object","additionalProperties":true}`, "(" + catalogSignatureFreeForm + ")"},
		{"unknown stays unknown", `{"type":"object","properties":{"a":{"format":"weird"}}}`, "(a?: unknown)"},
	}
	for _, tc := range cases {
		got, err := signatureFromJSON([]byte(tc.schema))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
}

func TestSchemaSignatureFallsBackWhenTooLong(t *testing.T) {
	properties := map[string]any{}
	for i := 0; i < 60; i++ {
		properties["field_number_"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+string(rune('a'+i/26))] = map[string]any{"type": "string"}
	}
	got := schemaSignature(map[string]any{"type": "object", "properties": properties})
	if got != "("+catalogSignatureOverflow+")" {
		t.Fatalf("got %s", got)
	}
}

func TestSchemaSignatureDepthLimit(t *testing.T) {
	leaf := map[string]any{"type": "object", "properties": map[string]any{"z": map[string]any{"type": "string"}}}
	l3 := map[string]any{"type": "object", "properties": map[string]any{"c": leaf}}
	l2 := map[string]any{"type": "object", "properties": map[string]any{"b": l3}}
	got := schemaSignature(map[string]any{"type": "object", "properties": map[string]any{"a": l2}})
	if got != "(a?: { b?: { c?: object } })" {
		t.Fatalf("got %s", got)
	}
}

// 目录行一眼能看出怎么填：名字、签名、说明。
func TestDeferredCatalogLineCarriesSignature(t *testing.T) {
	tool := &schemaTool{name: "remote_image", description: "看图或发图", schema: map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"action"},
		"properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"view", "send"}},
			"url":    map[string]any{"type": "string"},
		},
	}}
	if got := deferredCatalogLine(tool); got != `- remote_image(action: "view" | "send", url?: string): 看图或发图`+"\n" {
		t.Fatalf("line = %q", got)
	}
}

type schemaTool struct {
	name, description string
	schema            map[string]any
}

func (t *schemaTool) Name() string                { return t.name }
func (t *schemaTool) Description() string         { return t.description }
func (t *schemaTool) InputSchema() map[string]any { return t.schema }
func (t *schemaTool) Run(_ context.Context, _ map[string]any) (string, error) {
	return "", nil
}

// 临时核对：DIANA_SIGNATURE_DUMP 指向一份 {name: {name, description, inputSchema}} 的
// JSON（从生产调试记录里扒的契约），打印每个工具的签名和长度，用来定 catalogSignatureMaxRunes。
func TestDumpSignaturesFromContracts(t *testing.T) {
	path := os.Getenv("DIANA_SIGNATURE_DUMP")
	if path == "" {
		t.Skip("set DIANA_SIGNATURE_DUMP to a contracts JSON file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var contracts map[string]struct {
		InputSchema map[string]any `json:"inputSchema"`
	}
	if err := json.Unmarshal(raw, &contracts); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(contracts))
	for name := range contracts {
		names = append(names, name)
	}
	sort.Strings(names)
	total := 0
	var out strings.Builder
	for _, name := range names {
		signature := schemaSignature(contracts[name].InputSchema)
		full := schemaSignatureLimit(contracts[name].InputSchema, 1<<20)
		total += len([]rune(signature))
		out.WriteString(strconv.Itoa(len([]rune(signature))) + "\t" + strconv.Itoa(len([]rune(full))) + "\t" + name + signature + "\n")
		if signature != full {
			out.WriteString("\t\t" + name + full + "\n")
		}
	}
	out.WriteString("total signature runes: " + strconv.Itoa(total) + " over " + strconv.Itoa(len(names)) + " tools\n")
	t.Log(out.String())
	if target := os.Getenv("DIANA_SIGNATURE_DUMP_OUT"); target != "" {
		if err := os.WriteFile(target, []byte(out.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
