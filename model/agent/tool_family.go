// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// 工具族：把一组成员工具对模型呈现成一个带 action 的工具。
//
// 13 个 browser_* 在主人的按需目录里占 13 行，两周合计才调用一百多次。目录每多一行，
// 每条请求都要多带一行，模型还要在长得很像的名字里挑。方案和回放见
// docs/tool-catalog-consolidation.md。
//
// 工具族只是一层外观，成员工具照旧按自己的名字登记在注册表里：
//
//   - 权限不变。Retain、Remove、DenyTools、安全模式和配置开关仍按成员名裁剪；族的
//     action 只列这份注册表里实际取得到的成员，裁掉的动作自动从 enum 里消失。
//   - 执行不变。Runner 在分发时把「族名 + action」换回成员名和成员自己的参数（见
//     resolveFamilyCall），之后的超时、可重复调用、结果附件、确认、操作日志都落在
//     成员工具上，和合并前逐字相同。
//   - 目录变了。Catalog、Definitions、系统提示词目录和按需目录只出现族名。
//   - 旧名还认。模型照历史里的旧名字调用时仍然取得到成员工具。
//
// 族里一个成员都取不到时，族本身也不存在。

// ToolFamilyMember 是工具族里的一个动作。
type ToolFamilyMember struct {
	// Action 是 action 参数的取值。
	Action string
	// Tool 是成员工具登记在注册表里的名字。
	Tool string
	// Fixed 是这个动作替成员填好的参数。成员自己也有 action 这类子动作参数时，族把
	// 它们摊平成各自的动作（tabs_list、tabs_close……），模型只填族的 action。
	Fixed map[string]any
	// FixedOptional 为 true 时，成员的 schema 里没有 Fixed 的参数也算这个动作可用
	// （个人浏览器的标签页工具只会列出，没有 action 参数）。
	FixedOptional bool
	// Summary 不为空时代替成员自己的说明，用在一个成员摊成几个动作的时候。
	Summary string
}

// ToolFamily 声明一个工具族：对外的名字、目录里的第一句和各动作对应的成员。
type ToolFamily struct {
	Name        string
	Description string
	Members     []ToolFamilyMember
}

// toolFamilyActionField 是族工具区分动作的参数名。
const toolFamilyActionField = "action"

// RegisterFamily 登记一个工具族。同名的后登记覆盖先登记。
func (r *ToolRegistry) RegisterFamily(family ToolFamily) {
	if r == nil || strings.TrimSpace(family.Name) == "" || len(family.Members) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for index, existing := range r.families {
		if existing.Name == family.Name {
			r.families[index] = family
			return
		}
	}
	r.families = append(r.families, family)
}

// toolFamilies 返回这份注册表看得到的工具族：自己登记的在前，再沿父注册表补上。
func (r *ToolRegistry) toolFamilies() []ToolFamily {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	families := append([]ToolFamily(nil), r.families...)
	parent := r.parent
	r.mu.RUnlock()
	if parent == nil {
		return families
	}
	seen := make(map[string]bool, len(families))
	for _, family := range families {
		seen[family.Name] = true
	}
	for _, family := range parent.toolFamilies() {
		if !seen[family.Name] {
			families = append(families, family)
		}
	}
	return families
}

// familyByName 按族名取工具族。
func (r *ToolRegistry) familyByName(name string) (ToolFamily, bool) {
	for _, family := range r.toolFamilies() {
		if family.Name == name {
			return family, true
		}
	}
	return ToolFamily{}, false
}

// familyOfMember 找出某个成员工具属于哪个族、对应哪个动作。
func (r *ToolRegistry) familyOfMember(tool string) (ToolFamily, string, bool) {
	for _, family := range r.toolFamilies() {
		for _, member := range family.Members {
			if member.Tool == tool {
				return family, member.Action, true
			}
		}
	}
	return ToolFamily{}, "", false
}

// PresentedNames 是给模型看的工具名：族的成员收成一个族名，其余照 Names 的顺序。
// 权限、配置和统计仍用 Names，那里要的是成员名。
func (r *ToolRegistry) PresentedNames() []string {
	names := r.Names()
	families := r.toolFamilies()
	if len(families) == 0 {
		return names
	}
	memberOf := map[string]string{}
	for _, family := range families {
		for _, member := range family.Members {
			memberOf[member.Tool] = family.Name
		}
	}
	registered := make(map[string]bool, len(names))
	for _, name := range names {
		registered[name] = true
	}
	out := make([]string, 0, len(names))
	emitted := map[string]bool{}
	for _, name := range names {
		family, isMember := memberOf[name]
		// 真有一个工具就叫族名时不折叠，免得两个同名的东西互相顶掉。
		if !isMember || registered[family] {
			out = append(out, name)
			continue
		}
		if !emitted[family] {
			emitted[family] = true
			out = append(out, family)
		}
	}
	sort.Strings(out)
	return out
}

