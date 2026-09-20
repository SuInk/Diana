package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

func sortedKeys(values map[string]string) []string {
	keys := []string{}
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type ExtensionAdminRequest struct {
	Operation string         `json:"operation"`
	Kind      string         `json:"kind"`
	Name      string         `json:"name"`
	ProfileID string         `json:"profile_id,omitempty"`
	Enabled   bool           `json:"enabled"`
	Content   string         `json:"content,omitempty"`
	SourceURL string         `json:"source_url,omitempty"`
	Replace   bool           `json:"replace,omitempty"`
	Config    map[string]any `json:"config,omitempty"`
	// Preset/Transport/Values 只用于 preset_save：按内置模板拼出 Config，
	// 拼完之后和手填的 save 走同一条路。
	Preset    string            `json:"preset,omitempty"`
	Transport string            `json:"transport,omitempty"`
	Values    map[string]string `json:"values,omitempty"`
	// Audience 只用于 audience 操作：限定这个扩展开放给哪些人、哪些群。
	Audience     ExtensionAudience `json:"audience,omitempty"`
	ClearHeaders []string          `json:"clear_headers,omitempty"`
	ClearEnv     []string          `json:"clear_env,omitempty"`
}

func extensionAdminManager(cfg Config) (*ExtensionManager, error) {
	cfg = cfg.WithDefaults()
	m := &ExtensionManager{cfg: cfg, registry: NewToolRegistry(), mcpConfigs: map[string]mcpServerConfig{}, mcpRuntimes: map[string]*mcpServerRuntime{}, mcpErrors: map[string]string{}, mcpToolNames: map[string][]string{}}
	if err := m.reloadSkills(); err != nil {
		return nil, err
	}
	servers, err := loadMCPServers(resolveMCPConfigPath(cfg))
	if err != nil {
		return nil, err
	}
	m.mcpConfigs = servers
	return m, nil
}

// AdministerExtensions never starts MCP processes on a catalog read or save.
// A connection is opened only for the explicit test operation.
func AdministerExtensions(ctx context.Context, cfg Config, req ExtensionAdminRequest) (any, error) {
	m, err := extensionAdminManager(cfg)
	if err != nil {
		return nil, err
	}
	defer m.Close()
	switch req.Operation {
	case "list":
		states := m.Extensions()
		overrides, err := LoadExtensionOverrides(m.cfg.WorkDir, req.ProfileID)
		if err != nil {
			return nil, err
		}
		audiences, err := LoadExtensionAudiences(m.cfg.WorkDir, req.ProfileID)
		if err != nil {
			return nil, err
		}
		for i := range states {
			available := states[i].Enabled
			states[i].Available = &available
			if enabled, ok := overrides[states[i].ID]; ok {
				states[i].Enabled = states[i].Enabled && enabled
			}
			if states[i].Kind != ExtensionKindBuiltin && req.ProfileID != "" {
				members := overrides[MemberOverrideKey(states[i].ID)]
				states[i].MembersEnabled = &members
				if audience, ok := audiences[states[i].ID]; ok && !audience.Empty() {
					value := audience
					states[i].MemberAudience = &value
				}
			}
		}
		return map[string]any{"items": states}, nil
	case "presets":
		if req.Kind != "" && req.Kind != "mcp" {
			return nil, fmt.Errorf("不支持的扩展类型")
		}
		installed := map[string]bool{}
		for name := range m.mcpConfigs {
			installed[name] = true
		}
		items := []map[string]any{}
		for _, preset := range MCPPresetList() {
			items = append(items, map[string]any{"preset": preset, "installed": installed[preset.Name]})
		}
		return map[string]any{"items": items}, nil
	case "enabled":
		if req.ProfileID == "" {
			return nil, fmt.Errorf("请选择机器人后调整启用状态")
		}
		found := false
		for _, s := range m.Extensions() {
			if s.Kind == ExtensionKind(req.Kind) && s.Name == req.Name {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("扩展不存在")
		}
		return nil, saveExtensionOverride(m.cfg.WorkDir, req.ProfileID, req.Kind+":"+req.Name, req.Enabled)
	case "members":
		if req.ProfileID == "" {
			return nil, fmt.Errorf("请选择机器人后调整权限")
		}
		kind := ExtensionKind(req.Kind)
		if kind != ExtensionKindMCP && kind != ExtensionKindSkill {
			return nil, fmt.Errorf("只有 MCP 服务和 Skill 支持群成员权限")
		}
		found := false
		for _, s := range m.Extensions() {
			if s.Kind == kind && s.Name == req.Name {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("扩展不存在")
		}
		return nil, saveExtensionOverride(m.cfg.WorkDir, req.ProfileID, MemberOverrideKey(req.Kind+":"+req.Name), req.Enabled)
	case "audience":
		if req.ProfileID == "" {
			return nil, fmt.Errorf("请选择机器人后调整权限")
		}
		kind := ExtensionKind(req.Kind)
		if kind != ExtensionKindMCP && kind != ExtensionKindSkill {
			return nil, fmt.Errorf("只有 MCP 服务和 Skill 支持群成员权限")
		}
		found := false
		for _, s := range m.Extensions() {
			if s.Kind == kind && s.Name == req.Name {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("扩展不存在")
		}
		return nil, saveExtensionAudience(m.cfg.WorkDir, req.ProfileID, req.Kind+":"+req.Name, req.Audience)
	}
	if req.Kind == "skill" {
		switch req.Operation {
		case "read":
			for _, skill := range m.Skills() {
				if skill.Name == req.Name {
					content := skill.Content
					if content == "" {
						data, err := os.ReadFile(skill.Path)
						if err != nil {
							return nil, err
						}
						if len(data) > maxSkillFileBytes {
							return nil, fmt.Errorf("Skill 文件过大")
						}
						content = string(data)
					}
					return map[string]any{"content": content, "managed": skill.Managed}, nil
				}
			}
			return nil, fmt.Errorf("Skill 不存在")
		case "save":
			return m.installSkill(ctx, skillInstallRequest{Name: req.Name, Content: req.Content, SourceURL: req.SourceURL, Replace: req.Replace})
		case "delete":
			_, err := m.uninstallSkill(req.Name)
			return nil, err
		}
	}
	if req.Kind != "mcp" {
		return nil, fmt.Errorf("不支持的扩展类型")
	}
	if req.Operation == "preset_save" {
		config, err := mcpPresetConfig(req.Preset, req.Transport, req.Values)
		if err != nil {
			return nil, err
		}
		// 拼好就当成一次普通保存：写入、校验、凭据保留全部沿用原来那段，
		// 预设没有自己的写入路径。
		req.Operation, req.Config = "save", config
	}
	if !mcpServerNamePattern.MatchString(req.Name) {
		return nil, fmt.Errorf("无效 MCP 名称")
	}
	path := resolveMCPConfigPath(m.cfg)
	lock := extensionPathLock(path)
	lock.Lock()
	defer lock.Unlock()
	servers, err := loadMCPServers(path)
	if err != nil {
		return nil, err
	}
	previous, exists := servers[req.Name]
	if req.Operation == "read" {
		if !exists {
			return nil, fmt.Errorf("MCP 不存在")
		}
		data, _ := json.Marshal(previous)
		var public map[string]any
		_ = json.Unmarshal(data, &public)
		headers, env := map[string]string{}, map[string]string{}
		for key := range previous.Headers {
			headers[key] = ""
		}
		for key := range previous.Env {
			env[key] = ""
		}
		public["headers"], public["env"] = headers, env
		return map[string]any{"config": public, "configured_headers": sortedKeys(previous.Headers), "configured_env": sortedKeys(previous.Env)}, nil
	}
	if req.Operation == "delete" {
		if !exists {
			return nil, fmt.Errorf("MCP 不存在")
		}
		delete(servers, req.Name)
		return nil, saveMCPServers(path, servers)
	}
	if req.Operation != "save" && req.Operation != "test" {
		return nil, fmt.Errorf("不支持的操作")
	}
	if req.Operation == "save" && exists && !req.Replace {
		return nil, fmt.Errorf("同名 MCP 已存在")
	}
	server, err := mcpServerConfigFromInput(req.Config)
	if err != nil {
		return nil, err
	}
	if exists {
		server.InheritEnv = previous.InheritEnv
		server.Required = previous.Required
	}
	for key, value := range previous.Headers {
		if strings.TrimSpace(server.Headers[key]) == "" {
			if server.Headers == nil {
				server.Headers = map[string]string{}
			}
			server.Headers[key] = value
		}
	}
	for key, value := range previous.Env {
		if strings.TrimSpace(server.Env[key]) == "" {
			if server.Env == nil {
				server.Env = map[string]string{}
			}
			server.Env[key] = value
		}
	}
	for _, key := range req.ClearHeaders {
		delete(server.Headers, key)
	}
	for _, key := range req.ClearEnv {
		delete(server.Env, key)
	}
	if err := server.validate(); err != nil {
		return nil, err
	}
	if err := validateMCPConfigValues(server); err != nil {
		return nil, err
	}
	if req.Operation == "test" {
		runtime, err := startMCPServerRuntime(ctx, req.Name, server, m.cfg, map[string]bool{})
		if err != nil {
			return nil, fmt.Errorf("MCP 连接或工具发现失败，请检查地址、命令及凭据")
		}
		defer runtime.Close()
		names := []string{}
		for _, tool := range runtime.tools {
			names = append(names, tool.Name())
		}
		return map[string]any{"connected": true, "tools": names}, nil
	}
	servers[req.Name] = server
	return nil, saveMCPServers(path, servers)
}
