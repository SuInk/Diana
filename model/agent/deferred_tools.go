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
		builder.WriteString(deferredCatalogLine(tool))
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
		return l.registry.denialError(name)
	}
	return fmt.Errorf("工具 %q 不存在或已禁用；请重新选择 tools_load 名称", name)
}

// missingToolError 是「根本没有这个工具」时的回话，多半是模型自己编了个名字（线上见过
// diana.interactive_browser_take_screenshot）。以前这种名字也被当成延迟工具，回一句
// 「请先 tools_load」，模型照做，再撞一次「不存在」，白烧两步。
func (l *deferredToolLoader) missingToolError(name string) error {
	if l.registry.PolicyDenied(name) {
		return l.registry.denialError(name)
	}
	return fmt.Errorf("工具 %q 不存在。工具名要和目录里的一字不差，不加 diana. 之类的前缀；常驻工具直接调用，目录里的延迟工具先 tools_load 再 tools_execute", name)
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
			if _, exists := l.registry.Get(name); !exists {
				return action, l.missingToolError(name)
			}
			return action, fmt.Errorf("工具 %q 未在本轮加载，请先 tools_load，再 tools_execute", name)
		}
		action.Input = coerceToolInputArrays(schema, action.Input)
		input = action.Input
		tool, ok := l.registry.Get(name)
		if !ok {
			if l.registry.PolicyDenied(name) {
				return action, l.registry.denialError(name)
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
			if _, exists := l.registry.Get(action.Tool); !exists {
				return action, l.missingToolError(action.Tool)
			}
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

// ResolveCoreTools 算出这一轮每步都带完整定义的工具。
//
// base 是内置推荐名单，owners 把 ID 映射到它注册的工具名（插件、MCP 服务各自一份，
// 每个工具自己也有一条），overrides 是 `.extension-overrides.json` 里这台机器人的值。
//
// 这台机器人列过自己的名单，就完全以名单为准：名单里的 ID 展开成工具，没列进去的
// 一律按需。名单是一份清单而不是一组「相对默认的修改」——用户要的是「加进来」和
// 「拿出去」两个动作，那么存下来的就该是动作的结果本身。代价是以后版本往推荐名单里
// 加的新工具不会自动进已经列过名单的机器人，界面上「恢复推荐名单」退回跟随。
//
// 没列过就原样用推荐名单。
//
// 返回的顺序跟着 base 走，剩下的按工具名排序：这个数组直接决定请求里 tools 的顺序，
// 顺序一抖前缀缓存就断。
func ResolveCoreTools(base []string, owners map[string][]string, overrides map[string]bool) []string {
	resident := map[string]bool{}
	if listed, ok := ResidencyList(overrides); ok {
		for _, id := range listed {
			for _, name := range owners[id] {
				if name = strings.TrimSpace(name); name != "" {
					resident[name] = true
				}
			}
		}
	} else {
		for _, name := range base {
			if name = strings.TrimSpace(name); name != "" {
				resident[name] = true
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

// ToolOwners 把当前注册表里的工具按「档位单位」分组，一个工具可以属于两个单位：
// 它所在的 MCP 服务或插件（整条一档），以及它自己（`tool:` 那条，单独覆盖）。
// 两层都要：常驻与否通常按整条判断——一次运行要么用得上这套浏览器工具要么用不上——
// 但偶尔就是有「这条服务留着，其中最贵的那个工具踢出去」的需求。
//
// 直接挂在运行时上的内置工具不属于任何扩展，只有自己那一条。
func (r *ToolRegistry) ToolOwners() map[string][]string {
	if r == nil {
		return nil
	}
	owners := map[string][]string{}
	for _, state := range r.Extensions() {
		// MCP 服务和供给工具的插件都按整条配档；Skill 不在此列，它的正文走另一条路。
		if len(state.Tools) == 0 || (state.Kind != ExtensionKindMCP && state.Kind != ExtensionKindBuiltin) {
			continue
		}
		names := make([]string, 0, len(state.Tools))
		for _, name := range state.Tools {
			if _, ok := r.Get(name); !ok {
				continue
			}
			names = append(names, name)
		}
		if len(names) > 0 {
			owners[state.ID] = names
		}
	}
	for _, name := range r.Names() {
		owners[ToolResidentID(name)] = []string{name}
	}
	return owners
}