// familyTool 取族工具：按这份注册表里实际取得到的成员现拼出来。
func (r *ToolRegistry) familyTool(name string) (Tool, bool) {
	family, ok := r.familyByName(name)
	if !ok {
		return nil, false
	}
	tool := &toolFamilyTool{family: family}
	for _, member := range family.Members {
		target, ok := r.getRegistered(member.Tool)
		if !ok || !familyMemberAvailable(member, memberSchema(target)) {
			continue
		}
		tool.members = append(tool.members, toolFamilyResolved{action: member.Action, tool: target, fixed: member.Fixed, summary: member.Summary})
	}
	if len(tool.members) == 0 {
		return nil, false
	}
	return tool, true
}

// familyPolicyDenied 报告一个族是不是整族都因为没权限取不到。
func (r *ToolRegistry) familyPolicyDenied(name string) bool {
	family, ok := r.familyByName(name)
	if !ok {
		return false
	}
	denied := false
	for _, member := range family.Members {
		if _, ok := r.getRegistered(member.Tool); ok {
			return false
		}
		if r.PolicyDenied(member.Tool) {
			denied = true
		}
	}
	return denied
}

type toolFamilyResolved struct {
	action  string
	tool    Tool
	fixed   map[string]any
	summary string
}

// familyMemberAvailable 判断一个动作在这份成员工具上做不做得了：它替成员填的参数，
// 成员得认（有这个参数，带 enum 时取值也在里面）。
func familyMemberAvailable(member ToolFamilyMember, schema map[string]any) bool {
	fields, _ := schema["properties"].(map[string]any)
	for name, value := range member.Fixed {
		spec, ok := fields[name].(map[string]any)
		if !ok {
			if member.FixedOptional {
				continue
			}
			return false
		}
		if !schemaEnumAllows(spec["enum"], value) {
			return false
		}
	}
	return true
}

func schemaEnumAllows(enum any, value any) bool {
	switch values := enum.(type) {
	case nil:
		return true
	case []string:
		for _, item := range values {
			if item == value {
				return true
			}
		}
	case []any:
		for _, item := range values {
			if item == value {
				return true
			}
		}
	}
	return false
}

// toolFamilyTool 是族对外的那个工具。它自己也能执行（按 action 转给成员），但主回复
// 链路不靠它：Runner 在分发时已经换回成员工具，见 resolveFamilyCall。
type toolFamilyTool struct {
	family  ToolFamily
	members []toolFamilyResolved
}

func (t *toolFamilyTool) Name() string { return t.family.Name }

// CatalogDescription 是目录里那一行：族的定位，加上各动作和它的必填参数。
//
// 只写定位的话，模型要先 tool_search 取契约才敢调——回放里 12 条该用浏览器的请求有
// 6 条先多走了这一步，合并前看着 browser_open 这种成员名就能直接调。动作连同必填
// 参数列在目录里，简单的调用可以直接发；参数不对时分发层会把完整契约连报错交回去。
func (t *toolFamilyTool) CatalogDescription() string {
	signatures := make([]string, 0, len(t.members))
	for _, member := range t.members {
		signature := member.action
		var required []string
		for _, name := range requiredSchemaFields(memberSchema(member.tool)) {
			if _, fixed := member.fixed[name]; !fixed {
				required = append(required, name)
			}
		}
		if len(required) > 0 {
			signature += "(" + strings.Join(required, ",") + ")"
		}
		signatures = append(signatures, signature)
	}
	return strings.TrimSpace(t.family.Description) + " " + toolFamilyActionField + "：" + strings.Join(signatures, " / ")
}

// Description 第一句是族的定位（目录里只显示这一句），后面逐个列出可用动作和成员
// 自己的说明：加载完整契约时模型读到的使用约束和合并前是同一份文字。
func (t *toolFamilyTool) Description() string {
	var builder strings.Builder
	builder.WriteString(strings.TrimSpace(t.family.Description))
	builder.WriteString("\n" + toolFamilyActionField + " 可选：")
	for _, member := range t.members {
		builder.WriteString("\n- " + member.action)
		var required []string
		for _, name := range requiredSchemaFields(memberSchema(member.tool)) {
			if _, fixed := member.fixed[name]; !fixed {
				required = append(required, name)
			}
		}
		if len(required) > 0 {
			builder.WriteString("（必填 " + strings.Join(required, "、") + "）")
		}
		summary := member.summary
		if summary == "" {
			summary = member.tool.Description()
		}
		builder.WriteString("：" + strings.Join(strings.Fields(summary), " "))
	}
	return builder.String()
}

