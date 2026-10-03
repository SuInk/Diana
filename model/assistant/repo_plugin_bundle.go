// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/SuInk/diana/model/agent"
)

var pluginBundleVariable = regexp.MustCompile(`\$\{([^}]+)\}`)
var pluginBundleEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (m repoPluginManifestFile) hasBundle() bool {
	return len(m.Skills) > 0 || len(m.Scripts) > 0 || len(m.MCPServers) > 0
}

func (m repoPluginManifestFile) skillRoots() []string {
	if len(m.Skills) > 0 {
		return m.Skills
	}
	return []string{"."}
}

func sortedPluginScriptNames(scripts map[string]agent.PluginScript) []string {
	names := make([]string, 0, len(scripts))
	for name := range scripts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func validBundlePath(path string, allowRoot bool) bool {
	if path == "" || path != strings.TrimSpace(path) || strings.ContainsAny(path, `\:`) || strings.Contains(path, "${") || strings.HasPrefix(path, "/") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return false
		}
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	return clean != "." || allowRoot
}

func (m repoPluginManifestFile) validateBundle(wrap func(string, ...any) error) error {
	permissions := map[string]bool{}
	for _, permission := range m.Permissions {
		permissions[permission] = true
	}
	settings := map[string]bool{}
	for _, spec := range m.Settings {
		settings[spec.Key] = true
	}
	validateValue := func(value string) error {
		for _, match := range pluginBundleVariable.FindAllStringSubmatch(value, -1) {
			if match[1] == "PLUGIN_ROOT" {
				continue
			}
			key, ok := strings.CutPrefix(match[1], "settings.")
			if !ok || !settings[key] {
				return wrap("插件配置引用了未知变量 %q", match[1])
			}
		}
		return nil
	}
	validateMap := func(values map[string]string, environment bool) error {
		for key, value := range values {
			if environment && !pluginBundleEnvName.MatchString(key) {
				return wrap("非法环境变量名 %q", key)
			}
			if err := validateValue(value); err != nil {
				return err
			}
		}
		return nil
	}
	network := permissions["network:http"] || permissions["network:https"]
	for _, root := range m.Skills {
		if !validBundlePath(root, true) {
			return wrap("skills 包含非法路径 %q", root)
		}
	}
	for name, script := range m.Scripts {
		if !pluginIDPattern.MatchString(name) {
			return wrap("非法 scripts 入口名 %q", name)
		}
		if !permissions["process:execute"] {
			return wrap("scripts 需要声明 process:execute")
		}
		if script.Command == "" || script.Command == "*" || strings.ContainsAny(script.Command, "/\\ \t\r\n") {
			return wrap("scripts.%s.command 必须是程序名", name)
		}
		if !validBundlePath(script.Entry, false) {
			return wrap("scripts.%s.entry 是非法路径", name)
		}
		if script.WorkingDirectory != "" && script.WorkingDirectory != "workspace" && script.WorkingDirectory != "plugin" {
			return wrap("scripts.%s.working_directory 必须是 workspace 或 plugin", name)
		}
		if script.AllowNetwork && !network {
			return wrap("scripts.%s.allow_network 需要声明网络权限", name)
		}
		if err := validateMap(script.Env, true); err != nil {
			return err
		}
		for _, arg := range script.Args {
			if strings.Contains(arg, "${") {
				return wrap("scripts 的固定 args 不接受变量，请通过 env 传入设置")
			}
		}
	}
	for name, server := range m.MCPServers {
		if !pluginIDPattern.MatchString(name) {
			return wrap("非法 MCP 名称 %q", name)
		}
		if err := server.Validate(); err != nil {
			return wrap("mcp_servers.%s: %v", name, err)
		}
		if !permissions["agent:tool"] && !permissions["llm:tool"] {
			return wrap("mcp_servers 需要声明 agent:tool 或 llm:tool")
		}
		if server.InheritEnv != nil && *server.InheritEnv {
			return wrap("插件 MCP 不允许 inherit_env=true")
		}
		if server.Command != "" {
			if !permissions["process:execute"] {
				return wrap("本地 MCP 需要声明 process:execute")
			}
			if strings.ContainsAny(server.Command, "/\\ \t\r\n") || server.Command == "*" {
				return wrap("本地 MCP command 必须是程序名")
			}
			if server.CWD != "" && !validBundlePath(server.CWD, true) {
				return wrap("本地 MCP cwd 必须位于插件目录内")
			}
			for _, arg := range server.Args {
				if err := validateValue(arg); err != nil {
					return err
				}
			}
		} else {
			if !network {
				return wrap("远程 MCP 需要声明网络权限")
			}
			if err := validateValue(server.URL); err != nil {
				return err
			}
		}
		if err := validateMap(server.Env, true); err != nil {
			return err
		}
		if err := validateMap(server.Headers, false); err != nil {
			return err
		}
	}
	return nil
}

