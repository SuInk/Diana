// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

// 工具按需加载。
//
// 每个规划步都把全部工具的完整定义发给模型：线上一次主回复约 3.9 万 token，其中 29 个
// 工具定义占了 37% 的字符。可近 7 天 4642 次 Agent 运行里，只有搜索、历史媒体、聊天
// 记录、线程状态、生图、网页渲染这几个被几十上百次用到，其余每个最多几十次；而 79% 的
// 回复一个工具都不调，每一轮都在为用不上的工具定义付输入 token。
//
// 配置了 CoreTools 时，只有常驻工具带完整定义；其余工具在系统提示词里各留一行简介，
// 模型需要时先调用 tools.load，下一步起就能直接调用。没配置时行为和以前完全一样。
const ToolsLoadToolName = "tools.load"

type deferredToolLoader struct {
	registry *ToolRegistry
	core     map[string]bool
	loaded   map[string]bool
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
	loader := &deferredToolLoader{registry: registry, core: core, loaded: map[string]bool{}}
	if len(loader.deferredNames()) == 0 {
		return nil
	}
	return loader
}

// deferredNames 是还没带完整定义的工具，按注册顺序，保证系统提示词每次逐字节相同。
func (l *deferredToolLoader) deferredNames() []string {
	if l == nil {
		return nil
	}
	var names []string
	for _, name := range l.registry.Names() {
		if !l.core[name] && !l.loaded[name] {
			names = append(names, name)
		}
	}
	return names
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

// filter 只保留常驻和已加载工具的定义；还有没加载的工具时附上 tools.load。
func (l *deferredToolLoader) filter(definitions []llm.ToolDefinition) []llm.ToolDefinition {
	if l == nil {
		return definitions
	}
	out := make([]llm.ToolDefinition, 0, len(definitions)+1)
	for _, definition := range definitions {
		if l.core[definition.Name] || l.loaded[definition.Name] {
			out = append(out, definition)
		}
	}
	if len(l.deferredNames()) > 0 {
		out = append(out, llm.ToolDefinition{
			Name:        ToolsLoadToolName,
			Description: l.Description(),
			Parameters:  l.InputSchema(),
		})
	}
	return out
}

func (l *deferredToolLoader) Name() string { return ToolsLoadToolName }

func (l *deferredToolLoader) Description() string {
	return "加载系统提示词「按需加载的工具」里列出的工具。names 传工具名；加载后从下一步起就能直接调用它们。只加载这一轮确实要用的工具，不要为了看看有什么而加载。"
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
	var requested []string
	switch values := input["names"].(type) {
	case []any:
		for _, value := range values {
			if name, ok := value.(string); ok {
				requested = append(requested, strings.TrimSpace(name))
			}
		}
	case []string:
		requested = append(requested, values...)
	case string:
		requested = append(requested, strings.TrimSpace(values))
	}
	if len(requested) == 0 {
		return "", fmt.Errorf("names 不能为空")
	}
	var loaded, unknown []string
	var details strings.Builder
	for _, name := range requested {
		tool, ok := l.registry.Get(name)
		if name == "" || !ok {
			unknown = append(unknown, name)
			continue
		}
		if !l.core[name] {
			l.loaded[name] = true
		}
		loaded = append(loaded, name)
		details.WriteString("\n- ")
		details.WriteString(name)
		details.WriteString(": ")
		details.WriteString(compactToolDescription(tool.Description(), ToolDescriptionBudget))
	}
	if len(loaded) == 0 {
		return "", fmt.Errorf("没有找到这些工具：%s", strings.Join(unknown, "、"))
	}
	result := "已加载：" + strings.Join(loaded, "、") + "。下一步起可以直接调用，参数以工具定义为准。" + details.String()
	if len(unknown) > 0 {
		result += "\n没有找到：" + strings.Join(unknown, "、")
	}
	return result, nil
}
