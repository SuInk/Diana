// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

type ExtensionKind string

const (
	ExtensionKindBuiltin ExtensionKind = "builtin"
	ExtensionKindSkill   ExtensionKind = "skill"
	ExtensionKindMCP     ExtensionKind = "mcp"
)

// BuiltinExtension describes one existing Diana plugin in the unified Agent
// capability catalog without importing the assistant package back into agent.
type BuiltinExtension struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version,omitempty"`
	Description string   `json:"description,omitempty"`
	Official    bool     `json:"official"`
	BuiltIn     bool     `json:"built_in"`
	Installed   bool     `json:"installed"`
	Enabled     bool     `json:"enabled"`
	Permissions []string `json:"permissions,omitempty"`
}

// ExtensionState is the redacted common view of built-in plugins, local
// SKILL.md packages, and configured MCP servers.
type ExtensionState struct {
	Kind        ExtensionKind `json:"kind"`
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Version     string        `json:"version,omitempty"`
	Description string        `json:"description,omitempty"`
	Official    bool          `json:"official,omitempty"`
	BuiltIn     bool          `json:"built_in,omitempty"`
	Managed     bool          `json:"managed,omitempty"`
	Installed   bool          `json:"installed"`
	Enabled     bool          `json:"enabled"`
	Source      string        `json:"source,omitempty"`
	Transport   string        `json:"transport,omitempty"`
	Tools       []string      `json:"tools,omitempty"`
	Permissions []string      `json:"permissions,omitempty"`
	Error       string        `json:"error,omitempty"`
}

type ExtensionCatalog interface {
	Extensions() []ExtensionState
}

// ExtensionManager owns the live skills catalog and MCP sessions for a shared
// runtime registry. Request-scoped views inherit these capabilities without
// restarting MCP processes or taking ownership of their lifecycle.
type ExtensionManager struct {
	mu sync.RWMutex

	cfg      Config
	registry *ToolRegistry
	builtin  []BuiltinExtension
	skills   []SkillMetadata

	mcpConfigs   map[string]mcpServerConfig
	mcpRuntimes  map[string]*mcpServerRuntime
	mcpErrors    map[string]string
	mcpToolNames map[string][]string

	warnings []string
	closed   bool
}

func NewExtensionManager(ctx context.Context, cfg Config, registry *ToolRegistry) (*ExtensionManager, error) {
	cfg = cfg.WithDefaults()
	manager := &ExtensionManager{
		cfg:          cfg,
		registry:     registry,
		builtin:      append([]BuiltinExtension(nil), cfg.BuiltinExtensions...),
		mcpConfigs:   map[string]mcpServerConfig{},
		mcpRuntimes:  map[string]*mcpServerRuntime{},
		mcpErrors:    map[string]string{},
		mcpToolNames: map[string][]string{},
	}
	if err := manager.reloadSkills(); err != nil {
		manager.addWarning("skills: " + err.Error())
	}

	skillTools := newLiveSkillTools(manager.Skills)
	registry.Register(skillTools.List)
	registry.Register(skillTools.Read)
	registry.Register(NewExtensionsListTool(manager, cfg.ExtensionManagement))
	if cfg.ExtensionManagement {
		registry.Register(&SkillsInstallTool{manager: manager})
		registry.Register(&SkillsUninstallTool{manager: manager})
		registry.Register(&MCPInstallTool{manager: manager})
		registry.Register(&MCPSetEnabledTool{manager: manager})
		registry.Register(&MCPUninstallTool{manager: manager})
	}
	if err := manager.loadMCP(ctx); err != nil {
		_ = manager.Close()
		return nil, err
	}
	return manager, nil
}

func (m *ExtensionManager) Extensions() []ExtensionState {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	builtin := append([]BuiltinExtension(nil), m.builtin...)
	skills := append([]SkillMetadata(nil), m.skills...)
	mcpCount := len(m.mcpConfigs)
	m.mu.RUnlock()

	states := make([]ExtensionState, 0, len(builtin)+len(skills)+mcpCount)
	for _, item := range builtin {
		states = append(states, ExtensionState{
			Kind:        ExtensionKindBuiltin,
			ID:          item.ID,
			Name:        item.Name,
			Version:     item.Version,
			Description: item.Description,
			Official:    item.Official,
			BuiltIn:     item.BuiltIn,
			Installed:   item.Installed,
			Enabled:     item.Enabled,
			Permissions: append([]string(nil), item.Permissions...),
		})
	}
	for _, skill := range skills {
		states = append(states, ExtensionState{
			Kind:        ExtensionKindSkill,
			ID:          "skill:" + skill.Name,
			Name:        skill.Name,
			Description: skill.Description,
			Managed:     skill.Managed,
			Installed:   true,
			Enabled:     true,
			Source:      skill.Source,
		})
	}
	states = append(states, m.mcpExtensionStates()...)
	sort.Slice(states, func(i, j int) bool {
		if states[i].Kind != states[j].Kind {
			return states[i].Kind < states[j].Kind
		}
		return states[i].ID < states[j].ID
	})
	return states
}

func (m *ExtensionManager) Skills() []SkillMetadata {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]SkillMetadata(nil), m.skills...)
}

