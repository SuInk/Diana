package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/netguard"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pelletier/go-toml/v2"
)

type MCPRegistry struct {
	Tools   []Tool
	Closers []closeableTool
}

type mcpConfigFile struct {
	MCPServers map[string]mcpServerConfig `json:"mcpServers" toml:"mcp_servers"`
}

type mcpServerConfig struct {
	Command           string            `json:"command,omitempty" toml:"command,omitempty"`
	Args              []string          `json:"args,omitempty" toml:"args,omitempty"`
	Env               map[string]string `json:"env,omitempty" toml:"env,omitempty"`
	CWD               string            `json:"cwd,omitempty" toml:"cwd,omitempty"`
	URL               string            `json:"url,omitempty" toml:"url,omitempty"`
	Headers           map[string]string `json:"headers,omitempty" toml:"headers,omitempty"`
	InheritEnv        *bool             `json:"inherit_env,omitempty" toml:"inherit_env,omitempty"`
	Enabled           *bool             `json:"enabled,omitempty" toml:"enabled,omitempty"`
	Required          bool              `json:"required,omitempty" toml:"required,omitempty"`
	StartupTimeoutSec int               `json:"startup_timeout_sec,omitempty" toml:"startup_timeout_sec,omitempty"`
	ToolTimeoutSec    int               `json:"tool_timeout_sec,omitempty" toml:"tool_timeout_sec,omitempty"`
	EnabledTools      []string          `json:"enabled_tools,omitempty" toml:"enabled_tools,omitempty"`
	DisabledTools     []string          `json:"disabled_tools,omitempty" toml:"disabled_tools,omitempty"`
}

func (cfg mcpServerConfig) enabled() bool {
	return cfg.Enabled == nil || *cfg.Enabled
}

func (cfg mcpServerConfig) inheritEnvironment() bool {
	// Existing hand-written configurations historically inherited Diana's full
	// process environment. Self-installed servers persist false explicitly.
	return cfg.InheritEnv == nil || *cfg.InheritEnv
}

func (cfg mcpServerConfig) transport() string {
	if strings.TrimSpace(cfg.URL) != "" {
		return "streamable_http"
	}
	if strings.TrimSpace(cfg.Command) != "" {
		return "stdio"
	}
	return "unknown"
}

func (cfg mcpServerConfig) allowsTool(name string) bool {
	if len(cfg.EnabledTools) > 0 && !slices.Contains(cfg.EnabledTools, name) {
		return false
	}
	return !slices.Contains(cfg.DisabledTools, name)
}

func (cfg mcpServerConfig) validate() error {
	hasCommand := strings.TrimSpace(cfg.Command) != ""
	hasURL := strings.TrimSpace(cfg.URL) != ""
	if hasCommand == hasURL {
		return errors.New("configure exactly one of command or url")
	}
	if cfg.StartupTimeoutSec < 0 || cfg.ToolTimeoutSec < 0 {
		return errors.New("timeouts cannot be negative")
	}
	return nil
}

// NewMCPRegistry remains the standalone MCP loader used by tests and callers
// that do not need self-management. The official SDK handles protocol
// negotiation for both current and legacy MCP servers.
func NewMCPRegistry(ctx context.Context, cfg Config) (MCPRegistry, error) {
	cfg = cfg.WithDefaults()
	path := resolveMCPConfigPath(cfg)
	servers, err := loadMCPServers(path)
	if err != nil {
		return MCPRegistry{}, err
	}
	var registry MCPRegistry
	usedNames := map[string]bool{}
	for _, name := range sortedMCPServerNames(servers) {
		server := servers[name]
		if !server.enabled() {
			continue
		}
		runtime, startErr := startMCPServerRuntime(ctx, name, server, cfg, usedNames)
		if startErr != nil {
			if server.Required {
				closeMCPClosers(registry.Closers)
				return MCPRegistry{}, startErr
			}
			continue
		}
		if len(runtime.tools) == 0 {
			_ = runtime.Close()
			continue
		}
		for _, tool := range runtime.tools {
			registry.Tools = append(registry.Tools, tool)
		}
		registry.Closers = append(registry.Closers, runtime)
	}
	return registry, nil
}

func resolveMCPConfigPath(cfg Config) string {
	path := strings.TrimSpace(cfg.MCPConfigPath)
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	base, err := filepath.Abs(cfg.WorkDir)
	if err != nil {
		base = cfg.WorkDir
	}
	return filepath.Clean(filepath.Join(base, path))
}

