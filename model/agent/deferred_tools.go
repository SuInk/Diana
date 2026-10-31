// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/SuInk/diana/model/llm"
)

// Deferred tools expose their contract through messages, never by mutating API declarations.
//
// ToolsLoadToolName 现在叫 tool_search，和 Codex CLI、Claude Code 的叫法一致：按目录里的
// 确切名字取，或者按关键词搜。常量名沿用旧的，免得一处处改调用点。
const ToolsLoadToolName = "tool_search"

// LegacyToolsLoadName 是改名前的叫法。不再出现在工具定义里，但模型照旧这么调时照样认，
// 各群历史里已有的调用也不受影响。
const LegacyToolsLoadName = "tools_load"
const ToolsExecuteToolName = "tools_execute"
const maxLoadedContractChars = 128 * 1024

type deferredToolLoader struct {
	registry *ToolRegistry
	core     map[string]bool
	// shared 为 nil 时所有工具都算公共；见 Config.SharedTools。
	shared   map[string]bool
	featured map[string]bool
	loaded   map[string]map[string]any
	order    []string
	onLoad   func([]string)
}

func newDeferredToolLoader(registry *ToolRegistry, coreTools []string, shared map[string]bool, featured []string) *deferredToolLoader {
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
	featuredSet := make(map[string]bool, len(featured))
	for _, name := range featured {
		featuredSet[name] = true
	}
	return &deferredToolLoader{registry: registry, core: core, shared: shared, featured: featuredSet, loaded: map[string]map[string]any{}}
}

// isShared 判断工具是不是「对谁都一样」的那一份，见 Config.SharedTools。
func (l *deferredToolLoader) isShared(name string) bool {
	return l.shared == nil || l.shared[name]
}

// catalog 列出公共的非常驻工具，不看本轮已加载的状态：系统提示词要保持稳定才能命中缓存。
func (l *deferredToolLoader) catalog() string {
	return l.catalogWhere(true)
}

// speakerCatalog 列出只有这位发言者才有的按需工具，放在历史之后的尾部。
func (l *deferredToolLoader) speakerCatalog() string {
	return l.catalogWhere(false)
}

func (l *deferredToolLoader) catalogWhere(shared bool) string {
	var builder strings.Builder
	for _, name := range l.registry.Names() {
		if l.core[name] || l.isShared(name) != shared {
			continue
		}
		tool, ok := l.registry.Get(name)
		if !ok {
			continue
		}
		if l.featured[name] {
			builder.WriteString("- " + name + ": " + strings.Join(strings.Fields(tool.Description()), " ") + "\n")
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
		llm.ToolDefinition{Name: ToolsExecuteToolName, Description: "执行按需目录里的工具：name 填目录里的名字，input 须符合其 inputSchema；不清楚参数时先 tool_search。", Parameters: executeInputSchema()},
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

// loadedContracts 渲染已加载工具的完整契约。shared=true 只收公共工具（放在历史之前），
// shared=false 只收发言者专属的（放在尾部）。
func (l *deferredToolLoader) loadedContracts(shared bool) string {
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
		if l.isShared(name) != shared {
			continue
		}
		tool, ok := l.registry.Get(name)
		if !ok {
			continue
		}
		contracts = append(contracts, contract{Name: name, Description: tool.Description(), InputSchema: l.loaded[name]})
	}
	if len(contracts) == 0 {
		return ""
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
	return "取得「按需加载的工具」的完整定义，之后用 tools_execute 调用。names 填目录里的确切名字（优先）；拿不准叫什么时用 query 写关键词，返回最相关的几个。取过的在本会话后续保留；只取确实要用的。"
}

// toolSearchQueryLimit 是按关键词搜时最多返回几个工具。
const toolSearchQueryLimit = 4

func (l *deferredToolLoader) InputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"names": map[string]any{
				"type":        "array",
				"description": "工具名，取自按需加载目录，一字不差",
				"items":       map[string]any{"type": "string"},
				"minItems":    1,
				"maxItems":    8,
			},
			"query": map[string]any{
				"type":        "string",
				"description": "不知道确切名字时的关键词，如「禁言」「切换模型」",
			},
		},
	}
}