func (m repoPluginManifestFile) validateBundleArchive(index repoArchiveIndex) error {
	if !m.hasBundle() {
		return nil
	}
	for _, root := range m.skillRoots() {
		root = strings.TrimSuffix(filepath.ToSlash(filepath.Clean(root)), "/")
		found := false
		for path, body := range index.contents {
			if filepath.Base(path) != RepoPluginEntryFile {
				continue
			}
			dir := filepath.ToSlash(filepath.Dir(path))
			if dir != root && filepath.ToSlash(filepath.Dir(dir)) != root {
				continue
			}
			if err := validateSkillFrontmatter(body); err != nil {
				return fmt.Errorf("%w: %s", err, path)
			}
			found = true
		}
		if !found {
			return fmt.Errorf("%w: skills 目录 %s 没有有效 SKILL.md", ErrRepoPluginSkill, root)
		}
	}
	return nil
}

func resolveBundleValue(value, root string, settings SettingValues) string {
	return pluginBundleVariable.ReplaceAllStringFunc(value, func(variable string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(variable, "${"), "}")
		if name == "PLUGIN_ROOT" {
			return root
		}
		key := strings.TrimPrefix(name, "settings.")
		if value, ok := settings[key]; ok && value != nil {
			return fmt.Sprint(value)
		}
		return ""
	})
}

func resolveBundleMap(values map[string]string, root string, settings SettingValues) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = resolveBundleValue(value, root, settings)
	}
	return out
}

type repoPluginBundleSession struct {
	key     string
	mcp     agent.MCPRegistry
	active  atomic.Bool
	used    time.Time
	created time.Time
	ctx     context.Context
	cancel  context.CancelFunc
}

func (s *repoPluginBundleSession) close() {
	s.active.Store(false)
	if s.cancel != nil {
		s.cancel()
	}
	_ = s.mcp.Close()
}

// Close releases all bundle sessions. The installed plugin can be enabled again.
func (p *RepoPlugin) Close() error {
	p.closeBundleProfile("")
	return nil
}

func (p *RepoPlugin) closeBundleProfile(profile string) {
	p.bundleMu.Lock()
	defer p.bundleMu.Unlock()
	for scope, session := range p.bundleSessions {
		if profile == "" || strings.HasPrefix(scope, profile+"\x00") {
			session.close()
			delete(p.bundleSessions, scope)
		}
	}
}