func loadMCPServers(path string) (map[string]mcpServerConfig, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]mcpServerConfig{}, nil
		}
		return nil, err
	}
	var cfg mcpConfigFile
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		if err := toml.Unmarshal(body, &cfg); err != nil {
			return nil, err
		}
	default:
		if err := json.Unmarshal(body, &cfg); err != nil {
			return nil, err
		}
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = map[string]mcpServerConfig{}
	}
	return cfg.MCPServers, nil
}

func saveMCPServers(path string, servers map[string]mcpServerConfig) error {
	file := mcpConfigFile{MCPServers: servers}
	var (
		body []byte
		err  error
	)
	if strings.EqualFold(filepath.Ext(path), ".toml") {
		body, err = toml.Marshal(file)
	} else {
		body, err = json.MarshalIndent(file, "", "  ")
		body = append(body, '\n')
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".mcp-config-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err == nil {
		return nil
	}
	// Windows cannot atomically replace an existing file with Rename. Keep a
	// rollback copy while performing the two renames.
	backup := path + ".replace-backup"
	_ = os.Remove(backup)
	if err := os.Rename(path, backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Rename(backup, path)
		return err
	}
	_ = os.Remove(backup)
	return nil
}

type mcpServerRuntime struct {
	name   string
	config mcpServerConfig
	client *MCPClient
	tools  []*MCPTool
}

func startMCPServerRuntime(ctx context.Context, name string, server mcpServerConfig, cfg Config, usedNames map[string]bool) (*mcpServerRuntime, error) {
	if err := server.validate(); err != nil {
		return nil, fmt.Errorf("mcp server %q: %w", name, err)
	}
	startupTimeout := time.Duration(firstPositive(server.StartupTimeoutSec*1000, cfg.MCPStartupTimeoutMS)) * time.Millisecond
	toolTimeout := time.Duration(firstPositive(server.ToolTimeoutSec*1000, cfg.MCPToolTimeoutMS)) * time.Millisecond
	startCtx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()
	client, err := startMCPClient(startCtx, name, server, cfg.WorkDir, toolTimeout)
	if err != nil {
		return nil, err
	}
	runtime := &mcpServerRuntime{name: name, config: server, client: client}
	tools, err := client.ListTools(startCtx)
	if err != nil {
		_ = runtime.Close()
		return nil, fmt.Errorf("mcp server %q tools/list failed: %w", name, err)
	}
	for _, raw := range tools {
		if !server.allowsTool(raw.Name) {
			continue
		}
		modelName := uniqueMCPModelToolName(name, raw.Name, usedNames)
		runtime.tools = append(runtime.tools, &MCPTool{
			client:      client,
			serverName:  name,
			rawName:     raw.Name,
			modelName:   modelName,
			description: raw.Description,
			inputSchema: append(json.RawMessage(nil), raw.InputSchema...),
		})
	}
	return runtime, nil
}

func (r *mcpServerRuntime) Close() error {
	if r == nil || r.client == nil {
		return nil
	}
	return r.client.Close()
}

type mcpToolInfo struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

type MCPTool struct {
	client      *MCPClient
	serverName  string
	rawName     string
	modelName   string
	description string
	inputSchema json.RawMessage
}

func (t *MCPTool) Name() string { return t.modelName }

func (t *MCPTool) Description() string {
	var parts []string
	if strings.TrimSpace(t.description) != "" {
		parts = append(parts, strings.TrimSpace(t.description))
	}
	if len(t.inputSchema) > 0 && string(t.inputSchema) != "null" {
		parts = append(parts, "input schema: "+string(t.inputSchema))
	}
	if len(parts) == 0 {
		parts = append(parts, "MCP tool")
	}
	return fmt.Sprintf("MCP server %s tool %s. %s", t.serverName, t.rawName, strings.Join(parts, " "))
}

func (t *MCPTool) Run(ctx context.Context, input map[string]any) (string, error) {
	return t.client.CallTool(ctx, t.rawName, input)
}

type MCPClient struct {
	name        string
	session     *mcpsdk.ClientSession
	stderr      *lockedBuffer
	toolTimeout time.Duration
	closeOnce   sync.Once
	closeErr    error
}