func (m *ExtensionManager) reloadSkills() error {
	localSkills, err := LoadSkills(m.cfg.SkillRoots)
	skills := mergeBuiltinSkills(m.cfg.BuiltinSkills, localSkills, m.cfg.ReservedSkillNames)
	m.mu.Lock()
	m.skills = append([]SkillMetadata(nil), skills...)
	m.mu.Unlock()
	if m.registry != nil {
		m.registry.SetSkills(skills)
		tools := newLiveSkillTools(m.Skills)
		if _, ok := m.registry.Get("skills.list"); !ok {
			m.registry.Register(tools.List)
		}
		if _, ok := m.registry.Get("skills.read"); !ok {
			m.registry.Register(tools.Read)
		}
	}
	return err
}

func normalizeBuiltinSkills(values []SkillMetadata) []SkillMetadata {
	seen := map[string]bool{}
	out := make([]SkillMetadata, 0, len(values))
	for _, value := range values {
		value.Name = strings.TrimSpace(value.Name)
		value.Description = strings.TrimSpace(value.Description)
		value.ShortDescription = strings.TrimSpace(value.ShortDescription)
		value.Path = strings.TrimSpace(value.Path)
		value.Source = strings.TrimSpace(value.Source)
		value.Content = strings.TrimSpace(value.Content)
		if value.Name == "" || value.Description == "" || value.Path == "" || value.Content == "" || seen[value.Name] {
			continue
		}
		seen[value.Name] = true
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func mergeBuiltinSkills(builtin, local []SkillMetadata, reservedNames []string) []SkillMetadata {
	out := append([]SkillMetadata(nil), normalizeBuiltinSkills(builtin)...)
	seen := make(map[string]bool, len(out)+len(reservedNames))
	for _, name := range reservedNames {
		seen[strings.TrimSpace(name)] = true
	}
	for _, skill := range out {
		seen[skill.Name] = true
	}
	for _, skill := range local {
		if !seen[skill.Name] {
			seen[skill.Name] = true
			out = append(out, skill)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].Path < out[j].Path
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (m *ExtensionManager) addWarning(message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	m.mu.Lock()
	m.warnings = append(m.warnings, message)
	m.mu.Unlock()
}

func (m *ExtensionManager) Warnings() []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]string(nil), m.warnings...)
}

func (m *ExtensionManager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	runtimes := make([]*mcpServerRuntime, 0, len(m.mcpRuntimes))
	for _, runtime := range m.mcpRuntimes {
		runtimes = append(runtimes, runtime)
	}
	m.mcpRuntimes = map[string]*mcpServerRuntime{}
	m.mu.Unlock()
	var firstErr error
	for _, runtime := range runtimes {
		if err := runtime.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

type ExtensionsListTool struct {
	catalog           ExtensionCatalog
	managementEnabled bool
}

func NewExtensionsListTool(catalog ExtensionCatalog, managementEnabled bool) *ExtensionsListTool {
	return &ExtensionsListTool{catalog: catalog, managementEnabled: managementEnabled}
}

func (t *ExtensionsListTool) Name() string { return "extensions.list" }

func (t *ExtensionsListTool) Description() string {
	return `列出 Diana 的统一能力目录，包括默认内置插件、本地 Skills、MCP 服务、启用状态和 MCP 工具名。input: {}`
}

func (t *ExtensionsListTool) Run(context.Context, map[string]any) (string, error) {
	var extensions []ExtensionState
	if t != nil && t.catalog != nil {
		extensions = t.catalog.Extensions()
	}
	payload := map[string]any{
		"extensions":         extensions,
		"management_enabled": t != nil && t.managementEnabled,
	}
	if t != nil {
		if manager, ok := t.catalog.(*ExtensionManager); ok {
			if warnings := manager.Warnings(); len(warnings) > 0 {
				payload["warnings"] = warnings
			}
		}
	}
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func RenderExtensionsPrompt(states []ExtensionState) string {
	if len(states) == 0 {
		return ""
	}
	var builtins, mcps []string
	for _, state := range states {
		status := "disabled"
		if state.Enabled {
			status = "enabled"
		}
		switch state.Kind {
		case ExtensionKindBuiltin:
			builtins = append(builtins, state.Name+" ("+status+")")
		case ExtensionKindMCP:
			mcps = append(mcps, state.Name+" ("+status+")")
		}
	}
	var lines []string
	lines = append(lines, "## Capability Extensions", "Diana uses one capability catalog for built-in plugins, Skills, and MCP services.")
	if len(builtins) > 0 {
		lines = append(lines, "Default built-in plugins: "+strings.Join(builtins, ", ")+".")
	}
	if len(mcps) > 0 {
		lines = append(lines, "Configured MCP services: "+strings.Join(mcps, ", ")+".")
	}
	lines = append(lines, "Call `extensions.list` when you need the complete current catalog and MCP tool names.")
	return strings.Join(lines, "\n")
}

func normalizeBuiltinExtensions(values []BuiltinExtension) []BuiltinExtension {
	seen := map[string]bool{}
	out := make([]BuiltinExtension, 0, len(values))
	for _, value := range values {
		value.ID = strings.TrimSpace(value.ID)
		value.Name = strings.TrimSpace(value.Name)
		if value.ID == "" || value.Name == "" || seen[value.ID] {
			continue
		}
		seen[value.ID] = true
		value.Version = strings.TrimSpace(value.Version)
		value.Description = strings.TrimSpace(value.Description)
		value.Permissions = cleanStringList(value.Permissions)
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
