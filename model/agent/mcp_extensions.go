// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/net/http/httpguts"
)

var (
	mcpServerNamePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	environmentKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type MCPInstallTool struct {
	manager *ExtensionManager
}

func (t *MCPInstallTool) Name() string { return "mcp.install" }

func (t *MCPInstallTool) Description() string {
	return `安装并连接一个 MCP 服务，持久化后立即注册其工具。stdio 使用 command/args，远程服务使用 url，二者必须二选一；headers/env 可引用 ${ENV_VAR}。首次调用会被拒绝并返回确认码，请把要装的服务讲清楚、等用户原样回复确认码后再重发本次调用。input: {"name":"服务名","command":"npx，可选","args":["-y","package"],"env":{"KEY":"value"},"cwd":"可选","url":"https://host/mcp，可选","headers":{"Authorization":"Bearer ${TOKEN}"},"enabled":true,"startup_timeout_sec":10,"tool_timeout_sec":60,"enabled_tools":[],"disabled_tools":[],"replace":false}`
}

func (t *MCPInstallTool) ExplicitUserRequestKind() string { return "mcp" }

func (t *MCPInstallTool) Run(ctx context.Context, input map[string]any) (string, error) {
	name := stringFromInput(input, "name")
	server, err := mcpServerConfigFromInput(input)
	if err != nil {
		return "", err
	}
	state, err := t.manager.installMCP(ctx, name, server, boolFromInput(input, "replace", false))
	if err != nil {
		return "", err
	}
	body, err := json.MarshalIndent(map[string]any{
		"installed": state,
		"message":   "MCP 服务已持久化并在当前 Agent 会话中生效；可直接调用 tools 中列出的工具。",
	}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

type MCPSetEnabledTool struct {
	manager *ExtensionManager
}

func (t *MCPSetEnabledTool) Name() string { return "mcp.set_enabled" }

func (t *MCPSetEnabledTool) Description() string {
	return `启用或停用一个已配置的 MCP 服务并立即刷新工具。首次调用会被拒绝并返回确认码，等用户原样回复后再重发本次调用。input: {"name":"服务名","enabled":true}`
}

func (t *MCPSetEnabledTool) ExplicitUserRequestKind() string { return "mcp" }

func (t *MCPSetEnabledTool) Run(ctx context.Context, input map[string]any) (string, error) {
	name := stringFromInput(input, "name")
	if name == "" {
		return "", errors.New("name is required")
	}
	state, err := t.manager.setMCPEnabled(ctx, name, boolFromInput(input, "enabled", true))
	if err != nil {
		return "", err
	}
	body, err := json.MarshalIndent(map[string]any{"updated": state}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

type MCPUninstallTool struct {
	manager *ExtensionManager
}

func (t *MCPUninstallTool) Name() string { return "mcp.uninstall" }

func (t *MCPUninstallTool) Description() string {
	return `卸载一个 MCP 服务：停止当前连接、移除工具并从 MCP 配置中删除。首次调用会被拒绝并返回确认码，等用户原样回复后再重发本次调用。input: {"name":"服务名"}`
}

func (t *MCPUninstallTool) ExplicitUserRequestKind() string { return "mcp" }

func (t *MCPUninstallTool) Run(_ context.Context, input map[string]any) (string, error) {
	name := stringFromInput(input, "name")
	if name == "" {
		return "", errors.New("name is required")
	}
	if err := t.manager.uninstallMCP(name); err != nil {
		return "", err
	}
	body, err := json.MarshalIndent(map[string]any{
		"uninstalled": name,
		"message":     "MCP 服务已停止，工具已移除，配置项已删除。",
	}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (m *ExtensionManager) loadMCP(ctx context.Context) error {
	path := resolveMCPConfigPath(m.cfg)
	servers, err := loadMCPServers(path)
	if err != nil {
		return fmt.Errorf("load MCP config: %w", err)
	}
	m.mu.Lock()
	m.mcpConfigs = cloneMCPConfigs(servers)
	m.mu.Unlock()
	usedNames := m.usedToolNames(nil)
	started := make([]*mcpServerRuntime, 0, len(servers))
	for _, name := range sortedMCPServerNames(servers) {
		server := servers[name]
		if validateErr := server.validate(); validateErr != nil {
			message := validateErr.Error()
			m.mu.Lock()
			m.mcpErrors[name] = message
			m.mu.Unlock()
			if server.Required {
				closeMCPRuntimes(started)
				return fmt.Errorf("mcp server %q: %w", name, validateErr)
			}
			continue
		}
		if !server.enabled() {
			continue
		}
		runtime, startErr := startMCPServerRuntime(ctx, name, server, m.cfg, usedNames)
		if startErr != nil {
			m.mu.Lock()
			m.mcpErrors[name] = startErr.Error()
			m.mu.Unlock()
			if server.Required {
				closeMCPRuntimes(started)
				return startErr
			}
			continue
		}
		started = append(started, runtime)
		m.attachMCPRuntime(name, runtime)
	}
	return nil
}

func (m *ExtensionManager) installMCP(ctx context.Context, name string, server mcpServerConfig, replace bool) (ExtensionState, error) {
	name = strings.TrimSpace(name)
	if !mcpServerNamePattern.MatchString(name) {
		return ExtensionState{}, errors.New("MCP server name must use only letters, numbers, dot, underscore, or hyphen")
	}
	server = normalizeMCPServerConfig(server)
	if err := server.validate(); err != nil {
		return ExtensionState{}, err
	}
	if err := validateMCPConfigValues(server); err != nil {
		return ExtensionState{}, err
	}

	path := resolveMCPConfigPath(m.cfg)
	lock := extensionPathLock(path)
	lock.Lock()
	defer lock.Unlock()
	servers, err := loadMCPServers(path)
	if err != nil {
		return ExtensionState{}, err
	}
	if _, exists := servers[name]; exists && !replace {
		return ExtensionState{}, fmt.Errorf("MCP server %q already exists; set replace=true only when the user requested replacement", name)
	}

	var runtime *mcpServerRuntime
	if server.enabled() {
		usedNames := m.usedToolNames(m.currentMCPToolNames(name))
		runtime, err = startMCPServerRuntime(ctx, name, server, m.cfg, usedNames)
		if err != nil {
			return ExtensionState{}, err
		}
	}
	servers[name] = cloneMCPServerConfig(server)
	if err := saveMCPServers(path, servers); err != nil {
		if runtime != nil {
			_ = runtime.Close()
		}
		return ExtensionState{}, err
	}
	m.replaceMCPRuntime(name, runtime, servers)
	state, _ := m.mcpState(name)
	return state, nil
}

func (m *ExtensionManager) setMCPEnabled(ctx context.Context, name string, enabled bool) (ExtensionState, error) {
	name = strings.TrimSpace(name)
	path := resolveMCPConfigPath(m.cfg)
	lock := extensionPathLock(path)
	lock.Lock()
	defer lock.Unlock()
	servers, err := loadMCPServers(path)
	if err != nil {
		return ExtensionState{}, err
	}
	server, exists := servers[name]
	if !exists {
		return ExtensionState{}, fmt.Errorf("MCP server %q not found", name)
	}
	value := enabled
	server.Enabled = &value
	server = normalizeMCPServerConfig(server)
	var runtime *mcpServerRuntime
	if enabled {
		usedNames := m.usedToolNames(m.currentMCPToolNames(name))
		runtime, err = startMCPServerRuntime(ctx, name, server, m.cfg, usedNames)
		if err != nil {
			return ExtensionState{}, err
		}
	}
	servers[name] = server
	if err := saveMCPServers(path, servers); err != nil {
		if runtime != nil {
			_ = runtime.Close()
		}
		return ExtensionState{}, err
	}
	m.replaceMCPRuntime(name, runtime, servers)
	state, _ := m.mcpState(name)
	return state, nil
}

func (m *ExtensionManager) uninstallMCP(name string) error {
	name = strings.TrimSpace(name)
	path := resolveMCPConfigPath(m.cfg)
	lock := extensionPathLock(path)
	lock.Lock()
	defer lock.Unlock()
	servers, err := loadMCPServers(path)
	if err != nil {
		return err
	}
	if _, exists := servers[name]; !exists {
		return fmt.Errorf("MCP server %q not found", name)
	}
	delete(servers, name)
	if err := saveMCPServers(path, servers); err != nil {
		return err
	}
	m.replaceMCPRuntime(name, nil, servers)
	return nil
}

func (m *ExtensionManager) attachMCPRuntime(name string, runtime *mcpServerRuntime) {
	if runtime == nil {
		return
	}
	toolNames := make([]string, 0, len(runtime.tools))
	for _, tool := range runtime.tools {
		m.registry.Register(tool)
		toolNames = append(toolNames, tool.Name())
	}
	m.mu.Lock()
	m.mcpRuntimes[name] = runtime
	m.mcpToolNames[name] = toolNames
	delete(m.mcpErrors, name)
	m.mu.Unlock()
}

func (m *ExtensionManager) replaceMCPRuntime(name string, runtime *mcpServerRuntime, configs map[string]mcpServerConfig) {
	m.mu.Lock()
	oldRuntime := m.mcpRuntimes[name]
	oldNames := append([]string(nil), m.mcpToolNames[name]...)
	delete(m.mcpRuntimes, name)
	delete(m.mcpToolNames, name)
	delete(m.mcpErrors, name)
	m.mcpConfigs = cloneMCPConfigs(configs)
	m.mu.Unlock()
	for _, toolName := range oldNames {
		m.registry.Remove(toolName)
	}
	if oldRuntime != nil {
		_ = oldRuntime.Close()
	}
	if runtime != nil {
		m.attachMCPRuntime(name, runtime)
	}
}

func (m *ExtensionManager) currentMCPToolNames(name string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]string(nil), m.mcpToolNames[name]...)
}

func (m *ExtensionManager) usedToolNames(exclude []string) map[string]bool {
	used := map[string]bool{}
	for _, name := range m.registry.Names() {
		used[name] = true
	}
	for _, name := range exclude {
		delete(used, name)
	}
	return used
}

func (m *ExtensionManager) mcpExtensionStates() []ExtensionState {
	m.mu.RLock()
	configs := cloneMCPConfigs(m.mcpConfigs)
	errorsByName := make(map[string]string, len(m.mcpErrors))
	toolsByName := make(map[string][]string, len(m.mcpToolNames))
	for name, message := range m.mcpErrors {
		errorsByName[name] = message
	}
	for name, tools := range m.mcpToolNames {
		toolsByName[name] = append([]string(nil), tools...)
	}
	m.mu.RUnlock()
	states := make([]ExtensionState, 0, len(configs))
	for _, name := range sortedMCPServerNames(configs) {
		server := configs[name]
		source := ""
		if command := strings.TrimSpace(server.Command); command != "" {
			source = filepath.Base(command)
		}
		if server.transport() == "streamable_http" {
			source = redactedSourceURL(server.URL)
		}
		tools := toolsByName[name]
		sort.Strings(tools)
		states = append(states, ExtensionState{
			Kind:      ExtensionKindMCP,
			ID:        "mcp:" + name,
			Name:      name,
			Managed:   true,
			Installed: true,
			Enabled:   server.enabled(),
			Source:    source,
			Transport: server.transport(),
			Tools:     tools,
			Error:     errorsByName[name],
		})
	}
	return states
}

func (m *ExtensionManager) mcpState(name string) (ExtensionState, bool) {
	for _, state := range m.mcpExtensionStates() {
		if state.ID == "mcp:"+name {
			return state, true
		}
	}
	return ExtensionState{}, false
}

func mcpServerConfigFromInput(input map[string]any) (mcpServerConfig, error) {
	enabled := boolFromInput(input, "enabled", true)
	inheritEnv := false
	server := mcpServerConfig{
		Command:           stringFromInput(input, "command"),
		Args:              stringSliceFromInput(input, "args"),
		Env:               stringMapFromInput(input, "env"),
		CWD:               stringFromInput(input, "cwd"),
		URL:               stringFromInput(input, "url"),
		Headers:           stringMapFromInput(input, "headers"),
		InheritEnv:        &inheritEnv,
		Enabled:           &enabled,
		Required:          false,
		StartupTimeoutSec: intFromInput(input, "startup_timeout_sec", 0),
		ToolTimeoutSec:    intFromInput(input, "tool_timeout_sec", 0),
		EnabledTools:      stringSliceFromInput(input, "enabled_tools"),
		DisabledTools:     stringSliceFromInput(input, "disabled_tools"),
	}
	server = normalizeMCPServerConfig(server)
	return server, validateMCPConfigValues(server)
}

func normalizeMCPServerConfig(server mcpServerConfig) mcpServerConfig {
	server.Command = strings.TrimSpace(server.Command)
	server.CWD = strings.TrimSpace(server.CWD)
	server.URL = strings.TrimSpace(server.URL)
	server.Args = cleanStringListPreserveOrder(server.Args)
	server.EnabledTools = cleanStringList(server.EnabledTools)
	server.DisabledTools = cleanStringList(server.DisabledTools)
	server.Env = cleanStringMap(server.Env)
	server.Headers = cleanStringMap(server.Headers)
	return server
}

func validateMCPConfigValues(server mcpServerConfig) error {
	if err := server.validate(); err != nil {
		return err
	}
	if server.StartupTimeoutSec > 120 {
		return errors.New("startup_timeout_sec cannot exceed 120")
	}
	if server.ToolTimeoutSec > 300 {
		return errors.New("tool_timeout_sec cannot exceed 300")
	}
	for key := range server.Env {
		if !environmentKeyPattern.MatchString(key) {
			return fmt.Errorf("invalid environment variable name %q", key)
		}
	}
	for key, value := range server.Headers {
		if !httpguts.ValidHeaderFieldName(key) || !httpguts.ValidHeaderFieldValue(value) {
			return fmt.Errorf("invalid HTTP header %q", key)
		}
		if reservedMCPHeader(key) {
			return fmt.Errorf("HTTP header %q cannot be overridden", key)
		}
	}
	return nil
}

func reservedMCPHeader(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if strings.HasPrefix(key, "mcp-") {
		return true
	}
	switch key {
	case "host", "content-length", "content-type", "accept", "connection", "transfer-encoding", "origin":
		return true
	default:
		return false
	}
}

func stringMapFromInput(input map[string]any, key string) map[string]string {
	raw, ok := input[key]
	if !ok || raw == nil {
		return nil
	}
	out := map[string]string{}
	switch values := raw.(type) {
	case map[string]string:
		for itemKey, value := range values {
			out[itemKey] = value
		}
	case map[string]any:
		for itemKey, value := range values {
			if text, ok := value.(string); ok {
				out[itemKey] = text
			}
		}
	}
	return out
}

func cleanStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		if key = strings.TrimSpace(key); key != "" {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cleanStringListPreserveOrder(values []string) []string {
	return append([]string(nil), values...)
}

func cloneMCPConfigs(configs map[string]mcpServerConfig) map[string]mcpServerConfig {
	out := make(map[string]mcpServerConfig, len(configs))
	for name, server := range configs {
		out[name] = cloneMCPServerConfig(server)
	}
	return out
}

func cloneMCPServerConfig(server mcpServerConfig) mcpServerConfig {
	server.Args = append([]string(nil), server.Args...)
	server.EnabledTools = append([]string(nil), server.EnabledTools...)
	server.DisabledTools = append([]string(nil), server.DisabledTools...)
	server.Env = cloneStringMap(server.Env)
	server.Headers = cloneStringMap(server.Headers)
	if server.InheritEnv != nil {
		inheritEnv := *server.InheritEnv
		server.InheritEnv = &inheritEnv
	}
	if server.Enabled != nil {
		enabled := *server.Enabled
		server.Enabled = &enabled
	}
	return server
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func closeMCPRuntimes(runtimes []*mcpServerRuntime) {
	for _, runtime := range runtimes {
		_ = runtime.Close()
	}
}