func (l *deferredToolLoader) Run(ctx context.Context, input map[string]any) (string, error) {
	if err := validateToolInput(l.InputSchema(), input); err != nil {
		return "", err
	}
	var requested []string
	if names, ok := input["names"]; ok {
		raw, _ := json.Marshal(names)
		if err := json.Unmarshal(raw, &requested); err != nil {
			return "", fmt.Errorf("names 必须是工具名数组")
		}
	}
	if len(requested) == 0 {
		query, _ := input["query"].(string)
		if strings.TrimSpace(query) == "" {
			return "", fmt.Errorf("names 和 query 至少填一个：知道名字填 names，不知道填 query 关键词")
		}
		requested = l.search(query)
		if len(requested) == 0 {
			return "", fmt.Errorf("没有找到和 %q 相关的工具；换个说法，或者对照按需加载目录直接填 names", query)
		}
	}
	type contract struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		InputSchema map[string]any `json:"inputSchema"`
	}
	contracts := []contract{}
	skills := []json.RawMessage{}
	snapshots := map[string]map[string]any{}
	for _, name := range requested {
		tool, ok := l.registry.Get(name)
		if !ok && l.isSkill(name) {
			// 技能和工具一样是按需加载的能力，只是正文不同。模型拿技能名来 tools_load
			// 时（线上见过 "bot-protocol"）直接把 SKILL.md 给它，不必再绕去 read_skill。
			body, err := (&SkillsReadTool{provider: l.registry.Skills}).Run(ctx, map[string]any{"name": name})
			if err != nil {
				return "", fmt.Errorf("技能 %q 读取失败：%w", name, err)
			}
			skills = append(skills, json.RawMessage(body))
			continue
		}
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
	payload := map[string]any{"loaded": contracts, "instruction": "使用 tools_execute，把目标工具名放入 name、参数对象放入 input；当前群会话后续运行会保留加载状态。"}
	if len(skills) > 0 {
		payload["skills"] = skills
		payload["skill_instruction"] = "skills 里是技能的完整 SKILL.md，按其中的说明去调用它提到的工具。"
	}
	result, err := json.Marshal(payload)
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
	return fmt.Errorf("工具 %q 不存在或已禁用；请对照目录重新填 tool_search 的 names，或改用 query 搜", name)
}

// missingToolError 是「根本没有这个工具」时的回话，多半是模型自己编了个名字（线上见过
// diana.interactive_browser_take_screenshot）。以前这种名字也被当成延迟工具，回一句
// 「请先 tools_load」，模型照做，再撞一次「不存在」，白烧两步。
func (l *deferredToolLoader) missingToolError(name string) error {
	if l.registry.PolicyDenied(name) {
		return l.registry.denialError(name)
	}
	if l.isSkill(name) {
		return fmt.Errorf("%q 是技能，不能直接执行：先 tool_search 它读到 SKILL.md，再按说明调用里面提到的工具", name)
	}
	return fmt.Errorf("工具 %q 不存在。工具名要和目录里的一字不差，不加 diana. 之类的前缀；常驻工具直接调用，目录里的按需工具先 tool_search 再 tools_execute", name)
}

// isSkill 判断这个名字是不是当前可见的技能。
func (l *deferredToolLoader) isSkill(name string) bool {
	if name == "" {
		return false
	}
	for _, skill := range l.registry.Skills() {
		if skill.Name == name {
			return true
		}
	}
	return false
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
		// 有的模型把 input 对象额外 JSON 编码了一次。只拆这一层封装，随后
		// 仍校验分发信封、工具权限和目标 schema；保留原调用供协议续接。
		if raw, ok := action.Input["input"].(string); ok && json.Valid([]byte(raw)) {
			var input map[string]any
			if err := decoderFromString(raw).Decode(&input); err == nil && input != nil {
				action.Input = cloneDeferredInput(action.Input).(map[string]any)
				action.Input["input"] = input
			}
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
		return l.resolveLoaded(action)
	}
	if l != nil {
		if action.Tool == LegacyToolsLoadName {
			action.Tool = ToolsLoadToolName
		}
		if action.Tool == ToolsLoadToolName {
			action.Input = coerceToolInputArrays(l.InputSchema(), action.Input)
			return action, validateToolInput(l.InputSchema(), action.Input)
		}
		if !l.core[action.Tool] {
			return l.dispatchDirect(action)
		}
	}
	return action, nil
}

// dispatchDirect 处理模型不套 tools_execute、直接按名字调用延迟工具的情况。
//
// 弱一些的模型（线上是 gemini-3.8-flash-low）看到提示词、工具调用记录和已加载契约里
// 都写着 platform、reminder 这类名字，就当成函数直接调；以前一律回「请先 tools_load，
// 再 tools_execute」，白白多走一到两轮模型调用。两天 21 次里 15 次工具其实已经加载过。
//
// 已加载就和 tools_execute 走同一套校验后执行；没加载就不执行——模型只看过目录里那一行，
// 没读过完整描述里的使用约束——而是当场替它加载，把契约连同报错一起交回去，下一步照着
// 重新调用即可。权限不在这里放宽：取不到的工具（身份白名单、机器人开关、没注册）照旧按
// 不存在或没权限回话，执行前 Runner 还会再过一遍注册表、确认码和操作开关。
func (l *deferredToolLoader) dispatchDirect(action llmAction) (llmAction, error) {
	name := action.Tool
	if _, exists := l.registry.Get(name); !exists {
		return action, l.missingToolError(name)
	}
	if _, loaded := l.loaded[name]; loaded {
		action.Input = cloneDeferredInput(action.Input).(map[string]any)
		return l.resolveLoaded(action)
	}
	if l.loadOnCall(name, action.Input) {
		action.Input = cloneDeferredInput(action.Input).(map[string]any)
		return l.resolveLoaded(action)
	}
	contract, err := l.Run(context.Background(), map[string]any{"names": []string{name}})
	if err != nil {
		return action, err
	}
	return action, fmt.Errorf("工具 %q 还没加载，这次没有执行；已经替你加载，完整契约如下，按 inputSchema 用 tools_execute 重新调用：%s", name, contract)
}

