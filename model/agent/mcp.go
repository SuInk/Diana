// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"github.com/SuInk/diana/model/version"
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
	// Preset/PresetTransport 记的是这条服务从哪个预设装出来的，界面按它把编辑框
	// 换回预设那张表（填地址和令牌），而不是让人对着命令行参数和环境变量 JSON 改。
	// 只记出身，不复制字段值：值仍然只有配置本身这一份，手改过也不会和表单对不上。
	Preset          string `json:"preset,omitempty" toml:"preset,omitempty"`
	PresetTransport string `json:"preset_transport,omitempty" toml:"preset_transport,omitempty"`
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
	if path == "" {
		// 没配就按默认位置算，不然会拼成工作目录本身，调用方拿到一个目录当配置文件。
		return filepath.Clean(defaultMCPConfigPath(cfg.WithDefaults().WorkDir))
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
	description := strings.TrimSpace(t.description)
	if description == "" {
		description = "MCP tool"
	}
	return fmt.Sprintf("MCP server %s tool %s. %s", t.serverName, t.rawName, description)
}

// InputSchema 把上游 MCP 的 schema 原样转发给 provider。它此前被拼进描述文本，
// 既没法参与原生约束解码，还会被描述的字数预算截断。
func (t *MCPTool) InputSchema() map[string]any {
	if len(t.inputSchema) == 0 || string(t.inputSchema) == "null" {
		return nil
	}
	var schema map[string]any
	if err := json.Unmarshal(t.inputSchema, &schema); err != nil {
		return nil
	}
	if len(schema) == 0 {
		return nil
	}
	return schema
}

func (t *MCPTool) Run(ctx context.Context, input map[string]any) (string, error) {
	return t.client.CallTool(ctx, t.rawName, input)
}

// MCPClient 持有一个 MCP 服务的会话。服务进程退出或远程会话失效后，下一次调用会先
// 重新连接再发请求；已经发出、中途断掉的那次调用不自动重试——服务可能已经执行了
// 一半，重发会把非幂等的操作做两遍。
type MCPClient struct {
	name           string
	config         mcpServerConfig
	workDir        string
	toolTimeout    time.Duration
	startupTimeout time.Duration

	mu            sync.Mutex
	current       *mcpSession
	closed        bool
	lastReconnect time.Time
	reconnectErr  error
}

type mcpSession struct {
	session *mcpsdk.ClientSession
	stderr  *lockedBuffer
	done    chan struct{}
}

func (s *mcpSession) alive() bool {
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

// mcpReconnectBackoff 限制连续重连：服务一启动就崩时，不能每次调用都卡一个启动超时。
const mcpReconnectBackoff = 5 * time.Second

func startMCPClient(ctx context.Context, name string, cfg mcpServerConfig, workDir string, toolTimeout time.Duration) (*MCPClient, error) {
	startupTimeout := time.Duration(DefaultMCPStartupTimeoutMS) * time.Millisecond
	if deadline, ok := ctx.Deadline(); ok {
		startupTimeout = time.Until(deadline)
	}
	client := &MCPClient{name: name, config: cfg, workDir: workDir, toolTimeout: toolTimeout, startupTimeout: startupTimeout}
	session, err := connectMCPSession(ctx, name, cfg, workDir, toolTimeout)
	if err != nil {
		return nil, err
	}
	client.current = session
	return client, nil
}

// mcpClientVersion 在握手里报出 Diana 的版本。以前写死成 0.5.0，服务端日志和兼容判断
// 看到的一直是一个早已不存在的版本。发布时 VERSION 必须与标签一致，所以源码基线可信。
func mcpClientVersion() string {
	if source := strings.TrimPrefix(version.Source(), "v"); source != "" {
		return source
	}
	return "0.0.0"
}

func connectMCPSession(ctx context.Context, name string, cfg mcpServerConfig, workDir string, toolTimeout time.Duration) (*mcpSession, error) {
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
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "diana-agent", Version: mcpClientVersion()}, &mcpsdk.ClientOptions{
		Capabilities: &mcpsdk.ClientCapabilities{},
	})
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, withMCPStderr(fmt.Errorf("mcp server %q connect failed: %w", name, err), stderr)
	}
	state := &mcpSession{session: session, stderr: stderr, done: make(chan struct{})}
	go func() {
		defer recoverGoroutinePanic("mcp_session_watcher")
		_ = session.Wait()
		close(state.done)
	}()
	return state, nil
}

// activeSession 返回可用会话；上一个会话已经断开时先重连。
func (c *MCPClient) activeSession(ctx context.Context) (*mcpSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("mcp server %q client is closed", c.name)
	}
	if c.current != nil && c.current.alive() {
		return c.current, nil
	}
	if c.current != nil {
		closeMCPSessionAsync(c.current)
		c.current = nil
	}
	if c.reconnectErr != nil && time.Since(c.lastReconnect) < mcpReconnectBackoff {
		return nil, c.reconnectErr
	}
	startupTimeout := c.startupTimeout
	if startupTimeout <= 0 {
		startupTimeout = time.Duration(DefaultMCPStartupTimeoutMS) * time.Millisecond
	}
	connectCtx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()
	c.lastReconnect = time.Now()
	session, err := connectMCPSession(connectCtx, c.name, c.config, c.workDir, c.toolTimeout)
	if err != nil {
		c.reconnectErr = fmt.Errorf("mcp server %q reconnect failed: %w", c.name, err)
		return nil, c.reconnectErr
	}
	c.reconnectErr = nil
	c.current = session
	return session, nil
}

