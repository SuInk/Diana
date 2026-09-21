// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

// Deferred tools expose their contract through messages, never by mutating API declarations.
const ToolsLoadToolName = "tools_load"
const ToolsExecuteToolName = "tools_execute"
const maxLoadedContractChars = 128 * 1024

type deferredToolLoader struct {
	registry *ToolRegistry
	core     map[string]bool
	loaded   map[string]map[string]any
	order    []string
	onLoad   func([]string)
}

func newDeferredToolLoader(registry *ToolRegistry, coreTools []string) *deferredToolLoader {
	if registry == nil || len(coreTools) == 0 {
		return nil
	}
	core := make(map[string]bool, len(coreTools))
	for _, name := range coreTools {
		if name = strings.TrimSpace(name); name != "" {
			core[name] = true
		}
	}
	// Keep the dispatcher active even when the registry currently contains only
	// core tools: a tool installed later in this Run is still deferred.
	return &deferredToolLoader{registry: registry, core: core, loaded: map[string]map[string]any{}}
}

// catalog 列出所有非常驻工具，不看本轮已加载的状态：系统提示词要保持稳定才能命中缓存。
func (l *deferredToolLoader) catalog() string {
	var builder strings.Builder
	for _, name := range l.registry.Names() {
		if l.core[name] {
			continue
		}
		tool, ok := l.registry.Get(name)
		if !ok {
			continue
		}
		builder.WriteString("- ")
		builder.WriteString(name)
		builder.WriteString(": ")
		builder.WriteString(compactToolDescription(tool.Description(), SystemPromptToolDescriptionBudget))
		builder.WriteByte('\n')
	}
	return strings.TrimSpace(builder.String())
}

// filter keeps declarations stable before and after loading any deferred tool.
func (l *deferredToolLoader) filter(definitions []llm.ToolDefinition) []llm.ToolDefinition {
	if l == nil {
		return definitions
	}
	out := make([]llm.ToolDefinition, 0, len(definitions)+2)
	for _, definition := range definitions {
		if l.core[definition.Name] || definition.Name == finalizeToolName {
			out = append(out, definition)
		}
	}
	return append(out,
		llm.ToolDefinition{Name: ToolsLoadToolName, Description: l.Description(), Parameters: l.InputSchema()},
		llm.ToolDefinition{Name: ToolsExecuteToolName, Description: "执行本轮 tools_load 已加载的工具；name 为工具名，input 必须符合加载返回的 inputSchema。", Parameters: executeInputSchema()},
	)
}

func (l *deferredToolLoader) restore(names []string) {
	if l == nil {
		return
	}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || l.core[name] {
			continue
		}
		tool, allowed := l.registry.Get(name)
		if !allowed {
			continue
		}
		schema, err := snapshotToolSchema(tool)
		if err != nil {
			continue
		}
		if _, exists := l.loaded[name]; !exists {
			l.order = append(l.order, name)
		}
		l.loaded[name] = schema
	}
}

func (l *deferredToolLoader) loadedContracts() string {
	if l == nil || len(l.order) == 0 {
		return ""
	}
	type contract struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		InputSchema map[string]any `json:"inputSchema"`
	}
	contracts := make([]contract, 0, len(l.order))
	for _, name := range l.order {
		tool, ok := l.registry.Get(name)
		if !ok {
			continue
		}
		contracts = append(contracts, contract{Name: name, Description: tool.Description(), InputSchema: l.loaded[name]})
	}
	raw, _ := json.Marshal(contracts)
	return string(raw)
}

func executeInputSchema() map[string]any {
	return toolObjectSchema([]string{"name", "input"}, map[string]any{
		"name":  map[string]any{"type": "string", "minLength": 1},
		"input": map[string]any{"type": "object", "additionalProperties": true},
	})
}

func (l *deferredToolLoader) Name() string { return ToolsLoadToolName }

func (l *deferredToolLoader) Description() string {
	return "加载系统提示词「按需加载的工具」里列出的工具。names 传工具名；加载结果包含完整契约，随后通过 tools_execute(name,input) 调用。已加载工具会在当前群会话后续运行中保留。只加载确实要用的工具。"
}

func (l *deferredToolLoader) InputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"names"},
		"properties": map[string]any{
			"names": map[string]any{
				"type":        "array",
				"description": "要加载的工具名，取自按需加载的工具目录。",
				"items":       map[string]any{"type": "string"},
				"minItems":    1,
				"maxItems":    8,
			},
		},
	}
}