func (p *RepoPlugin) bundleRuntime(ctx context.Context, scope string, cfg agent.Config, settings SettingValues) (*repoPluginBundleSession, map[string]agent.PluginScript, error) {
	root, err := filepath.Abs(p.dir)
	if err != nil {
		return nil, nil, err
	}
	scripts := map[string]agent.PluginScript{}
	for name, script := range p.bundle.Scripts {
		script.Env = resolveBundleMap(script.Env, root, settings)
		scripts[name] = script
	}
	servers := map[string]agent.PluginMCPServer{}
	for name, server := range p.bundle.MCPServers {
		server.Env = resolveBundleMap(server.Env, root, settings)
		server.Headers = resolveBundleMap(server.Headers, root, settings)
		server.URL = resolveBundleValue(server.URL, root, settings)
		server.Args = append([]string(nil), server.Args...)
		for i, arg := range server.Args {
			server.Args[i] = resolveBundleValue(arg, root, settings)
		}
		servers[name] = server
	}
	keyBody, _ := json.Marshal([]any{scripts, servers, cfg.WorkDir, cfg.MCPStartupTimeoutMS, cfg.MCPToolTimeoutMS, cfg.CommandAllowlist, cfg.CommandSandbox, cfg.CommandSandboxAllowNetwork})
	digest := sha256.Sum256(keyBody)
	key := fmt.Sprintf("%x", digest)
	p.bundleMu.Lock()
	defer p.bundleMu.Unlock()
	if session := p.bundleSessions[scope]; session != nil {
		if session.key == key && (len(session.mcp.Warnings) == 0 || time.Since(session.created) < 5*time.Second) {
			session.used = time.Now()
			return session, scripts, nil
		}
		session.close()
		delete(p.bundleSessions, scope)
	}
	if p.bundleSessions == nil {
		p.bundleSessions = map[string]*repoPluginBundleSession{}
	}
	// Bound processes even if the owner enables this bundle in many groups.
	if len(p.bundleSessions) >= 16 {
		oldest := ""
		for candidate, session := range p.bundleSessions {
			if oldest == "" || session.used.Before(p.bundleSessions[oldest].used) {
				oldest = candidate
			}
		}
		p.bundleSessions[oldest].close()
		delete(p.bundleSessions, oldest)
	}
	mcp, err := agent.NewPluginMCPRegistry(ctx, p.manifest.ID, root, servers, cfg)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	session := &repoPluginBundleSession{key: key, mcp: mcp, used: now, created: now}
	session.ctx, session.cancel = context.WithCancel(context.Background())
	session.active.Store(true)
	p.bundleSessions[scope] = session
	return session, scripts, nil
}

func (r *Runtime) registerRepoPluginBundles(ctx context.Context, registry *agent.ToolRegistry, cfg agent.Config, event MessageEvent) error {
	if r.plugins == nil {
		return nil
	}
	var skills []agent.SkillMetadata
	for _, state := range r.plugins.List() {
		value, settings, enabled := r.pluginWithSettingsForEvent(state.Manifest.ID, event)
		plugin, ok := value.(*RepoPlugin)
		if !ok || !enabled || !pluginSupportsPlatform(state.Manifest, event.Platform) || plugin.bundle == nil {
			continue
		}
		loaded, err := agent.LoadPluginSkills(plugin.manifest.ID, plugin.dir, plugin.source.URL, plugin.bundle.skillRoots(), len(plugin.bundle.Scripts) > 0)
		if err != nil {
			return fmt.Errorf("diana: plugin %q skills: %w", plugin.manifest.ID, err)
		}
		if !cfg.ExtensionManagement {
			// Safe mode can read instructions and resources without starting
			// third-party processes or exposing fixed script/MCP entry points.
			warnings := []string{agentSafeModeDisabledMessage}
			tool := agent.NewPluginResourceTool(plugin.manifest.ID, plugin.dir, cfg, plugin.bundle.Scripts, warnings)
			registry.RegisterPluginBundleCatalog(plugin.manifest.ID, loaded, []agent.Tool{tool}, warnings)
			registry.Register(tool)
			skills = append(skills, loaded...)
			continue
		}
		session, scripts, err := plugin.bundleRuntime(ctx, event.ProfileID+"\x00"+event.GroupID, cfg, settings)
		secrets := bundleSecrets(plugin.manifest, settings)
		if err != nil {
			return fmt.Errorf("diana: plugin %q MCP: %s", plugin.manifest.ID, redactBundleText(err.Error(), secrets))
		}
		current, currentSettings, stillEnabled := r.pluginWithSettingsForEvent(plugin.manifest.ID, event)
		if !stillEnabled || current != plugin || !reflect.DeepEqual(currentSettings, settings) || !session.active.Load() {
			plugin.bundleMu.Lock()
			scope := event.ProfileID + "\x00" + event.GroupID
			if plugin.bundleSessions[scope] == session {
				session.close()
				delete(plugin.bundleSessions, scope)
			}
			plugin.bundleMu.Unlock()
			continue
		}
		skills = append(skills, loaded...)
		warnings := make([]string, len(session.mcp.Warnings))
		for i, warning := range session.mcp.Warnings {
			warnings[i] = redactBundleText(warning, secrets)
		}
		provided := []agent.Tool{agent.NewPluginResourceTool(plugin.manifest.ID, plugin.dir, cfg, scripts, warnings)}
		if len(scripts) > 0 {
			provided = append(provided, agent.NewPluginScriptTool(plugin.manifest.ID, plugin.dir, cfg, scripts, secrets...))
		}
		provided = append(provided, session.mcp.Tools...)
		registry.RegisterPluginBundleCatalog(plugin.manifest.ID, loaded, provided, warnings)
		for _, tool := range provided {
			registry.Register(&repoBundleTool{delegate: tool, session: session, secrets: secrets, enabled: func() bool {
				current, _, enabled := r.pluginWithSettingsForEvent(plugin.manifest.ID, event)
				return enabled && current == plugin
			}})
		}
	}
	if len(skills) > 0 {
		registry.RegisterScopedSkills(cfg.BuiltinSkills, append(registry.Skills(), skills...), cfg.ReservedSkillNames)
	}
	return nil
}