// InputSchema 把成员的参数并成一份，外加 action。各动作的必填项写在 Description 里，
// 调用时再按成员自己的 schema 校验。
func (t *toolFamilyTool) InputSchema() map[string]any {
	actions := make([]any, 0, len(t.members))
	properties := map[string]any{}
	// uses 记下每个参数在哪些动作里出现、各自怎么说明，按说明文字归并。
	type usage struct {
		description string
		actions     []string
	}
	uses := map[string][]*usage{}
	for _, member := range t.members {
		actions = append(actions, member.action)
		fields, _ := memberSchema(member.tool)["properties"].(map[string]any)
		for name, definition := range fields {
			if _, fixed := member.fixed[name]; fixed || name == toolFamilyActionField {
				continue
			}
			if _, exists := properties[name]; !exists {
				properties[name] = cloneDeferredInput(definition)
			}
			description := ""
			if spec, ok := definition.(map[string]any); ok {
				description, _ = spec["description"].(string)
			}
			description = strings.TrimSpace(description)
			merged := false
			for _, existing := range uses[name] {
				if existing.description == description {
					existing.actions = append(existing.actions, member.action)
					merged = true
					break
				}
			}
			if !merged {
				uses[name] = append(uses[name], &usage{description: description, actions: []string{member.action}})
			}
		}
	}
	// 同名参数在不同动作里说明不一样时，逐组写明是哪些动作的；说明都一样就不啰嗦。
	for name, groups := range uses {
		spec, ok := properties[name].(map[string]any)
		if !ok || len(groups) < 2 {
			continue
		}
		var lines []string
		for _, group := range groups {
			line := strings.Join(group.actions, "/")
			if group.description != "" {
				line += "：" + group.description
			}
			lines = append(lines, line)
		}
		spec["description"] = strings.Join(lines, "；")
	}
	properties[toolFamilyActionField] = map[string]any{
		"type":        "string",
		"enum":        actions,
		"description": "要做的动作，各动作的用法和必填参数见工具说明",
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{toolFamilyActionField},
		"properties":           properties,
	}
}

func (t *toolFamilyTool) Run(ctx context.Context, input map[string]any) (string, error) {
	member, memberInput, err := t.resolve(input)
	if err != nil {
		return "", err
	}
	return member.Run(ctx, memberInput)
}

// Introspection 和 ExplicitUserRequestKind 按动作转给成员：Runner 换回成员之前，
// 也可能有调用方拿族工具本身来问。
func (t *toolFamilyTool) Introspection(input map[string]any) bool {
	member, memberInput, err := t.resolve(input)
	if err != nil {
		return false
	}
	typed, ok := member.(IntrospectionTool)
	return ok && typed.Introspection(memberInput)
}

// resolve 按 action 找到成员，并把参数收成成员自己认的那几项。
func (t *toolFamilyTool) resolve(input map[string]any) (Tool, map[string]any, error) {
	action, _ := input[toolFamilyActionField].(string)
	action = strings.TrimSpace(action)
	available := make([]string, 0, len(t.members))
	for _, member := range t.members {
		available = append(available, member.action)
		if member.action != action {
			continue
		}
		memberInput := familyMemberInput(memberSchema(member.tool), input, member.fixed)
		if schema := memberSchema(member.tool); schema != nil {
			if err := validateToolInput(schema, memberInput); err != nil {
				return nil, nil, fmt.Errorf("%s 的 %s=%s 参数不对：%w", t.family.Name, toolFamilyActionField, action, err)
			}
		}
		return member.tool, memberInput, nil
	}
	if action == "" {
		return nil, nil, fmt.Errorf("%s 需要 %s，可选：%s", t.family.Name, toolFamilyActionField, strings.Join(available, "、"))
	}
	return nil, nil, fmt.Errorf("%s 没有 %s=%q 这个动作，当前可选：%s", t.family.Name, toolFamilyActionField, action, strings.Join(available, "、"))
}

func memberSchema(tool Tool) map[string]any {
	if typed, ok := tool.(ToolInputSchema); ok {
		return typed.InputSchema()
	}
	return nil
}

func requiredSchemaFields(schema map[string]any) []string {
	switch required := schema["required"].(type) {
	case []string:
		return append([]string(nil), required...)
	case []any:
		out := make([]string, 0, len(required))
		for _, item := range required {
			if name, ok := item.(string); ok {
				out = append(out, name)
			}
		}
		return out
	}
	return nil
}

