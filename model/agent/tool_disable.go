// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"fmt"
	"sort"
	"strings"
)

// 被配置整体关掉的能力（例如机器人的安全模式）和「身份不够」是两回事：后者换主人来
// 说就能做，前者连主人也做不了，得先去改设置。所以这里单独记一份原因，取不到工具时
// 回的是这句原因，而不是 deniedToolError 那句「需要主人来做」——主人本人在对话里
// 也会撞上这道闸，再让他「找主人」只会让模型原地打转。
//
// 名字始终留在注册表里（记成 hidden），不静默删掉：模型点名要用时能得到明确的
// 拒绝理由，而不是「查无此工具」然后换个名字接着猜。

// DisabledOperation 是一个工具里被关掉的那几种操作，其余操作照常可用。
// Field 是工具入参里选操作的字段名（operation / action），Values 是被关掉的取值。
type DisabledOperation struct {
	Tool   string
	Field  string
	Values []string
}

type toolDisableState struct {
	reasons    map[string]string
	operations map[string]disabledOperationRule
	mcpReason  string
}

type disabledOperationRule struct {
	field  string
	values map[string]bool
	reason string
}

// DisableTools 把这些工具整体关掉：已注册的摘掉，共享底座上的同名工具也不再回落，
// 之后再 Register 同名工具同样无效。reason 是模型调用时收到的原话。
func (r *ToolRegistry) DisableTools(reason string, names ...string) {
	if r == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		r.Remove(name)
		r.mu.Lock()
		if r.disabled.reasons == nil {
			r.disabled.reasons = map[string]string{}
		}
		r.disabled.reasons[name] = reason
		r.mu.Unlock()
	}
}

// DisableOperations 只关掉工具里的某几种操作。工具本身照常注册、照常出现在目录里，
// 读类操作不受影响；调用被关掉的操作时 Runner 不执行，直接把 reason 交给模型。
func (r *ToolRegistry) DisableOperations(reason string, rules ...DisabledOperation) {
	if r == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rule := range rules {
		tool := strings.TrimSpace(rule.Tool)
		field := strings.TrimSpace(rule.Field)
		if tool == "" || field == "" || len(rule.Values) == 0 {
			continue
		}
		if r.disabled.operations == nil {
			r.disabled.operations = map[string]disabledOperationRule{}
		}
		current, ok := r.disabled.operations[tool]
		if !ok || current.field != field {
			current = disabledOperationRule{field: field, values: map[string]bool{}}
		}
		for _, value := range rule.Values {
			// 各工具读操作名时大多会先转小写，这里同样按小写比，免得 "MUTE" 绕过去。
			if value = strings.ToLower(strings.TrimSpace(value)); value != "" {
				current.values[value] = true
			}
		}
		current.reason = reason
		r.disabled.operations[tool] = current
	}
}

// DisableMCPTools 关掉全部 MCP 工具，包括之后才发现的：MCP 服务是第三方程序，
// 带着本进程的全部权限在跑，按名字一个个摘挡不住新冒出来的工具。
func (r *ToolRegistry) DisableMCPTools(reason string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.disabled.mcpReason = strings.TrimSpace(reason)
	if r.disabled.mcpReason == "" {
		r.disabled.mcpReason = "MCP 工具已被关闭"
	}
	// list_capabilities 建视图时直接拿了底层目录，看不到这里的停用标记；换成读注册表
	// 自己的版本，目录里的 MCP 服务才会标成停用并带上原因。扩展管理此时一并视为关闭。
	if _, ok := r.tools[extensionsListToolName]; ok {
		r.tools[extensionsListToolName] = NewExtensionsListTool(r, false)
	}
	r.mu.Unlock()
}