// resolveLoaded 校验一次已经拆开的延迟工具调用：本轮加载过、当前仍可用、入参同时符合
// 加载时和现在的 inputSchema。
func (l *deferredToolLoader) resolveLoaded(action llmAction) (llmAction, error) {
	name := action.Tool
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
	if !loaded && l.loadOnCall(name, action.Input) {
		schema, loaded = l.loaded[name], true
	}
	if !loaded {
		if _, exists := l.registry.Get(name); !exists {
			return action, l.missingToolError(name)
		}
		return action, fmt.Errorf("工具 %q 未在本轮加载，请先 tool_search，再 tools_execute", name)
	}
	action.Input = coerceToolInputArrays(schema, action.Input)
	input := action.Input
	tool, ok := l.registry.Get(name)
	if !ok {
		if l.registry.PolicyDenied(name) {
			return action, l.registry.denialError(name)
		}
		return action, fmt.Errorf("工具 %q 已移除或禁用，请重新 tool_search", name)
	}
	if err := validateToolInput(schema, input); err != nil {
		return action, err
	}
	current, err := snapshotToolSchema(tool)
	if err != nil {
		return action, fmt.Errorf("工具当前 inputSchema 无效，请重新 tool_search")
	}
	if err := validateToolInput(current, input); err != nil {
		return action, fmt.Errorf("当前工具契约校验失败，请重新 tool_search: %w", err)
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

// search 按关键词给非常驻工具打分：名字或描述里出现查询词（中文按相邻两字切）的次数，
// 名字命中加权。只在模型不知道确切名字时兜底，常规路径仍是对照目录填 names。
func (l *deferredToolLoader) search(query string) []string {
	terms := toolSearchTerms(query)
	if len(terms) == 0 {
		return nil
	}
	type hit struct {
		name  string
		score int
	}
	var hits []hit
	for _, name := range l.registry.Names() {
		if l.core[name] {
			continue
		}
		tool, ok := l.registry.Get(name)
		if !ok {
			continue
		}
		lowerName := strings.ToLower(name)
		description := strings.ToLower(tool.Description())
		score := 0
		for _, term := range terms {
			if strings.Contains(lowerName, term) {
				score += 3
			}
			if strings.Contains(description, term) {
				score++
			}
		}
		if score > 0 {
			hits = append(hits, hit{name, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	names := make([]string, 0, toolSearchQueryLimit)
	for _, h := range hits {
		if len(names) == toolSearchQueryLimit {
			break
		}
		names = append(names, h.name)
	}
	return names
}

// toolSearchTerms 把查询切成小写词：拉丁字母按词，连续汉字按相邻两字。
func toolSearchTerms(query string) []string {
	var terms []string
	seen := map[string]bool{}
	add := func(term string) {
		if term != "" && !seen[term] {
			seen[term] = true
			terms = append(terms, term)
		}
	}
	var latin []rune
	var han []rune
	flush := func() {
		if len(latin) > 1 {
			add(strings.ToLower(string(latin)))
		}
		latin = latin[:0]
		if len(han) == 1 {
			add(string(han))
		}
		for i := 0; i+1 < len(han); i++ {
			add(string(han[i : i+2]))
		}
		han = han[:0]
	}
	for _, r := range query {
		switch {
		case unicode.Is(unicode.Han, r):
			if len(latin) > 0 {
				flush()
			}
			han = append(han, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			if len(han) > 0 {
				flush()
			}
			latin = append(latin, r)
		default:
			flush()
		}
	}
	flush()
	return terms
}

// loadOnCall 免去单独取定义的一步：模型没先 tool_search 就直接点名调用按需工具时，
// 参数符合定义就当场登记为已加载、照常执行；不符合才交回完整契约让它重新调用。权限不在
// 这里放宽：取不到的工具（没权限、没注册）照旧按不存在或没权限回话。
func (l *deferredToolLoader) loadOnCall(name string, input map[string]any) bool {
	if l.core[name] {
		return false
	}
	tool, ok := l.registry.Get(name)
	if !ok {
		return false
	}
	schema, err := snapshotToolSchema(tool)
	if err != nil || validateToolInput(schema, input) != nil {
		return false
	}
	if _, exists := l.loaded[name]; !exists {
		l.order = append(l.order, name)
	}
	l.loaded[name] = schema
	if l.onLoad != nil {
		l.onLoad(append([]string(nil), l.order...))
	}
	return true
}
