// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// 原始文本输入的自定义工具（照 Pi 的 codemode：constrainedSampling + openai_lark）。
//
// 同一个请求里，一个工具要么全按自定义工具发、要么全按函数工具发：调用和结果必须
// 成对同型，否则上游整轮拒绝。所以按「这次请求里这个工具带不带 Freeform」决定，
// 回放历史时把另一种形态的调用和结果改写过来——网关降级后、或从别的上游切过来时，
// 历史里两种形态都可能有。

const downgradeFieldCustomTools = "custom_tools"

// freeformFields 返回这次请求里按自定义工具发的工具名和它们的文本参数名。
func freeformFields(definitions []ToolDefinition) map[string]string {
	var fields map[string]string
	for _, definition := range definitions {
		if definition.Freeform == nil || strings.TrimSpace(definition.Freeform.Field) == "" {
			continue
		}
		if fields == nil {
			fields = map[string]string{}
		}
		fields[wireToolName(definition.Name)] = definition.Freeform.Field
		fields[definition.Name] = definition.Freeform.Field
	}
	return fields
}

func openAIResponseCustomTool(definition ToolDefinition) responses.ToolUnionParam {
	description := definition.Description
	if hint := strings.TrimSpace(definition.Freeform.Hint); hint != "" {
		description = strings.TrimSpace(description + " " + hint)
	}
	tool := &responses.CustomToolParam{Name: wireToolName(definition.Name), Description: param.NewOpt(description)}
	if grammar := strings.TrimSpace(definition.Freeform.Grammar); grammar != "" {
		tool.Format = shared.CustomToolInputFormatUnionParam{OfGrammar: &shared.CustomToolInputFormatGrammarParam{Definition: grammar, Syntax: "lark"}}
	}
	return responses.ToolUnionParam{OfCustom: tool}
}

// freeformArguments 把模型写的原始文本还原成参数。第一行可以是
// `// @options: {...}`（同 Pi），里面的字段并进参数，文本本身是其余部分。
func freeformArguments(field, input string) map[string]any {
	arguments := map[string]any{}
	body := input
	if first, rest, found := strings.Cut(input, "\n"); strings.HasPrefix(strings.TrimSpace(first), "// @options:") {
		options := map[string]any{}
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(first), "// @options:"))), &options) == nil {
			for key, value := range options {
				arguments[key] = value
			}
			if found {
				body = rest
			} else {
				body = ""
			}
		}
	}
	arguments[field] = body
	return arguments
}

// freeformInput 是 freeformArguments 的反向：把参数写回原始文本，回放历史用。
func freeformInput(field string, arguments map[string]any) string {
	body, _ := arguments[field].(string)
	options := map[string]any{}
	for key, value := range arguments {
		if key != field {
			options[key] = value
		}
	}
	if len(options) == 0 {
		return body
	}
	encoded, err := json.Marshal(options)
	if err != nil {
		return body
	}
	return "// @options: " + string(encoded) + "\n" + body
}

// normalizeFreeformItems 把历史里原样回放的调用和结果改成这次请求该用的形态。
// custom 记下哪些 call_id 按自定义工具发，结果消息照它选输出类型。
func normalizeFreeformItems(raws []json.RawMessage, fields map[string]string, custom map[string]bool) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(raws))
	for _, raw := range raws {
		var head struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Input     string `json:"input"`
			Arguments string `json:"arguments"`
		}
		if json.Unmarshal(raw, &head) != nil {
			out = append(out, raw)
			continue
		}
		field, freeform := fields[head.Name]
		switch {
		case head.Type == "custom_tool_call" && freeform:
			custom[head.CallID] = true
			out = append(out, raw)
		case head.Type == "custom_tool_call":
			// 这次按函数工具发：改写成 function_call，文本按 Pi 的规矩放进 code 之类的参数。
			arguments, _ := json.Marshal(freeformArguments(firstNonEmptyString(fieldForCustomCall(head.Name), "input"), head.Input))
			encoded, _ := json.Marshal(map[string]any{"type": "function_call", "call_id": head.CallID, "name": head.Name, "arguments": string(arguments)})
			out = append(out, encoded)
		case head.Type == "function_call" && freeform:
			arguments := map[string]any{}
			_ = json.Unmarshal([]byte(head.Arguments), &arguments)
			encoded, _ := json.Marshal(map[string]any{"type": "custom_tool_call", "call_id": head.CallID, "name": head.Name, "input": freeformInput(field, arguments)})
			custom[head.CallID] = true
			out = append(out, encoded)
		default:
			out = append(out, raw)
		}
	}
	return out
}

// freeformToolFields 记下进程里见过的自定义工具和它们的文本参数名。降级后这次请求
// 不带 Freeform，回放历史里的 custom_tool_call 时仍要知道文本该落进哪个参数。
var freeformToolFields = struct {
	sync.RWMutex
	m map[string]string
}{m: map[string]string{}}

func rememberFreeformFields(definitions []ToolDefinition) {
	freeformToolFields.Lock()
	defer freeformToolFields.Unlock()
	for _, definition := range definitions {
		if definition.Freeform != nil && definition.Freeform.Field != "" {
			freeformToolFields.m[wireToolName(definition.Name)] = definition.Freeform.Field
		}
	}
}

func fieldForCustomCall(name string) string {
	freeformToolFields.RLock()
	defer freeformToolFields.RUnlock()
	return freeformToolFields.m[name]
}

// customToolsRejected 认出网关不支持自定义工具（DeepSeek：Unsupported custom tool）。
func customToolsRejected(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "custom tool") || strings.Contains(text, "custom_tool") || (strings.Contains(text, "unsupported") && strings.Contains(text, "custom"))
}

func requestHasFreeformTools(req GenerateRequest) bool {
	for _, definition := range req.Tools {
		if definition.Freeform != nil {
			return true
		}
	}
	return false
}

func stripFreeformTools(req GenerateRequest) (GenerateRequest, bool) {
	if !requestHasFreeformTools(req) {
		return req, false
	}
	rememberFreeformFields(req.Tools)
	tools := make([]ToolDefinition, len(req.Tools))
	for index, definition := range req.Tools {
		definition.Freeform = nil
		tools[index] = definition
	}
	req.Tools = tools
	return req, true
}
