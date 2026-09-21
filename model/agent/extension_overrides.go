package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 群成员权限和机器人启用开关放在同一份 profile 覆盖文件里，键名加前缀区分，
// 不改文件结构：`mcp:foo` 是这台机器人用不用，`members:mcp:foo` 是群成员能不能用。
const memberOverridePrefix = "members:"

// 常驻档位共用同一份文件，再加一个前缀：`resident:mcp:foo` 是这台机器人把这条服务
// 的工具常驻还是按需。键不在就是跟随默认，所以三档只需要「有没有这个键」加一个 bool。
const residentOverridePrefix = "resident:"

// MemberOverrideKey 返回某个扩展的群成员权限键。
func MemberOverrideKey(id string) string { return memberOverridePrefix + id }

// ResidentOverrideKey 返回某个扩展的常驻档位键。
func ResidentOverrideKey(id string) string { return residentOverridePrefix + id }

// ToolResidentID 是单个内置工具在档位表里的 ID。内置工具不属于任何扩展——它们直接挂
// 在运行时上，按插件分组反而分不出来，所以档位的单位就是工具本身。
func ToolResidentID(name string) string { return "tool:" + strings.TrimSpace(name) }

// SaveExtensionResidency 写入一个档位；resident 为 nil 表示退回默认档。
func SaveExtensionResidency(root, profile, id string, resident *bool) error {
	if resident == nil {
		return clearExtensionOverride(root, profile, ResidentOverrideKey(id))
	}
	return saveExtensionOverride(root, profile, ResidentOverrideKey(id), *resident)
}

// ResidentOverride 读出某个扩展的档位：nil 表示跟随默认。
func ResidentOverride(values map[string]bool, id string) *bool {
	resident, ok := values[ResidentOverrideKey(id)]
	if !ok {
		return nil
	}
	return &resident
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
	return filepath.Join(root, ".extension-overrides.json")
}
func loadExtensionOverrides(root string) (map[string]map[string]bool, error) {
	data, err := os.ReadFile(extensionOverridePath(root))
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
	return saveExtensionFile(extensionOverridePath(root), data)
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
	return saveExtensionFile(extensionOverridePath(root), data)
}

// Filter only this request view. Other robots retain their shared MCP sessions.
func (r *ToolRegistry) ApplyExtensionOverrides(values map[string]bool) {
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
		if resident := ResidentOverride(values, "skill:"+skill.Name); resident != nil {
			skill.Resident = *resident
		}
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
	r.mu.RUnlock()
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
		if enabled, ok := c.values[states[i].ID]; ok && !enabled {
			states[i].Enabled = false
			states[i].Tools = nil
		}
	}
	return states
}