// DisabledReason 回答这个工具是不是被配置整体关掉了，以及原因。
func (r *ToolRegistry) DisabledReason(name string) (string, bool) {
	if r == nil {
		return "", false
	}
	name = strings.TrimSpace(name)
	r.mu.RLock()
	defer r.mu.RUnlock()
	if reason, ok := r.disabled.reasons[name]; ok {
		return reason, true
	}
	if r.disabled.mcpReason != "" && strings.HasPrefix(name, mcpToolNamePrefix) {
		return r.disabled.mcpReason, true
	}
	return "", false
}

// CanonicalOperationTool 由按操作分档的工具实现：给出这次调用实际会执行的操作名，
// 已经套用工具自己的别名表和缺省值。
//
// 只比对入参原文拦不住：github 把 create_issue、new 都当 create，llm_config 没写
// operation 就按 update 执行。拦截必须和工具执行时用同一套换算，所以换算留在工具里，
// 这里只问结果。同一个动词风险不同时（比如盯当前会话还是盯别的群），工具可以返回
// 更细的名字，规则按那个名字写。
type CanonicalOperationTool interface {
	CanonicalOperation(input map[string]any) string
}

// OperationDisabledError 在这次调用选中了被关掉的操作时返回拒绝原因，否则返回 nil。
// 工具实现了 CanonicalOperationTool 就按它换算后的操作名比；没实现的才退回比入参原文。
func (r *ToolRegistry) OperationDisabledError(name string, input map[string]any) error {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	rule, ok := r.disabled.operations[name]
	r.mu.RUnlock()
	if !ok {
		return nil
	}
	value, _ := input[rule.field].(string)
	if tool, found := r.Get(name); found {
		if canonical, ok := tool.(CanonicalOperationTool); ok {
			value = canonical.CanonicalOperation(input)
		}
	}
	value = strings.ToLower(strings.TrimSpace(value))
	if !rule.values[value] {
		return nil
	}
	return fmt.Errorf("工具 %q 的 %s=%s 不可用：%s。不要重试，也不要换别的工具绕过", name, rule.field, value, rule.reason)
}

// DisabledSummary 给系统提示词用：列出被整体关掉的工具和被关掉的操作，让模型一开始
// 就知道哪些事这轮做不了，不必先撞一次。没有关掉任何东西时返回空串。
func (r *ToolRegistry) DisabledSummary() string {
	if r == nil {
		return ""
	}
	r.mu.RLock()
	byReason := map[string][]string{}
	for name, reason := range r.disabled.reasons {
		byReason[reason] = append(byReason[reason], name)
	}
	for tool, rule := range r.disabled.operations {
		values := make([]string, 0, len(rule.values))
		for value := range rule.values {
			values = append(values, value)
		}
		sort.Strings(values)
		byReason[rule.reason] = append(byReason[rule.reason], tool+"（"+rule.field+"="+strings.Join(values, "/")+"）")
	}
	if r.disabled.mcpReason != "" {
		byReason[r.disabled.mcpReason] = append(byReason[r.disabled.mcpReason], "全部 MCP 工具（"+mcpToolNamePrefix+"*）")
	}
	r.mu.RUnlock()
	if len(byReason) == 0 {
		return ""
	}
	reasons := make([]string, 0, len(byReason))
	for reason := range byReason {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	var lines []string
	for _, reason := range reasons {
		names := byReason[reason]
		sort.Strings(names)
		lines = append(lines, "- "+reason+"："+strings.Join(names, "、"))
	}
	return "以下工具或操作在本轮被关掉了，调用只会被拒绝；用户要求时直接说明做不到以及原因，不要尝试，也不要换别的工具绕过：\n" + strings.Join(lines, "\n")
}

// denialError 是名字取不到、且确属权限问题时交给模型的那句话。
func (r *ToolRegistry) denialError(name string) error {
	if reason, ok := r.DisabledReason(name); ok {
		return fmt.Errorf("工具 %q 不可用：%s。不要重试，也不要换别的工具绕过", name, reason)
	}
	return deniedToolError(name)
}

func (r *ToolRegistry) disabledLocked(name string) bool {
	if _, ok := r.disabled.reasons[name]; ok {
		return true
	}
	return false
}