func bundleSecrets(manifest PluginManifest, settings SettingValues) []string {
	var secrets []string
	for _, spec := range manifest.Settings {
		if spec.Secret {
			if value, ok := settings[spec.Key].(string); ok && value != "" {
				secrets = append(secrets, value)
			}
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	return secrets
}

func redactBundleText(text string, secrets []string) string {
	for _, secret := range secrets {
		text = strings.ReplaceAll(text, secret, "[REDACTED]")
		encoded, _ := json.Marshal(secret)
		if len(encoded) > 2 {
			text = strings.ReplaceAll(text, string(encoded[1:len(encoded)-1]), "[REDACTED]")
		}
	}
	return text
}

type repoBundleTool struct {
	delegate agent.Tool
	session  *repoPluginBundleSession
	secrets  []string
	enabled  func() bool
}

func (t *repoBundleTool) Name() string { return t.delegate.Name() }
func (t *repoBundleTool) Description() string {
	return redactBundleText(t.delegate.Description(), t.secrets)
}
func (t *repoBundleTool) InputSchema() map[string]any {
	if tool, ok := t.delegate.(agent.ToolInputSchema); ok {
		schema := tool.InputSchema()
		if len(t.secrets) == 0 {
			return schema
		}
		body, err := json.Marshal(schema)
		if err != nil {
			return nil
		}
		var redacted map[string]any
		if json.Unmarshal([]byte(redactBundleText(string(body), t.secrets)), &redacted) != nil {
			return nil
		}
		return redacted
	}
	return map[string]any{"type": "object"}
}
func (t *repoBundleTool) Introspection(input map[string]any) bool {
	if tool, ok := t.delegate.(agent.IntrospectionTool); ok {
		return tool.Introspection(input)
	}
	return false
}
func (t *repoBundleTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if !t.session.active.Load() || !t.enabled() {
		return "", errors.New("plugin bundle is disabled, updated or uninstalled; reload its capabilities")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(t.session.ctx, cancel)
	defer stop()
	output, err := t.delegate.Run(runCtx, input)
	if err != nil {
		err = errors.New(redactBundleText(err.Error(), t.secrets))
	}
	return redactBundleText(output, t.secrets), err
}

// CloseRepoPluginRuntimes is called when the runtime's extension cache stops.
func (m *PluginManager) CloseRepoPluginRuntimes() {
	if m == nil {
		return
	}
	m.mu.RLock()
	var plugins []*RepoPlugin
	for _, plugin := range m.catalog {
		if repo, ok := plugin.(*RepoPlugin); ok {
			plugins = append(plugins, repo)
		}
	}
	m.mu.RUnlock()
	for _, plugin := range plugins {
		_ = plugin.Close()
	}
}

func closeRepoPluginRuntime(plugin Plugin, profile string) {
	if repo, ok := plugin.(*RepoPlugin); ok {
		repo.closeBundleProfile(profile)
	}
}