func (l *deferredToolLoader) Run(_ context.Context, input map[string]any) (string, error) {
	if err := validateToolInput(l.InputSchema(), input); err != nil {
		return "", err
	}
	raw, _ := json.Marshal(input["names"])
	var requested []string
	if err := json.Unmarshal(raw, &requested); err != nil {
		return "", fmt.Errorf("names 必须是工具名数组")
	}
	type contract struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		InputSchema map[string]any `json:"inputSchema"`
	}
	contracts := []contract{}
	snapshots := map[string]map[string]any{}
	for _, name := range requested {
		tool, ok := l.registry.Get(name)
		if !ok || name == "" {
			return "", l.unavailableToolError(name)
		}
		schema, err := snapshotToolSchema(tool)
		if err != nil {
			return "", fmt.Errorf("工具 %q 的 inputSchema 无效", name)
		}
		contracts = append(contracts, contract{name, tool.Description(), schema})
		if !l.core[name] {
			snapshots[name] = schema
		}
	}
	result, err := json.Marshal(map[string]any{"loaded": contracts, "instruction": "使用 tools_execute，把目标工具名放入 name、参数对象放入 input；当前群会话后续运行会保留加载状态。"})
	if err != nil {
		return "", fmt.Errorf("无法编码工具契约")
	}
	if len([]rune(string(result))) > maxLoadedContractChars {
		return "", fmt.Errorf("完整工具契约超过 %d 字符，请减少加载数量；单个工具超限需缩小其 schema 或描述", maxLoadedContractChars)
	}
	// Commit only after all contracts fit; never mark a truncated/unseen schema loaded.
	for _, name := range requested {
		schema, ok := snapshots[name]
		if !ok {
			continue
		}
		if _, exists := l.loaded[name]; !exists {
			l.order = append(l.order, name)
		}
		l.loaded[name] = schema
	}
	if l.onLoad != nil {
		l.onLoad(append([]string(nil), l.order...))
	}
	return string(result), nil
}

// 名字取不到时把两种原因分开：查无此工具要换一个名字，没权限则换名字也没用，
// 得让模型改口告诉用户，而不是在协议修复次数里对着同一个名字空转。
func (l *deferredToolLoader) unavailableToolError(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("工具名不能为空；请从目录里选一个名称")
	}
	if l.registry.PolicyDenied(name) {
		return deniedToolError(name)
	}
	return fmt.Errorf("工具 %q 不存在或已禁用；请重新选择 tools_load 名称", name)
}

func deniedToolError(name string) error {
	return fmt.Errorf("工具 %q 当前会话没有权限使用：它只对主人开放，或者没有对群成员开放。不要重试，直接说明这件事需要主人来做", name)
}

// dispatch expands only the internal action. Provider calls and IDs stay untouched.
func (l *deferredToolLoader) dispatch(action llmAction) (llmAction, error) {
	if action.Tool == ToolsExecuteToolName {
		if l == nil {
			return action, fmt.Errorf("当前未启用延迟工具，不能调用 tools_execute")
		}
		if err := validateToolInput(executeInputSchema(), action.Input); err != nil {
			return action, err
		}
		name := action.Input["name"].(string)
		input, ok := action.Input["input"].(map[string]any)
		if !ok {
			return action, fmt.Errorf("input 必须是 JSON 对象")
		}
		action.Tool, action.Input = name, cloneDeferredInput(input).(map[string]any)
		schema, loaded := l.loaded[name]
		if !loaded && l.core[name] {
			// 常驻工具本来就能直接调用，不进目录也不进 loaded，于是把它裹进
			// tools_execute 时会撞上「未在本轮加载」。这句话对常驻工具是死路：模型照着
			// 去 tools_load，那一步对常驻工具不登记加载状态，回来还是同一个错，一直耗到
			// 协议修复次数用尽（线上 browser_render 就这么连撞两次）。信封拆开照常执行。
			if tool, ok := l.registry.Get(name); ok {
				if current, err := snapshotToolSchema(tool); err == nil {
					schema, loaded = current, true
				}
			}
		}
		if !loaded {
			return action, fmt.Errorf("工具 %q 未在本轮加载，请先 tools_load，再 tools_execute", name)
		}
		action.Input = coerceToolInputArrays(schema, action.Input)
		input = action.Input
		tool, ok := l.registry.Get(name)
		if !ok {
			if l.registry.PolicyDenied(name) {
				return action, deniedToolError(name)
			}
			return action, fmt.Errorf("工具 %q 已移除或禁用，请重新 tools_load", name)
		}
		if err := validateToolInput(schema, input); err != nil {
			return action, err
		}
		current, err := snapshotToolSchema(tool)
		if err != nil {
			return action, fmt.Errorf("工具当前 inputSchema 无效，请重新 tools_load")
		}
		if err := validateToolInput(current, input); err != nil {
			return action, fmt.Errorf("当前工具契约校验失败，请重新 tools_load: %w", err)
		}
		return action, nil
	}
	if l != nil {
		if action.Tool == ToolsLoadToolName {
			action.Input = coerceToolInputArrays(l.InputSchema(), action.Input)
			return action, validateToolInput(l.InputSchema(), action.Input)
		}
		if !l.core[action.Tool] {
			return action, fmt.Errorf("不能直接调用延迟工具 %q；请先 tools_load，再 tools_execute", action.Tool)
		}
	}
	return action, nil
}

