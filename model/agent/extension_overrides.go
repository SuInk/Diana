package agent

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
)

// 群成员权限和机器人启用开关放在同一份 profile 覆盖文件里，键名加前缀区分，
// 不改文件结构：`mcp:foo` 是这台机器人用不用，`members:mcp:foo` 是群成员能不能用。
const memberOverridePrefix = "members:"

// 常驻名单共用同一份文件，再加一个前缀：`resident:mcp:foo` 表示这条服务在名单里，
// 它的工具每轮都带完整定义。名单是一份清单，不是三档开关——`resident:` 的键只会是
// true，删掉键就是把它从名单里拿走。
const residentOverridePrefix = "resident:"

// residencyListKey 记录「这台机器人自己列过名单」。没有它就跟随内置推荐名单，有了
// 它就以清单为准——包括清单是空的那种情况，那是「一个工具都不常驻」，不是「没配过」。
// 它不带 resident: 前缀，免得被当成一个扩展 ID 读出来。
const residencyListKey = "residency:list"

// MemberOverrideKey 返回某个扩展的群成员权限键。
func MemberOverrideKey(id string) string { return memberOverridePrefix + id }

// ResidentOverrideKey 返回某个扩展的常驻档位键。
func ResidentOverrideKey(id string) string { return residentOverridePrefix + id }

// ToolResidentID 是单个内置工具在档位表里的 ID。内置工具不属于任何扩展——它们直接挂
// 在运行时上，按插件分组反而分不出来，所以档位的单位就是工具本身。
func ToolResidentID(name string) string { return toolResidentPrefix + strings.TrimSpace(name) }

// toolResidentPrefix 区分「单个工具」和「整条扩展」两级档位，档位解析靠它定先后。
const toolResidentPrefix = "tool:"

// SaveExtensionResidency 把一个 ID 加进名单或拿出去。整份名单由 SaveResidencyList
// 写，这里是给「就地加一个 / 删一个」用的：第一次这么写会把当前生效的推荐名单固定
// 下来，之后这台机器人就以自己的名单为准。
func SaveExtensionResidency(root, profile, id string, resident *bool, recommended []string) error {
	// Skill 不在名单里，它仍是三态：不带 resident 就是退回「看触发词」。
	if strings.HasPrefix(id, skillResidentPrefix) {
		if resident == nil {
			return clearExtensionOverride(root, profile, ResidentOverrideKey(id))
		}
		return saveExtensionOverride(root, profile, ResidentOverrideKey(id), *resident)
	}
	values, err := LoadExtensionOverrides(root, profile)
	if err != nil {
		return err
	}
	ids, listed := ResidencyList(values)
	if !listed {
		ids = append([]string(nil), recommended...)
	}
	next := make([]string, 0, len(ids)+1)
	for _, existing := range ids {
		if existing != id {
			next = append(next, existing)
		}
	}
	if resident != nil && *resident {
		next = append(next, id)
	}
	return SaveResidencyList(root, profile, next)
}

// ResidentOverride 读出某个 ID 有没有被写进名单：nil 表示没写过。
//
// 不写过 ≠ 不常驻：一个工具可能因为它所属的插件在名单里而常驻，也可能因为这台机器人
// 还没列过名单而跟着推荐走。那两件事各有各的出处（ResidencyListed、owners 展开），
// 这里只回答「这一条自己写过没有」——把「没写过」直接当成 false 是上一版的错，它让
// 整条插件进名单之后，它旗下的工具反而个个成了明确的「不常驻」。
func ResidentOverride(values map[string]bool, id string) *bool {
	resident, ok := values[ResidentOverrideKey(id)]
	if !ok {
		return nil
	}
	return &resident
}

// skillResidentPrefix 是 Skill 的 ID 前缀，它不受常驻名单管。
const skillResidentPrefix = "skill:"

// ResidencyListed 报告这台机器人有没有自己的常驻名单。
func ResidencyListed(values map[string]bool) bool { return values[residencyListKey] }

// SaveResidencyList 整份写下这台机器人的常驻名单。
//
// 一次写完，不是逐项写：名单本来就是一整件事，分成几次写中间会有「插件已经进去、
// 它里面被排除的那个还没拿掉」这种谁都不想要的中间态；而这份名单每变一次，所有会话
// 的工具列表就跟着变一次。ids 传 nil 表示退回推荐名单。
func SaveResidencyList(root, profile string, ids []string) error {
	lock := extensionPathLock(extensionOverridePath(root))
	lock.Lock()
	defer lock.Unlock()
	values, err := loadExtensionOverrides(root)
	if err != nil {
		return err
	}
	if values[profile] == nil {
		values[profile] = map[string]bool{}
	}
	for key := range values[profile] {
		// Skill 的档位不归名单管，重写名单时要原样留着。
		if strings.HasPrefix(key, residentOverridePrefix) && !strings.HasPrefix(key, residentOverridePrefix+skillResidentPrefix) {
			delete(values[profile], key)
		}
	}
	if ids == nil {
		delete(values[profile], residencyListKey)
	} else {
		values[profile][residencyListKey] = true
		for _, id := range ids {
			if id = strings.TrimSpace(id); id != "" {
				values[profile][ResidentOverrideKey(id)] = true
			}
		}
	}
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	return extensionOverridesState.save(root, data)
}