// dropSession 把断掉的会话作废，下一次调用重连。只作废仍是当前的那一个，
// 避免把别的调用刚重连好的会话关掉。
func (c *MCPClient) dropSession(state *mcpSession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == state {
		closeMCPSessionAsync(state)
		c.current = nil
	}
}

// closeMCPSessionAsync 在后台关掉已经断开的会话。关闭 stdio 会话要等子进程退出，
// 最长 TerminateDuration，不能让下一次调用在锁里陪着等。
func closeMCPSessionAsync(state *mcpSession) {
	go func() {
		defer recoverGoroutinePanic("mcp_session_close")
		_ = state.session.Close()
	}()
}

func (c *MCPClient) ListTools(ctx context.Context) ([]mcpToolInfo, error) {
	state, err := c.activeSession(ctx)
	if err != nil {
		return nil, err
	}
	var all []mcpToolInfo
	var cursor string
	for {
		params := &mcpsdk.ListToolsParams{Cursor: cursor}
		result, err := state.session.ListTools(ctx, params)
		if err != nil {
			return nil, withMCPStderr(err, state.stderr)
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
	state, err := c.activeSession(callCtx)
	if err != nil {
		return "", err
	}
	mark := state.stderr.written()
	result, err := state.session.CallTool(callCtx, &mcpsdk.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		// 传输层断开（进程退出、连接被关）时作废会话，下一次调用重连。服务端返回的
		// JSON-RPC 错误不属于这一类，会话照常复用。
		if mcpTransportClosed(err) || !state.alive() {
			c.dropSession(state)
		}
		return "", withMCPStderrSince(fmt.Errorf("mcp server %q tools/call %q failed: %w", c.name, name, err), state.stderr, mark)
	}
	return formatSDKMCPToolResult(result)
}

func mcpTransportClosed(err error) bool {
	return errors.Is(err, mcpsdk.ErrConnectionClosed) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func (c *MCPClient) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.current == nil {
		return nil
	}
	err := c.current.session.Close()
	c.current = nil
	return err
}

// mcpInlineBinaryLimit 之内的二进制内容也不内联：工具输出进的是模型上下文，base64 只会
// 占满字数预算，模型读不出图片或音频。这里只留类型和大小，让模型知道服务返回了什么。
func describeMCPBinaryContent(kind, mimeType string, size int) string {
	mimeType = strings.TrimSpace(mimeType)
	if mimeType == "" {
		mimeType = "未知类型"
	}
	return fmt.Sprintf("[MCP 返回了%s（%s，%d 字节），二进制内容未放入文本结果]", kind, mimeType, size)
}

func formatSDKMCPToolResult(result *mcpsdk.CallToolResult) (string, error) {
	if result == nil {
		return "", errors.New("empty MCP tool result")
	}
	var parts []string
	for _, content := range result.Content {
		switch typed := content.(type) {
		case *mcpsdk.TextContent:
			parts = append(parts, typed.Text)
			continue
		case *mcpsdk.ImageContent:
			parts = append(parts, describeMCPBinaryContent("图片", typed.MIMEType, len(typed.Data)))
			continue
		case *mcpsdk.AudioContent:
			parts = append(parts, describeMCPBinaryContent("音频", typed.MIMEType, len(typed.Data)))
			continue
		case *mcpsdk.EmbeddedResource:
			if typed.Resource != nil && len(typed.Resource.Blob) > 0 {
				parts = append(parts, describeMCPBinaryContent("资源 "+typed.Resource.URI, typed.Resource.MIMEType, len(typed.Resource.Blob)))
				continue
			}
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
	mu    sync.Mutex
	data  []byte
	total int64
}

const maxMCPStderrBytes = 64 << 10

// maxReportedMCPStderrBytes 是一次报错里附带的 stderr 上限。报错会进模型上下文，
// 带上整个进程生命周期累积的 64KB 只是在浪费预算。
const maxReportedMCPStderrBytes = 4 << 10

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	written := len(p)
	b.total += int64(written)
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

// written 返回累计写入的字节数，用作「这次调用之后新增了哪些」的起点。
func (b *lockedBuffer) written() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total
}

// since 返回从 mark 之后写入、仍留在缓冲区里的内容。
func (b *lockedBuffer) since(mark int64) string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	start := b.total - int64(len(b.data))
	if mark <= start {
		return string(b.data)
	}
	if mark >= b.total {
		return ""
	}
	return string(b.data[mark-start:])
}

func withMCPStderr(err error, stderr *lockedBuffer) error {
	return appendMCPStderr(err, stderr.String())
}

func withMCPStderrSince(err error, stderr *lockedBuffer, mark int64) error {
	return appendMCPStderr(err, stderr.since(mark))
}

func appendMCPStderr(err error, detail string) error {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return err
	}
	if len(detail) > maxReportedMCPStderrBytes {
		detail = "…" + strings.ToValidUTF8(detail[len(detail)-maxReportedMCPStderrBytes:], "")
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

// mcpToolNamePrefix 是 MCP 工具在模型侧的固定前缀，权限提示据此识别这类名字。
const mcpToolNamePrefix = "mcp__"

func mcpModelToolName(server, tool string) string {
	name := mcpToolNamePrefix + sanitizeToolName(server) + "__" + sanitizeToolName(tool)
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