// Target tools may normalize or fill their input in place. Do not let that
// mutate the provider's original tools_execute envelope or its nested values.
func cloneDeferredInput(value any) any {
	switch v := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(v))
		for key, item := range v {
			result[key] = cloneDeferredInput(item)
		}
		return result
	case []any:
		result := make([]any, len(v))
		for index, item := range v {
			result[index] = cloneDeferredInput(item)
		}
		return result
	case []string:
		return append([]string(nil), v...)
	default:
		return value
	}
}

// ResolveCoreTools 按用户配的扩展档位调整常驻工具名单。
//
// base 是内置的默认名单，owners 把扩展 ID 映射到它注册的工具名（内置插件、MCP 服务
// 各自一份），overrides 是 `.extension-overrides.json` 里这台机器人的覆盖值。档位只有
// 三种：没配这个键就用默认名单的结果，配成 true 把这个扩展的工具全部常驻，配成 false
// 全部改按需。
//
// 返回的顺序跟着 base 走，新增的按工具名排序：这个数组直接决定请求里 tools 的顺序，
// 顺序一抖前缀缓存就断。扩展 ID 也排了一次序，那是另一回事——同一个工具名被两个扩展
// 声明时，决定谁最后写赢，和输出顺序无关。
func ResolveCoreTools(base []string, owners map[string][]string, overrides map[string]bool) []string {
	resident := make(map[string]bool, len(base))
	for _, name := range base {
		if name = strings.TrimSpace(name); name != "" {
			resident[name] = true
		}
	}
	ids := make([]string, 0, len(owners))
	for id := range owners {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		override := ResidentOverride(overrides, id)
		if override == nil {
			continue
		}
		names := append([]string(nil), owners[id]...)
		sort.Strings(names)
		for _, name := range names {
			if name = strings.TrimSpace(name); name != "" {
				resident[name] = *override
			}
		}
	}
	out := make([]string, 0, len(resident))
	seen := map[string]bool{}
	for _, name := range base {
		if resident[name] && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	added := make([]string, 0, len(resident))
	for name, keep := range resident {
		if keep && !seen[name] {
			added = append(added, name)
		}
	}
	sort.Strings(added)
	return append(out, added...)
}

// ToolOwners 把当前注册表里的工具按「档位单位」分组：MCP 服务的工具归到它自己那条
// 服务，其余内置工具各自成组。内置工具不属于任何插件——它们直接挂在运行时上，按插件
// 分组既分不干净，也没法表达「戳一戳常驻、发文件按需」这种逐个工具的要求。
func (r *ToolRegistry) ToolOwners() map[string][]string {
	if r == nil {
		return nil
	}
	owners := map[string][]string{}
	owned := map[string]bool{}
	for _, state := range r.Extensions() {
		if state.Kind != ExtensionKindMCP || len(state.Tools) == 0 {
			continue
		}
		names := make([]string, 0, len(state.Tools))
		for _, name := range state.Tools {
			if _, ok := r.Get(name); !ok {
				continue
			}
			owned[name] = true
			names = append(names, name)
		}
		if len(names) > 0 {
			owners[state.ID] = names
		}
	}
	for _, name := range r.Names() {
		if owned[name] {
			continue
		}
		owners[ToolResidentID(name)] = []string{name}
	}
	return owners
}
