package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
)

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
		if enabled, ok := values["skill:"+skill.Name]; !ok || enabled {
			skills = append(skills, skill)
		}
	}
	r.SetSkills(skills)
	tools := newLiveSkillTools(r.Skills)
	if _, ok := r.Get("skills.list"); ok {
		r.Register(tools.List)
	}
	if _, ok := r.Get("skills.read"); ok {
		r.Register(tools.Read)
	}
	r.mu.Lock()
	previous := r.extensions
	if previous != nil {
		r.extensions = &filteredExtensionCatalog{previous: previous, values: values}
	}
	r.mu.Unlock()
	if _, ok := r.Get("extensions.list"); ok {
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
