// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import "sort"

// strictToolSchema rewrites a plain input schema into the strict function
// calling subset: every object rejects undeclared properties and lists all of
// its properties as required, with previously optional properties widened to
// also accept null. Providers compile that form into a decoding grammar, so a
// malformed tool call cannot be produced at all.
//
// The rewrite happens at the provider boundary rather than in the tool itself,
// so tools keep one readable schema and providers that constrain decoding from
// plain JSON Schema keep seeing genuinely optional arguments.
func strictToolSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	out := make(map[string]any, len(schema)+2)
	for key, value := range schema {
		out[key] = value
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return out
	}
	required := map[string]bool{}
	for _, name := range schemaRequiredNames(schema) {
		required[name] = true
	}
	rewritten := make(map[string]any, len(properties))
	names := make([]string, 0, len(properties))
	for name, raw := range properties {
		names = append(names, name)
		property, isObject := raw.(map[string]any)
		if !isObject {
			rewritten[name] = raw
			continue
		}
		property = strictToolSchema(property)
		if items, hasItems := property["items"].(map[string]any); hasItems {
			property["items"] = strictToolSchema(items)
		}
		if !required[name] {
			property = nullableProperty(property)
		}
		rewritten[name] = property
	}
	sort.Strings(names)
	out["properties"] = rewritten
	out["required"] = names
	out["additionalProperties"] = false
	return out
}

// nullableProperty widens a property so an argument the model does not need can
// still be emitted as null, which is how the strict subset expresses optional.
func nullableProperty(property map[string]any) map[string]any {
	switch declared := property["type"].(type) {
	case string:
		if declared == "null" {
			return property
		}
		property["type"] = []any{declared, "null"}
	case []any:
		for _, value := range declared {
			if value == "null" {
				return property
			}
		}
		property["type"] = append(append([]any{}, declared...), "null")
	case []string:
		for _, value := range declared {
			if value == "null" {
				return property
			}
		}
		widened := make([]any, 0, len(declared)+1)
		for _, value := range declared {
			widened = append(widened, value)
		}
		property["type"] = append(widened, "null")
	default:
		// A property without a declared type already accepts null.
		return property
	}
	// An enum has to admit null as well, otherwise the widened type can never
	// actually be satisfied by null.
	switch values := property["enum"].(type) {
	case []string:
		widened := make([]any, 0, len(values)+1)
		for _, value := range values {
			widened = append(widened, value)
		}
		property["enum"] = append(widened, nil)
	case []any:
		property["enum"] = append(append([]any{}, values...), nil)
	}
	return property
}

func schemaRequiredNames(schema map[string]any) []string {
	switch values := schema["required"].(type) {
	case []string:
		return values
	case []any:
		names := make([]string, 0, len(values))
		for _, value := range values {
			if name, ok := value.(string); ok {
				names = append(names, name)
			}
		}
		return names
	default:
		return nil
	}
}

// strictToolDefinitions returns the definitions with strict-marked schemas
// rewritten. Definitions that do not ask for strict decoding pass through.
func strictToolDefinitions(definitions []ToolDefinition) []ToolDefinition {
	out := make([]ToolDefinition, len(definitions))
	copy(out, definitions)
	for index := range out {
		if out[index].Strict {
			out[index].Parameters = strictToolSchema(out[index].Parameters)
		}
	}
	return out
}

// withoutStrictTools drops the strict request, used to retry once against a
// gateway that rejects the strict subset.
func withoutStrictTools(req GenerateRequest) GenerateRequest {
	if !requestHasStrictTools(req) {
		return req
	}
	tools := make([]ToolDefinition, len(req.Tools))
	copy(tools, req.Tools)
	for index := range tools {
		tools[index].Strict = false
	}
	req.Tools = tools
	return req
}

func requestHasStrictTools(req GenerateRequest) bool {
	for _, definition := range req.Tools {
		if definition.Strict {
			return true
		}
	}
	return false
}