// RecommendedResidencyIDs 把内置推荐名单（工具名）换算成名单里的 ID。第一次就地
// 增删时要用它把当前生效的那份固定下来，否则「加一个」会顺带把推荐的全清空。
func RecommendedResidencyIDs(coreTools []string) []string {
	ids := make([]string, 0, len(coreTools))
	for _, name := range coreTools {
		if name = strings.TrimSpace(name); name != "" {
			ids = append(ids, ToolResidentID(name))
		}
	}
	return ids
}

// ResidencyList 读出这台机器人的名单；第二个返回值是「有没有列过」。
func ResidencyList(values map[string]bool) ([]string, bool) {
	if !ResidencyListed(values) {
		return nil, false
	}
	ids := []string{}
	for _, id := range ResidentOverrideIDs(values) {
		if !strings.HasPrefix(id, skillResidentPrefix) && values[ResidentOverrideKey(id)] {
			ids = append(ids, id)
		}
	}
	return ids, true
}

// ResidentOverrideIDs 列出名单里的 ID。目录是每轮对话现攒的，进程重启后就空了，
// 而名单还在文件里生效——界面得靠这份清单说清「列过的还在，只是暂时列不出细节」。
func ResidentOverrideIDs(values map[string]bool) []string {
	ids := []string{}
	for key := range values {
		if !strings.HasPrefix(key, residentOverridePrefix) {
			continue
		}
		if id := strings.TrimPrefix(key, residentOverridePrefix); id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// MemberAllowedExtensionIDs 列出这台机器人放开给群成员的扩展 ID。机器人级停用
// 优先：关掉的服务不会因为成员开关还开着就恢复。
func MemberAllowedExtensionIDs(values map[string]bool) []string {
	ids := []string{}
	for key, allowed := range values {
		if !allowed || !strings.HasPrefix(key, memberOverridePrefix) {
			continue
		}
		id := strings.TrimPrefix(key, memberOverridePrefix)
		if id == "" {
			continue
		}
		if enabled, ok := values[id]; ok && !enabled {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func extensionOverridePath(root string) string {
	return extensionOverridesState.path(root)
}
func loadExtensionOverrides(root string) (map[string]map[string]bool, error) {
	data, err := extensionOverridesState.read(root)
	if os.IsNotExist(err) {
		return map[string]map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	var values map[string]map[string]bool
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, err
	}
	if values == nil {
		values = map[string]map[string]bool{}
	}
	return values, nil
}
func LoadExtensionOverrides(root, profile string) (map[string]bool, error) {
	values, err := loadExtensionOverrides(root)
	if err != nil {
		return nil, err
	}
	return values[profile], nil
}

// clearExtensionOverride 删掉一个键，让它退回默认档；saveExtensionOverride 只能写
// true/false，表达不了「跟随默认」这一档。
func clearExtensionOverride(root, profile, id string) error {
	lock := extensionPathLock(extensionOverridePath(root))
	lock.Lock()
	defer lock.Unlock()
	values, err := loadExtensionOverrides(root)
	if err != nil {
		return err
	}
	if values[profile] == nil {
		return nil
	}
	if _, ok := values[profile][id]; !ok {
		return nil
	}
	delete(values[profile], id)
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	return extensionOverridesState.save(root, data)
}

func saveExtensionOverride(root, profile, id string, enabled bool) error {
	lock := extensionPathLock(extensionOverridePath(root))
	lock.Lock()
	defer lock.Unlock()
	values, err := loadExtensionOverrides(root)
	if err != nil {
		return err
	}
	if values[profile] == nil {
		values[profile] = map[string]bool{}
	}
	values[profile][id] = enabled
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	return extensionOverridesState.save(root, data)
}

// Filter only this request view. Other robots retain their shared MCP sessions.
func (r *ToolRegistry) ApplyExtensionOverrides(values map[string]bool) {
	// MCP 没写机器人设置的，跟随全局开关。全局关着的服务可能因为别的机器人单独
	// 打开而在跑，这台机器人没打开就得把它的工具摘掉——哪怕这台一条覆盖都没写过。
	values = cloneToolAllowlist(values)
	if values == nil {
		values = map[string]bool{}
	}
	for _, state := range r.Extensions() {
		if state.Kind != ExtensionKindMCP {
			continue
		}
		if _, ok := values[state.ID]; !ok && !state.Enabled {
			values[state.ID] = false
		}
	}
	if len(values) == 0 {
		return
	}
	r.mu.Lock()
	r.extensionOverrides = cloneToolAllowlist(values)
	r.mu.Unlock()
	for _, state := range r.Extensions() {
		if enabled, ok := values[state.ID]; ok && !enabled && state.Kind == ExtensionKindMCP {
			for _, name := range state.Tools {
				r.Remove(name)
			}
		}
	}
	skills := []SkillMetadata{}
	for _, skill := range r.Skills() {
		if enabled, ok := values["skill:"+skill.Name]; ok && !enabled {
			continue
		}
		skill.Resident = ResidentOverride(values, "skill:"+skill.Name)
		skills = append(skills, skill)
	}
	r.SetSkills(skills)
	tools := newLiveSkillTools(r.Skills)
	if _, ok := r.Get("read_skill"); ok {
		r.Register(tools.Read)
	}
	r.mu.Lock()
	previous := r.extensions
	if previous != nil {
		r.extensions = &filteredExtensionCatalog{previous: previous, values: values}
	}
	r.mu.Unlock()
	if _, ok := r.Get("list_capabilities"); ok {
		r.Register(NewExtensionsListTool(r.extensions, true))
	}
}

func (r *ToolRegistry) extensionToolAllowed(tool Tool) bool {
	mcp, ok := tool.(*MCPTool)
	if !ok {
		return true
	}
	r.mu.RLock()
	enabled, configured := r.extensionOverrides["mcp:"+mcp.serverName]
	mcpDisabled := r.disabled.mcpReason != ""
	r.mu.RUnlock()
	if mcpDisabled {
		return false
	}
	return !configured || enabled
}
func (r *ToolRegistry) filterExtensionToolNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if _, ok := r.Get(name); ok {
			out = append(out, name)
		}
	}
	return out
}

type filteredExtensionCatalog struct {
	previous ExtensionCatalog
	values   map[string]bool
}

func (c *filteredExtensionCatalog) Extensions() []ExtensionState {
	states := c.previous.Extensions()
	for i := range states {
		enabled, ok := c.values[states[i].ID]
		if !ok {
			continue
		}
		// 机器人单独打开的 MCP，全局关着也算开着。
		states[i].Enabled = enabled
		if !enabled {
			states[i].Tools = nil
		}
	}
	return states
}

// MCP 服务的全局开关是各台机器人的默认，不是禁令：`mcp:foo` 写了就以机器人为准，
// 没写就跟随全局。全局关着、某台机器人单独打开时，服务照样要起进程给它用。
//
// 以前全局关掉是硬停用，机器人开关救不回来，那时留在文件里的 `mcp:foo: true` 是
// 早就作废的旧值。直接换成新规则，这些旧值会让主人关掉的服务（比如能下单付款的点单
// 服务）在升级后悄悄跑起来。所以先按旧规则清一次：全局关着的服务，它的机器人 true
// 一律删掉，清完在文件里记一笔，之后写进来的 true 才算数。
// 标记放在空 profile 下：机器人 ID 不会是空串，这一格不会和真实配置撞。
const mcpBotDefaultMigrationKey = "migrated:mcp-bot-default"

// migrateLegacyMCPDisable 清掉旧规则下作废的机器人 true，只做一次。
func migrateLegacyMCPDisable(root string, servers map[string]mcpServerConfig) error {
	lock := extensionPathLock(extensionOverridePath(root))
	lock.Lock()
	defer lock.Unlock()
	values, err := loadExtensionOverrides(root)
	if err != nil {
		return err
	}
	if values[""][mcpBotDefaultMigrationKey] {
		return nil
	}
	for name, server := range servers {
		if server.enabled() {
			continue
		}
		id := "mcp:" + name
		for profile, overrides := range values {
			if overrides[id] {
				delete(values[profile], id)
			}
		}
	}
	if values[""] == nil {
		values[""] = map[string]bool{}
	}
	values[""][mcpBotDefaultMigrationKey] = true
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	return extensionOverridesState.save(root, data)
}

// mcpBotOptIns 列出至少有一台机器人单独打开的 MCP 服务 ID。全局关着的服务靠它决定
// 要不要起进程。
func mcpBotOptIns(root string) (map[string]bool, error) {
	values, err := loadExtensionOverrides(root)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for profile, overrides := range values {
		if profile == "" {
			continue
		}
		for key, enabled := range overrides {
			if enabled && strings.HasPrefix(key, "mcp:") {
				out[key] = true
			}
		}
	}
	return out, nil
}

// clearBotEnabledOverrides 删掉所有机器人对某个扩展的启用设置，让它们重新跟随全局。
// 只动启用键，群成员档位、常驻名单这些各管各的，不跟着清。
func clearBotEnabledOverrides(root, id string) error {
	lock := extensionPathLock(extensionOverridePath(root))
	lock.Lock()
	defer lock.Unlock()
	values, err := loadExtensionOverrides(root)
	if err != nil {
		return err
	}
	changed := false
	for profile, overrides := range values {
		if _, ok := overrides[id]; ok {
			delete(values[profile], id)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	return extensionOverridesState.save(root, data)
}
