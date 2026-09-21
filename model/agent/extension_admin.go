package agent

import (
	"context"
	"encoding/json"
	"errors"
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
	Audience ExtensionAudience `json:"audience,omitempty"`
	// Resident 只用于 residency 操作：true 常驻、false 按需、不带表示跟随默认档。
	Resident     *bool    `json:"resident,omitempty"`
	ClearHeaders []string `json:"clear_headers,omitempty"`
	ClearEnv     []string `json:"clear_env,omitempty"`
}

// extensionID 把「类型 + 名称」解析成目录里的扩展 ID，顺带确认它确实存在。
func (m *ExtensionManager) extensionID(kind, name string) (string, error) {
	for _, state := range m.Extensions() {
		if state.Kind == ExtensionKind(kind) && state.Name == name {
			return state.ID, nil
		}
	}
	return "", fmt.Errorf("扩展不存在")
}

// existingMCPServer 查这个名字当前有没有装过。预设那段要先知道这一点，才能决定
// 机密字段能不能留空。
func existingMCPServer(m *ExtensionManager, name string) (mcpServerConfig, bool) {
	server, ok := m.mcpConfigs[name]
	return server, ok
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
			if req.ProfileID != "" {
				states[i].Resident = ResidentOverride(overrides, states[i].ID)
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
		hidden, err := loadHiddenPresets(m.cfg.WorkDir)
		if err != nil {
			return nil, err
		}
		items := []map[string]any{}
		for _, preset := range MCPPresetList() {
			items = append(items, map[string]any{"preset": preset, "installed": installed[preset.Name], "hidden": hidden[preset.ID]})
		}
		return map[string]any{"items": items}, nil
	case "preset_hide", "preset_show":
		if req.Kind != "" && req.Kind != "mcp" {
			return nil, fmt.Errorf("不支持的扩展类型")
		}
		if _, ok := presetByID(req.Preset); !ok {
			return nil, fmt.Errorf("预设不存在")
		}
		// 删的是列表里那一行，服务本身没动：已经装上的那条 MCP 要删还是走 delete。
		return nil, saveHiddenPreset(m.cfg.WorkDir, req.Preset, req.Operation == "preset_hide")
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
	case "residency":
		// 三档只有一个可选的 bool：Resident 不带就是「跟随默认」，把键删掉。
		if req.ProfileID == "" {
			return nil, fmt.Errorf("请选择机器人后调整常驻档位")
		}
		id, err := m.extensionID(req.Kind, req.Name)
		if err != nil {
			return nil, err
		}
		return nil, SaveExtensionResidency(m.cfg.WorkDir, req.ProfileID, id, req.Resident)
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
	// preset_save 和 preset_verify 都先按预设把字段拼成配置；前者接着走普通保存，
	// 后者拼完只拿去验一次凭据，不落盘。
	presetID, presetTransport := "", ""
	if req.Operation == "preset_save" || req.Operation == "preset_verify" {
		// 编辑已经装好的那条时，令牌留空表示沿用旧的。
		_, installed := existingMCPServer(m, req.Name)
		config, err := mcpPresetConfig(req.Preset, req.Transport, req.Values, installed)
		if err != nil {
			return nil, err
		}
		presetID, presetTransport = req.Preset, req.Transport
		// 「服务可用」不在预设的字段表里，但那张表上有这个开关，原样带过去。
		if enabled, ok := req.Config["enabled"]; ok {
			config["enabled"] = enabled
		}
		// 拼好就当成一次普通保存：写入、校验、凭据保留全部沿用原来那段，
		// 预设没有自己的写入路径。
		if req.Operation == "preset_save" {
			req.Operation = "save"
		}
		req.Config = config
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
		result := map[string]any{"config": public, "configured_headers": sortedKeys(previous.Headers), "configured_env": sortedKeys(previous.Env)}
		// 从预设装出来的，界面还用那张表来改：把出身和非机密字段一起给回去，
		// 机密字段仍然只报「配过」，值不回显。
		if previous.Preset != "" {
			result["preset"], result["preset_transport"] = previous.Preset, previous.PresetTransport
			result["preset_values"] = presetValuesFromConfig(previous)
		}
		return result, nil
	}
	if req.Operation == "delete" {
		if !exists {
			return nil, fmt.Errorf("MCP 不存在")
		}
		delete(servers, req.Name)
		return nil, saveMCPServers(path, servers)
	}
	if req.Operation != "save" && req.Operation != "test" && req.Operation != "preset_verify" {
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
	if presetID != "" {
		server.Preset, server.PresetTransport = presetID, presetTransport
		// 预设那张表只问地址和令牌，超时和工具名单它根本没有输入框——不原样
		// 保留的话，从这张表改一次地址就会把这些设置悄悄清回默认。
		if exists {
			server.StartupTimeoutSec, server.ToolTimeoutSec = previous.StartupTimeoutSec, previous.ToolTimeoutSec
			server.EnabledTools, server.DisabledTools = previous.EnabledTools, previous.DisabledTools
		}
	} else if exists {
		// 有人绕过表单直接改了配置，出身仍然保留：界面下次打开还是那张表，
		// 填的也还是配置里当前的值。
		server.Preset, server.PresetTransport = previous.Preset, previous.PresetTransport
	}
	// 凭据只写不读，所以「值留空」只能理解成「保持原值」。但键整个不在提交里，
	// 那是人把那一行删掉了，就该真的删掉——通用表单里这些键本来就是自己填进去的。
	//
	// 预设表单是例外：它只提交自己那几个键，看不见也管不着别人额外注入的变量，
	// 按「不在提交里就删」处理会把它们连坐清掉，所以那条路径一律保留。
	keepAll := presetID != ""
	for key, value := range previous.Headers {
		if _, submitted := server.Headers[key]; !submitted && !keepAll {
			continue
		}
		if strings.TrimSpace(server.Headers[key]) == "" {
			if server.Headers == nil {
				server.Headers = map[string]string{}
			}
			server.Headers[key] = value
		}
	}
	for key, value := range previous.Env {
		if _, submitted := server.Env[key]; !submitted && !keepAll {
			continue
		}
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
	if req.Operation == "preset_verify" {
		account, supported, err := presetVerifyConfig(ctx, server)
		if !supported {
			return map[string]any{"verified": false, "supported": false, "message": "这种接法的凭据不经 Diana 的手，没法提前验，请用「测试连接」"}, nil
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"verified": true, "supported": true, "account": account}, nil
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
	// 预设装出来的服务在落盘前验一次凭据：令牌被明确拒绝就不保存，省得装上一条
	// 表面正常、一调用就 401 的服务。连不上只当警告——内网实例、先配置后联网都是
	// 常事，为此拦住保存反而更难用。
	result := map[string]any{"ok": true}
	if server.Preset != "" {
		account, supported, err := presetVerifyConfig(ctx, server)
		switch {
		case !supported:
		case errors.Is(err, ErrPresetCredentialRejected):
			return nil, err
		case err != nil:
			result["warning"] = fmt.Sprintf("已保存，但没能验证凭据：%v", err)
		default:
			// 验过了才说验过：有的接法根本没法验，不能让界面替它吹。
			result["verified"] = true
			if account != "" {
				result["account"] = account
			}
		}
	}
	servers[req.Name] = server
	if err := saveMCPServers(path, servers); err != nil {
		return nil, err
	}
	return result, nil
}