func startMCPClient(ctx context.Context, name string, cfg mcpServerConfig, workDir string, toolTimeout time.Duration) (*MCPClient, error) {
	var (
		transport mcpsdk.Transport
		stderr    *lockedBuffer
	)
	if command := strings.TrimSpace(cfg.Command); command != "" {
		cmd := exec.Command(command, cfg.Args...)
		if cwd := strings.TrimSpace(cfg.CWD); cwd != "" {
			if !filepath.IsAbs(cwd) {
				cwd = filepath.Join(workDir, cwd)
			}
			cmd.Dir = filepath.Clean(cwd)
		}
		cmd.Env = mergedCommandEnvironment(cfg.Env, cfg.inheritEnvironment())
		stderr = &lockedBuffer{}
		cmd.Stderr = stderr
		transport = &mcpsdk.CommandTransport{Command: cmd, TerminateDuration: 2 * time.Second}
	} else {
		endpoint := strings.TrimSpace(cfg.URL)
		if err := netguard.ValidatePublicURL(ctx, endpoint); err != nil {
			return nil, fmt.Errorf("mcp server %q URL rejected: %w", name, err)
		}
		origin, err := url.Parse(endpoint)
		if err != nil {
			return nil, fmt.Errorf("mcp server %q URL rejected: %w", name, err)
		}
		httpClient := netguard.NewPublicHTTPClient(toolTimeout)
		httpClient.Transport = &mcpHeaderTransport{base: httpClient.Transport, headers: expandedMCPHeaders(cfg.Headers), origin: origin}
		transport = &mcpsdk.StreamableClientTransport{
			Endpoint:             endpoint,
			HTTPClient:           httpClient,
			MaxRetries:           -1,
			DisableStandaloneSSE: true,
		}
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "diana-agent", Version: "0.5.0"}, &mcpsdk.ClientOptions{
		Capabilities: &mcpsdk.ClientCapabilities{},
	})
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, withMCPStderr(fmt.Errorf("mcp server %q connect failed: %w", name, err), stderr)
	}
	return &MCPClient{name: name, session: session, stderr: stderr, toolTimeout: toolTimeout}, nil
}

func (c *MCPClient) ListTools(ctx context.Context) ([]mcpToolInfo, error) {
	var all []mcpToolInfo
	var cursor string
	for {
		params := &mcpsdk.ListToolsParams{Cursor: cursor}
		result, err := c.session.ListTools(ctx, params)
		if err != nil {
			return nil, withMCPStderr(err, c.stderr)
		}
		for _, tool := range result.Tools {
			if tool == nil || strings.TrimSpace(tool.Name) == "" {
				continue
			}
			schema, _ := json.Marshal(tool.InputSchema)
			all = append(all, mcpToolInfo{Name: tool.Name, Description: tool.Description, InputSchema: schema})
		}
		if strings.TrimSpace(result.NextCursor) == "" {
			return all, nil
		}
		cursor = result.NextCursor
	}
}

func (c *MCPClient) CallTool(ctx context.Context, name string, arguments map[string]any) (string, error) {
	if arguments == nil {
		arguments = map[string]any{}
	}
	timeout := c.toolTimeout
	if timeout <= 0 {
		timeout = time.Duration(DefaultMCPToolTimeoutMS) * time.Millisecond
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := c.session.CallTool(callCtx, &mcpsdk.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return "", withMCPStderr(fmt.Errorf("mcp server %q tools/call %q failed: %w", c.name, name, err), c.stderr)
	}
	output, resultErr := formatSDKMCPToolResult(result)
	if resultErr != nil {
		return output, resultErr
	}
	return output, nil
}

func (c *MCPClient) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		if c.session != nil {
			c.closeErr = c.session.Close()
		}
	})
	return c.closeErr
}

func formatSDKMCPToolResult(result *mcpsdk.CallToolResult) (string, error) {
	if result == nil {
		return "", errors.New("empty MCP tool result")
	}
	var parts []string
	for _, content := range result.Content {
		if textContent, ok := content.(*mcpsdk.TextContent); ok {
			parts = append(parts, textContent.Text)
			continue
		}
		body, err := content.MarshalJSON()
		if err == nil {
			parts = append(parts, string(body))
		}
	}
	if result.StructuredContent != nil && len(parts) == 0 {
		if body, err := json.Marshal(result.StructuredContent); err == nil {
			parts = append(parts, string(body))
		}
	}
	output := strings.TrimSpace(strings.Join(parts, "\n"))
	if result.NeedsInput() {
		return output, errors.New("MCP tool requires interactive input, which Diana cannot satisfy in the current chat turn")
	}
	if result.IsError {
		if output == "" {
			output = "MCP tool returned an error"
		}
		return output, errors.New(output)
	}
	return output, nil
}