// familyMemberInput 把族的参数收成成员自己认的那几项：去掉族的 action，只留成员 schema
// 里有的参数，再补上这个动作替成员填好的参数。合并后的 schema 列着所有动作的参数，
// 模型会顺手把用不上的填成 null，原样交给成员会被它自己的 additionalProperties 拒掉。
// 成员没有声明参数时，除 action 外原样传。
func familyMemberInput(schema map[string]any, input map[string]any, fixed map[string]any) map[string]any {
	fields, hasFields := schema["properties"].(map[string]any)
	out := make(map[string]any, len(input)+len(fixed))
	for name, value := range input {
		if name == toolFamilyActionField {
			continue
		}
		if hasFields {
			if _, known := fields[name]; !known || value == nil {
				continue
			}
		}
		out[name] = cloneDeferredInput(value)
	}
	for name, value := range fixed {
		if _, known := fields[name]; known {
			out[name] = value
		}
	}
	return out
}

// resolveFamilyCall 把对族的调用换回成员：返回成员工具名和成员自己的参数。name 不是
// 族名（或者注册表里真有同名工具）时原样返回。
func (r *ToolRegistry) resolveFamilyCall(name string, input map[string]any) (string, map[string]any, error) {
	if r == nil {
		return name, input, nil
	}
	if _, registered := r.getRegistered(name); registered {
		return name, input, nil
	}
	tool, ok := r.familyTool(name)
	if !ok {
		return name, input, nil
	}
	member, memberInput, err := tool.(*toolFamilyTool).resolve(input)
	if err != nil {
		return name, input, err
	}
	return member.Name(), memberInput, nil
}

// familyCallNamePattern 认「族名 + 动作」连写的工具名：`browser 的 open`、
// `browser（action=open）`、`browser.open`、`browser:open`。回放里模型会把说明文字
// 里的写法照抄进 tools_execute 的 name，动作本身是对的，只是名字不合法。
var familyCallNamePattern = regexp.MustCompile(`^([A-Za-z0-9_]+)\s*(?:的|[.:：/（(])\s*(?:action\s*[=:：]\s*)?["'「]?([A-Za-z0-9_]+)["'」]?\s*[）)]?$`)

// normalizeFamilyCall 把模型写出来的各种叫法统一成「族名 + action 参数」：
//
//   - 成员的旧名字（browser_open）换成族名，action 补进参数；
//   - 「族名 + 动作」连写的名字拆开。
//
// 认不出来就原样返回。只在名字本身取不到工具、或它是某个族的成员时才改写。
func (r *ToolRegistry) normalizeFamilyCall(name string, input map[string]any) (string, map[string]any) {
	if r == nil {
		return name, input
	}
	withAction := func(family, action string) (string, map[string]any) {
		out := make(map[string]any, len(input)+1)
		for key, value := range input {
			out[key] = value
		}
		if existing, _ := out[toolFamilyActionField].(string); strings.TrimSpace(existing) == "" {
			out[toolFamilyActionField] = action
		}
		return family, dropNullInputs(out)
	}
	if family, _, ok := r.familyOfMember(name); ok {
		if _, presented := r.familyTool(family.Name); !presented {
			return name, input
		}
		// 成员自己带子动作时（标签页的 list/close），按它填的子动作找到摊平后的那个动作，
		// 再把子动作参数拿掉；对不上任何一个就按成员原名照旧调用。
		for _, member := range family.Members {
			if member.Tool != name {
				continue
			}
			matched := true
			for key, value := range member.Fixed {
				if got, present := input[key]; present && got != value || !present && !member.FixedOptional {
					matched = false
				}
			}
			if !matched {
				continue
			}
			rest := make(map[string]any, len(input))
			for key, value := range input {
				if _, fixed := member.Fixed[key]; !fixed {
					rest[key] = value
				}
			}
			input = rest
			return withAction(family.Name, member.Action)
		}
		return name, input
	}
	if _, ok := r.getRegistered(name); ok {
		return name, input
	}
	if _, ok := r.familyByName(name); ok {
		return name, dropNullInputs(input)
	}
	if match := familyCallNamePattern.FindStringSubmatch(strings.TrimSpace(name)); match != nil {
		if family, ok := r.familyByName(match[1]); ok {
			for _, member := range family.Members {
				if member.Action == match[2] {
					return withAction(family.Name, member.Action)
				}
			}
		}
	}
	return name, input
}

// dropNullInputs 去掉值为 null 的参数。族的 schema 列着所有动作的参数，模型常把这次
// 用不上的填成 null；按 schema 它们不是合法的字符串或数字，留着会让整次调用被拒。
func dropNullInputs(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for name, value := range input {
		if value != nil {
			out[name] = value
		}
	}
	return out
}