type lockedBuffer struct {
	mu   sync.Mutex
	data []byte
}

const maxMCPStderrBytes = 64 << 10

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	written := len(p)
	if len(p) >= maxMCPStderrBytes {
		b.data = append(b.data[:0], p[len(p)-maxMCPStderrBytes:]...)
		return written, nil
	}
	if overflow := len(b.data) + len(p) - maxMCPStderrBytes; overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
	}
	b.data = append(b.data, p...)
	return written, nil
}

func (b *lockedBuffer) String() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

func withMCPStderr(err error, stderr *lockedBuffer) error {
	detail := strings.TrimSpace(stderr.String())
	if detail == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, detail)
}

type mcpHeaderTransport struct {
	base    http.RoundTripper
	headers map[string]string
	origin  *url.URL
}

func (t *mcpHeaderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.Header = req.Header.Clone()
	if sameHTTPOrigin(t.origin, cloned.URL) {
		for key, value := range t.headers {
			cloned.Header.Set(key, value)
		}
	}
	return t.base.RoundTrip(cloned)
}

func sameHTTPOrigin(left, right *url.URL) bool {
	if left == nil || right == nil {
		return false
	}
	return strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(left.Hostname(), right.Hostname()) &&
		effectiveHTTPPort(left) == effectiveHTTPPort(right)
}

func effectiveHTTPPort(value *url.URL) string {
	if value == nil {
		return ""
	}
	if port := value.Port(); port != "" {
		return port
	}
	if strings.EqualFold(value.Scheme, "https") {
		return "443"
	}
	if strings.EqualFold(value.Scheme, "http") {
		return "80"
	}
	return ""
}

func expandedMCPHeaders(headers map[string]string) map[string]string {
	expanded := make(map[string]string, len(headers))
	for key, value := range headers {
		key = strings.TrimSpace(key)
		if key != "" {
			expanded[key] = os.ExpandEnv(value)
		}
	}
	return expanded
}

func mergedCommandEnvironment(overrides map[string]string, inheritAll bool) []string {
	values := map[string]string{}
	for _, item := range os.Environ() {
		key, value, found := strings.Cut(item, "=")
		if found && (inheritAll || safeMCPEnvironmentKey(key)) {
			values[key] = value
		}
	}
	for key, value := range overrides {
		if key = strings.TrimSpace(key); key != "" {
			values[key] = os.ExpandEnv(value)
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+values[key])
	}
	return out
}

func safeMCPEnvironmentKey(key string) bool {
	switch strings.ToUpper(strings.TrimSpace(key)) {
	case "PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "TEMP", "TMP",
		"SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT", "USERPROFILE", "APPDATA", "LOCALAPPDATA",
		"LANG", "LANGUAGE", "LC_ALL", "TERM", "SSL_CERT_FILE", "SSL_CERT_DIR",
		"XDG_CACHE_HOME", "XDG_CONFIG_HOME", "NPM_CONFIG_CACHE":
		return true
	default:
		return false
	}
}

func sortedMCPServerNames(servers map[string]mcpServerConfig) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func closeMCPClosers(closers []closeableTool) {
	for _, closer := range closers {
		_ = closer.Close()
	}
}

func mcpModelToolName(server, tool string) string {
	name := "mcp__" + sanitizeToolName(server) + "__" + sanitizeToolName(tool)
	if len(name) <= 64 {
		return name
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))[:12]
	return name[:51] + "_" + hash
}

func uniqueMCPModelToolName(server, tool string, used map[string]bool) string {
	base := mcpModelToolName(server, tool)
	if !used[base] {
		used[base] = true
		return base
	}
	for index := 2; ; index++ {
		suffix := fmt.Sprintf("_%d", index)
		candidate := base
		if len(candidate)+len(suffix) > 64 {
			candidate = candidate[:64-len(suffix)]
		}
		candidate += suffix
		if !used[candidate] {
			used[candidate] = true
			return candidate
		}
	}
}

func sanitizeToolName(name string) string {
	name = strings.TrimSpace(name)
	var builder strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('_')
		}
	}
	if builder.Len() == 0 {
		return "_"
	}
	return builder.String()
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
