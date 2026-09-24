// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/internal/procgroup"
	"github.com/SuInk/diana/internal/secretmask"
	"github.com/SuInk/diana/model/llm"
)

type Tool interface {
	Name() string
	Description() string
	Run(ctx context.Context, input map[string]any) (string, error)
}

// IntrospectionTool 由工具自己声明「这次调用只是打听 Diana 自己」。声明了就不占
// MaxSteps，改走自省配额（见 runner.go 的 maxIntrospectionCallsPerAgentRun）。
//
// 门槛是三条同时成立，不是「跑得快」：
//   - 只读：不改任何状态，也不对外部世界产生动作；
//   - 本地：不走外部往返，耗时不取决于别人的服务；
//   - 自省：答案是 Diana 自身的能力、身份、配置，而不是任务本身的进展。
//
// 快但会写的工具（改配置、发消息、戳一戳）不在此列——步数预算约束的是干活，不是延迟。
// 带 input 是因为同一个工具可能一半只读一半是动作（extension_access 的 list 与改档位）。
type IntrospectionTool interface {
	Tool
	Introspection(input map[string]any) bool
}

// ToolInputSchema optionally exposes the tool's JSON Schema to providers with
// native function calling. Existing tools remain compatible with a permissive
// object schema until they provide a strict schema.
type ToolInputSchema interface {
	Tool
	InputSchema() map[string]any
}

// StrictDecodingTool 让工具主动要求严格解码，即使它的 schema 声明了可选参数。
// 严格模式要求每个参数都出现并在不用时显式为 null，这个代价只值得付给「一旦
// 参数写错就要整轮重试」的工具。schema 的改写在 provider 边界完成，工具自身
// 保持一份可读的普通 schema。
type StrictDecodingTool interface {
	Tool
	PrefersStrictDecoding() bool
}

// ToolResultPartsTool lets a tool attach non-text evidence to the observation
// sent into the next model turn. The regular string result remains the source
// of truth for logs and models that do not support the extra content part.
type ToolResultPartsTool interface {
	Tool
	ToolResultParts(output string) []llm.ContentPart
}

type ToolCatalogItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// TerminalResultTool can finish the agent loop immediately after a successful
// tool call. It is intended for tools that already performed the requested
// action and return an authoritative user-facing acknowledgement.
type TerminalResultTool interface {
	Tool
	TerminalResult(output string) (string, bool)
}

// ExplicitUserRequestTool marks mutating tools that must be directly requested
// by the current user message. Tool output and skill/MCP instructions cannot
// grant this authorization to themselves.
type ExplicitUserRequestTool interface {
	Tool
	ExplicitUserRequestKind() string
}

// ExplicitUserRequestInputTool is the input-aware form used by tools that mix
// read operations with several independently authorized mutations.
type ExplicitUserRequestInputTool interface {
	Tool
	ExplicitUserRequestKind(input map[string]any) string
}

type closeableTool interface {
	Close() error
}

type ToolRegistry struct {
	mu                 sync.RWMutex
	tools              map[string]Tool
	order              []string
	closers            []closeableTool
	skills             []SkillMetadata
	skillsSet          bool
	builtinSkills      []SkillMetadata
	reservedSkillNames []string
	extensions         ExtensionCatalog
	parent             *ToolRegistry
	parentOnly         map[string]bool
	hidden             map[string]bool
	restricted         map[string]bool
	denied             map[string]bool
	extensionOverrides map[string]bool
	activeViews        int
	closeRequested     bool
	closed             bool
}

// NewDefaultToolRegistry 创建 Agent 默认工具注册表。
func NewDefaultToolRegistry(cfg Config) (*ToolRegistry, error) {
	cfg = cfg.WithDefaults()
	root, err := filepath.Abs(cfg.WorkDir)
	if err != nil {
		return nil, err
	}
	registry := NewToolRegistry()
	protected := agentProtectedFiles(cfg)
	// 默认工具都绑定到同一个绝对工作目录，后续 safePath 负责防逃逸校验；运行时自己的
	// 配置文件另外由 protected 拦掉，见 agentProtectedFiles。
	registry.Register(&ListFilesTool{root: root, limit: cfg.ListDirectoryLimit, protected: protected})
	registry.Register(&ReadFileTool{root: root, maxBytes: cfg.ReadFileMaxBytes, protected: protected})
	// 检索和按名字找文件与 read_file 同级：都只读，都锁在工作目录内。
	// 没有它们的话，模型定位一个文件只能靠 list_files 一层层翻或者猜路径。
	registry.Register(&GrepTool{root: root, maxBytes: cfg.ReadFileMaxBytes, protected: protected})
	registry.Register(&FindFilesTool{root: root, protected: protected})
	// 写入是单独一档：读错文件浪费一次调用，写错文件改的是磁盘。
	if cfg.FileWriteEnabled {
		registry.Register(&WriteFileTool{root: root, maxBytes: cfg.FileWriteMaxBytes, protected: protected})
		registry.Register(&EditFileTool{root: root, maxBytes: cfg.FileWriteMaxBytes, protected: protected})
	}
	if len(cfg.CommandAllowlist) > 0 {
		registry.Register(&RunCommandTool{
			root:           root,
			allowlist:      commandAllowlistSet(cfg.CommandAllowlist),
			protected:      protected,
			mcpConfigPath:  resolveMCPConfigPath(cfg),
			timeout:        time.Duration(cfg.CommandTimeoutMS) * time.Millisecond,
			maxBytes:       cfg.MaxToolOutputChars,
			sandboxMode:    cfg.CommandSandbox,
			sandbox:        detectCommandSandbox(),
			sandboxNetwork: cfg.CommandSandboxAllowNetwork,
		})
	}
	if !cfg.BrowserToolsDisabled {
		registry.RegisterBrowserTools(root, cfg)
	}
	registry.RegisterBrowserControlTools(root, cfg)
	return registry, nil
}

// NewAgentToolRegistry 创建包含本地工具、skills 工具和 MCP 工具的注册表。
func NewAgentToolRegistry(ctx context.Context, cfg Config) (*ToolRegistry, error) {
	cfg = cfg.WithDefaults()
	registry, err := NewDefaultToolRegistry(cfg)
	if err != nil {
		return nil, err
	}
	extensions, err := NewExtensionManager(ctx, cfg, registry)
	if err != nil {
		_ = registry.Close()
		return nil, err
	}
	registry.SetExtensionCatalog(extensions)
	registry.RegisterCloser(extensions)
	return registry, nil
}

// NewSharedExtensionRegistry 创建只含扩展（Skills 与 MCP）的共享底座。
//
// 请求视图查不到的工具会回落到底座里找，所以底座里不能有按机器人配置的本地工具
// （命令白名单、文件写入、浏览器）：否则一台机器人的 run_command 会被另一台借到。
// 本地工具和随事件变化的内置 Skill 都由 NewView 按本次请求的配置提供，底座只按
// ExtensionScope 共享，所有机器人共用同一套 MCP 进程和已装扩展。
func NewSharedExtensionRegistry(ctx context.Context, cfg Config) (*ToolRegistry, error) {
	cfg = cfg.ExtensionScope()
	registry := NewToolRegistry()
	extensions, err := NewExtensionManager(ctx, cfg, registry)
	if err != nil {
		_ = registry.Close()
		return nil, err
	}
	registry.SetExtensionCatalog(extensions)
	registry.RegisterCloser(extensions)
	return registry, nil
}

// NewView creates a request-scoped registry that inherits live extension tools
// from this registry without owning their MCP sessions. Local tools can still
// be added, filtered, and closed independently for one Agent run.
func (r *ToolRegistry) NewView(cfg Config) (*ToolRegistry, error) {
	cfg = cfg.WithDefaults()
	registry, err := NewDefaultToolRegistry(cfg)
	if err != nil {
		return nil, err
	}
	if r == nil {
		_ = registry.Close()
		return nil, errors.New("agent: parent tool registry is required")
	}
	r.mu.Lock()
	if r.closeRequested || r.closed {
		r.mu.Unlock()
		_ = registry.Close()
		return nil, errors.New("agent: tool registry is closing")
	}
	r.activeViews++
	r.mu.Unlock()
	registry.mu.Lock()
	registry.parent = r
	registry.extensions = &registryExtensionViewCatalog{
		parent:  r,
		builtin: append([]BuiltinExtension(nil), cfg.BuiltinExtensions...),
	}
	// 内置 Skill 随事件变化（比如平台接口只在开了的群里有），叠在底座的 Skills 之上，
	// 不进底座；skills.list/read 也换成读这份合并结果的版本。
	if builtin := normalizeBuiltinSkills(cfg.BuiltinSkills); len(builtin) > 0 {
		registry.builtinSkills = builtin
		registry.reservedSkillNames = append([]string(nil), cfg.ReservedSkillNames...)
	}
	registry.mu.Unlock()
	if len(registry.builtinSkills) > 0 {
		tools := newLiveSkillTools(registry.Skills)
		registry.Register(tools.Read)
	}
	registry.Register(NewExtensionsListTool(registry.extensions, cfg.ExtensionManagement))
	return registry, nil
}

type registryExtensionViewCatalog struct {
	parent  *ToolRegistry
	builtin []BuiltinExtension
}

func (c *registryExtensionViewCatalog) Extensions() []ExtensionState {
	states := c.parent.Extensions()
	out := make([]ExtensionState, 0, len(states)+len(c.builtin))
	for _, state := range states {
		if state.Kind != ExtensionKindBuiltin {
			out = append(out, state)
		}
	}
	for _, item := range normalizeBuiltinExtensions(c.builtin) {
		out = append(out, ExtensionState{
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
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// SetSkills 记录可用 skill 元数据，供 Runner 构造 skills 上下文。
func (r *ToolRegistry) SetSkills(skills []SkillMetadata) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.skills = append([]SkillMetadata(nil), skills...)
	r.skillsSet = true
}

// RegisterBuiltinSkills exposes only embedded, trusted instructions. It does
// not scan local skill roots or create MCP connections.
func (r *ToolRegistry) RegisterBuiltinSkills(skills []SkillMetadata) {
	r.RegisterScopedSkills(skills, nil, nil)
}

// RegisterScopedSkills 固定这份视图能看到的 skill：内置的那几份，加上显式放开的
// extra。设过之后不会再继承共享底座上的其他 skill，read_skill 也读不到它们。
func (r *ToolRegistry) RegisterScopedSkills(builtin, extra []SkillMetadata, reservedNames []string) {
	r.SetSkills(mergeBuiltinSkills(builtin, extra, reservedNames))
	tools := newLiveSkillTools(r.Skills)
	r.Register(tools.Read)
}

// Skills 返回当前注册表关联的 skills。
func (r *ToolRegistry) Skills() []SkillMetadata {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	skills := append([]SkillMetadata(nil), r.skills...)
	set := r.skillsSet
	parent := r.parent
	builtin := r.builtinSkills
	reserved := r.reservedSkillNames
	r.mu.RUnlock()
	if !set && parent != nil {
		if len(builtin) > 0 {
			return mergeBuiltinSkills(builtin, parent.Skills(), reserved)
		}
		return parent.Skills()
	}
	return skills
}

// SetExtensionCatalog attaches the live built-in/skill/MCP catalog used by the
// list_capabilities tool and by the Agent system prompt.
func (r *ToolRegistry) SetExtensionCatalog(catalog ExtensionCatalog) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.extensions = catalog
}

// Extensions returns a redacted snapshot of all capabilities visible in this
// registry. Runtime credentials and MCP environment values are never included.
func (r *ToolRegistry) Extensions() []ExtensionState {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	catalog := r.extensions
	parent := r.parent
	r.mu.RUnlock()
	if catalog != nil {
		return catalog.Extensions()
	}
	if parent != nil {
		return parent.Extensions()
	}
	return nil
}

// NewToolRegistry 创建工具注册表并登记初始工具。
func NewToolRegistry(tools ...Tool) *ToolRegistry {
	registry := &ToolRegistry{tools: map[string]Tool{}}
	for _, tool := range tools {
		registry.Register(tool)
	}
	return registry
}

// RegisterCloser 让注册表托管非工具资源的生命周期，例如 MCP 子进程。
func (r *ToolRegistry) RegisterCloser(closer closeableTool) {
	if closer == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closers = append(r.closers, closer)
}

// Register 将工具加入注册表并保持描述顺序稳定。
func (r *ToolRegistry) Register(tool Tool) {
	if tool == nil || strings.TrimSpace(tool.Name()) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closeRequested || r.closed {
		return
	}
	name := tool.Name()
	if _, exists := r.tools[name]; !exists {
		r.order = append(r.order, name)
		// 描述按名称排序，模型看到的工具列表稳定，测试输出也稳定。
		sort.Strings(r.order)
	}
	r.tools[name] = tool
	delete(r.hidden, name)
}

// RegisterBrowserTools 登记基于 Chrome DevTools Protocol 的浏览器工具。
func (r *ToolRegistry) RegisterBrowserTools(root string, cfg Config) {
	timeout := time.Duration(cfg.BrowserTimeoutMS) * time.Millisecond
	base := browserToolBase{
		root:     root,
		cdpURL:   cfg.BrowserCDPURL,
		builtin:  cfg.BuiltinBrowser,
		timeout:  timeout,
		maxChars: cfg.MaxToolOutputChars,
		session:  browserTabs.session(cfg.BrowserSessionKey),
		tabs:     browserTabs,
	}
	r.Register(&BrowserOpenTool{base: base})
	r.Register(&BrowserTextTool{base: base})
	r.Register(&BrowserClickTool{base: base})
	r.Register(&BrowserTypeTool{base: base})
	r.Register(&BrowserScreenshotTool{base: base})
	r.Register(&BrowserTabsTool{base: base})
	r.Register(&BrowserScrollTool{base: base})
	r.Register(&BrowserPressKeyTool{base: base})
	r.Register(&BrowserNavigateTool{base: base})
	r.Register(&BrowserSelectTool{base: base})
	r.Register(&BrowserWaitTool{base: base})
	r.Register(&BrowserEvalTool{base: base})
}

// InteractiveBrowserToolNames 是 RegisterBrowserTools 登记的全部工具，WebUI 和提示词
// 按这份名单判断「交互式浏览器在不在」，不再各抄一份。
var InteractiveBrowserToolNames = []string{
	"browser_open", "browser_text", "browser_click", "browser_type", "browser_screenshot",
	"browser_tabs", "browser_scroll", "browser_press_key", "browser_navigate",
	"browser_select", "browser_wait", "browser_eval",
}

// Get 按名称查找工具。
func (r *ToolRegistry) Get(name string) (Tool, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	tool, ok := r.tools[name]
	parent := r.parent
	allowed := r.parentOnly
	hidden := r.hidden[name]
	r.mu.RUnlock()
	if ok {
		if !r.extensionToolAllowed(tool) {
			return nil, false
		}
		return tool, true
	}
	if parent == nil || hidden || (allowed != nil && !allowed[name]) {
		return nil, false
	}
	tool, ok = parent.Get(name)
	if ok && !r.extensionToolAllowed(tool) {
		return nil, false
	}
	return tool, ok
}

// DenyTools 记下「这个名字本次会话没权限用」。
//
// 有些工具的权限门槛在注册之前就判完了——不够格就根本不构造这个工具，注册表自然
// 也不知道有过这个名字。于是模型问起来只会得到「不存在」，它照字面理解成拼错了，
// 换个名字接着猜，一路把工具预算耗光（线上真发生过：非主人在群里让机器人开 issue，
// github 工具因为没权限没注册，模型连猜四个名字直到额度用尽）。
//
// 这里只登记名字，不构造也不注册工具：能不能调用完全不受影响，变的只是取不到时
// 该说哪句话。
func (r *ToolRegistry) DenyTools(names ...string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			if r.denied == nil {
				r.denied = map[string]bool{}
			}
			r.denied[name] = true
		}
	}
}

// PolicyDenied 回答「这个名字是查无此工具，还是本次会话没权限用」。被身份白名单
// 摘掉、被机器人开关停用、调用方显式登记过没权限，或只存在于共享底座却不在白名单
// 里的工具都算后者：调用方据此给出的提示不一样，模型才不会对着同一个名字反复重试。
func (r *ToolRegistry) PolicyDenied(name string) bool {
	if r == nil || name == "" {
		return false
	}
	// 拿得到就不是权限问题：这个判断要能独立回答，不能依赖调用方先试过 Get。
	if _, ok := r.Get(name); ok {
		return false
	}
	r.mu.RLock()
	_, local := r.tools[name]
	restricted := r.restricted[name]
	hidden := r.hidden[name]
	denied := r.denied[name]
	allowed := cloneToolAllowlist(r.parentOnly)
	parent := r.parent
	r.mu.RUnlock()
	// 名字还在本地表里却取不出来，只可能是机器人级扩展开关把它关了。
	if local || restricted || hidden || denied {
		return true
	}
	if allowed == nil || allowed[name] {
		// 白名单没挡住的话，剩下的可能是机器人级扩展开关把这个 MCP 关了。
		if parent != nil {
			if tool, ok := parent.Get(name); ok && !r.extensionToolAllowed(tool) {
				return true
			}
		}
		return false
	}
	// MCP 工具名由本进程按固定前缀生成，白名单外的这类名字就是权限问题，
	// 不必为了区分而把整套共享扩展拉起来。
	if strings.HasPrefix(name, mcpToolNamePrefix) {
		return true
	}
	if parent == nil {
		return false
	}
	_, existsInBase := parent.Get(name)
	return existsInBase
}

// Retain removes every tool not present in allowed. A nil allowlist keeps all
// tools and is used only for the bot Owner's unrestricted registry.
func (r *ToolRegistry) Retain(allowed map[string]bool) {
	if r == nil || allowed == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	order := make([]string, 0, len(r.order))
	for _, name := range r.order {
		if allowed[name] {
			order = append(order, name)
			continue
		}
		if r.restricted == nil {
			r.restricted = map[string]bool{}
		}
		r.restricted[name] = true
		delete(r.tools, name)
	}
	r.order = order
	r.parentOnly = cloneToolAllowlist(allowed)
	if !allowed["read_skill"] {
		r.skills = nil
		r.skillsSet = true
	}
}

// Remove deletes one tool while preserving the stable order of the remaining
// registry entries.
func (r *ToolRegistry) Remove(name string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	name = strings.TrimSpace(name)
	if r.hidden == nil {
		r.hidden = map[string]bool{}
	}
	r.hidden[name] = true
	delete(r.tools, name)
	order := r.order[:0]
	for _, current := range r.order {
		if current != name {
			order = append(order, current)
		}
	}
	r.order = order
	if name == "read_skill" {
		r.skills = nil
		r.skillsSet = true
	}
}

func (r *ToolRegistry) Len() int {
	if r == nil {
		return 0
	}
	return len(r.Names())
}

// Names returns the registered tool names in the same stable order used by the
// Agent prompt.
func (r *ToolRegistry) Names() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	local := append([]string(nil), r.order...)
	parent := r.parent
	allowed := cloneToolAllowlist(r.parentOnly)
	hidden := cloneToolAllowlist(r.hidden)
	r.mu.RUnlock()
	if parent == nil {
		return r.filterExtensionToolNames(local)
	}
	seen := make(map[string]bool, len(local))
	for _, name := range local {
		seen[name] = true
	}
	for _, name := range parent.Names() {
		if seen[name] || hidden[name] || (allowed != nil && !allowed[name]) {
			continue
		}
		local = append(local, name)
		seen[name] = true
	}
	sort.Strings(local)
	return r.filterExtensionToolNames(local)
}

// Catalog returns a compact semantic routing catalog. Input schemas stay out of
// the router request and are shown only to the answering Agent after selection.
func (r *ToolRegistry) Catalog(descriptionRunes int) []ToolCatalogItem {
	if r == nil {
		return nil
	}
	if descriptionRunes <= 0 {
		descriptionRunes = 180
	}
	names := r.Names()
	items := make([]ToolCatalogItem, 0, len(names))
	for _, name := range names {
		tool, ok := r.Get(name)
		if !ok {
			continue
		}
		description := strings.TrimSpace(tool.Description())
		if index := strings.Index(strings.ToLower(description), "input:"); index >= 0 {
			description = strings.TrimSpace(description[:index])
		}
		description = strings.Join(strings.Fields(description), " ")
		items = append(items, ToolCatalogItem{
			Name:        name,
			Description: truncateRunes(description, descriptionRunes),
		})
	}
	return items
}

// Descriptions 返回给模型看的工具描述列表。
func (r *ToolRegistry) Descriptions() string {
	if r == nil {
		return "无可用工具。"
	}
	names := r.Names()
	if len(names) == 0 {
		return "无可用工具。"
	}
	var builder strings.Builder
	for _, name := range names {
		tool, ok := r.Get(name)
		if !ok {
			continue
		}
		builder.WriteString("- ")
		builder.WriteString(tool.Name())
		builder.WriteString(": ")
		builder.WriteString(compactToolDescription(tool.Description(), ToolDescriptionBudget))
		builder.WriteByte('\n')
	}
	return strings.TrimSpace(builder.String())
}

// Definitions returns the native function declarations sent on every model
// turn. The order matches Names so requests remain deterministic.
func (r *ToolRegistry) Definitions() []llm.ToolDefinition {
	if r == nil {
		return nil
	}
	definitions := make([]llm.ToolDefinition, 0, r.Len())
	for _, name := range r.Names() {
		tool, ok := r.Get(name)
		if !ok {
			continue
		}
		definitions = append(definitions, ToolDefinitionFor(tool))
	}
	return definitions
}

// ToolDefinitionFor 渲染一个工具的原生声明。档位界面按它估算常驻的开销，估的就是
// 请求里真正发出去的那份，不是另算一套。
func ToolDefinitionFor(tool Tool) llm.ToolDefinition {
	schema := map[string]any{"type": "object", "additionalProperties": true}
	strict := false
	if typed, ok := tool.(ToolInputSchema); ok {
		if provided := typed.InputSchema(); provided != nil {
			schema = provided
			strict = schemaAllowsStrictMode(provided)
		}
	}
	if typed, ok := tool.(StrictDecodingTool); ok && typed.PrefersStrictDecoding() {
		strict = true
	}
	return llm.ToolDefinition{Name: tool.Name(), Description: tool.Description(), Parameters: schema, Strict: strict}
}

// ResidencyCost 报告一个工具两档各占多少 token：常驻是整份原生声明，按需是目录里
// 的那一行。界面拿这两个数字告诉用户改档位到底贵多少、省多少。
func ResidencyCost(tool Tool) (resident, deferred int64) {
	if tool == nil {
		return 0, 0
	}
	return llm.EstimateToolDefinitionTokens(ToolDefinitionFor(tool)), llm.EstimateTextTokens(deferredCatalogLine(tool))
}

// deferredCatalogLine 是按需目录里的一行，catalog 和开销估算共用同一份格式。
func deferredCatalogLine(tool Tool) string {
	return "- " + tool.Name() + ": " + compactToolDescription(tool.Description(), SystemPromptToolDescriptionBudget) + "\n"
}

// SystemPromptCatalog 为系统提示词渲染一份每行一个工具的目录。每轮请求都会带上
// 原生工具定义，那里已经有权威描述和 JSON Schema，在提示词里再抄一遍全文只是
// 在同一个请求里重复同样的文本。短目录仍然让不支持 function calling 的供应商
// 能用兼容 JSON 协议选对工具。
func (r *ToolRegistry) SystemPromptCatalog() string {
	if r == nil {
		return "无可用工具。"
	}
	names := r.Names()
	if len(names) == 0 {
		return "无可用工具。"
	}
	var builder strings.Builder
	for _, name := range names {
		tool, ok := r.Get(name)
		if !ok {
			continue
		}
		builder.WriteString("- ")
		builder.WriteString(tool.Name())
		builder.WriteString(": ")
		builder.WriteString(compactToolDescription(tool.Description(), SystemPromptToolDescriptionBudget))
		builder.WriteByte('\n')
	}
	return strings.TrimSpace(builder.String())
}

// SystemPromptToolDescriptionBudget 把系统提示词里的目录压到大约一行一个工具。
// 完整的行为约束留在发给模型的工具定义里。
const SystemPromptToolDescriptionBudget = 120

// ToolDescriptionBudget 是工具清单里单个描述的字数上限。超出会被截断，且
// compactToolDescription 优先保留 input: 之后的参数示例——被砍掉的恰好是开头
// 那句「什么时候该用我」。参数契约应当放进 InputSchema，描述留在预算内。
const ToolDescriptionBudget = 720

// schemaAllowsStrictMode 报告这份 schema 能否安全地声明 strict。严格模式要求禁止
// 额外字段并且每个属性都出现在 required 里；不满足时照样把 schema 发出去（模型
// 仍然能看到参数名、类型和取值范围），只是不声明 strict——声明了 provider 会直接
// 拒绝整个请求，比没有 schema 更糟。
func schemaAllowsStrictMode(schema map[string]any) bool {
	if allowExtra, ok := schema["additionalProperties"].(bool); !ok || allowExtra {
		return false
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) == 0 {
		return false
	}
	required := map[string]bool{}
	switch values := schema["required"].(type) {
	case []string:
		for _, name := range values {
			required[name] = true
		}
	case []any:
		for _, value := range values {
			if name, ok := value.(string); ok {
				required[name] = true
			}
		}
	default:
		return false
	}
	if len(required) != len(properties) {
		return false
	}
	for name := range properties {
		if !required[name] {
			return false
		}
	}
	return true
}

// CompactToolDescription 把工具描述压成目录里的一行，界面和提示词共用同一份压法。
func CompactToolDescription(description string, maxRunes int) string {
	return compactToolDescription(description, maxRunes)
}

func compactToolDescription(description string, maxRunes int) string {
	description = strings.Join(strings.Fields(description), " ")
	if maxRunes <= 0 || len([]rune(description)) <= maxRunes {
		return description
	}
	inputIndex := strings.Index(strings.ToLower(description), "input:")
	if inputIndex < 0 {
		return truncateToolDescription(description, maxRunes)
	}
	purpose := strings.TrimSpace(description[:inputIndex])
	schema := strings.TrimSpace(description[inputIndex:])
	schemaRunes := []rune(schema)
	if len(schemaRunes) >= maxRunes-4 {
		return truncateToolDescription(schema, maxRunes)
	}
	purposeBudget := maxRunes - len(schemaRunes) - 4
	purpose = truncateToolDescription(purpose, purposeBudget)
	return strings.TrimSpace(purpose) + " " + schema
}

func truncateToolDescription(value string, maxRunes int) string {
	runes := []rune(strings.TrimSpace(value))
	if maxRunes <= 0 || len(runes) <= maxRunes {
		return string(runes)
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return strings.TrimSpace(string(runes[:maxRunes-3])) + "..."
}

// Close 释放工具持有的外部资源。
func (r *ToolRegistry) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed || r.closeRequested {
		r.mu.Unlock()
		return nil
	}
	r.closeRequested = true
	if r.activeViews > 0 {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	closers := append([]closeableTool(nil), r.closers...)
	tools := make([]Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		tools = append(tools, tool)
	}
	parent := r.parent
	r.parent = nil
	r.mu.Unlock()
	err := closeToolRegistryResources(closers, tools)
	if parent != nil {
		err = errors.Join(err, parent.releaseView())
	}
	return err
}

func (r *ToolRegistry) releaseView() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.activeViews > 0 {
		r.activeViews--
	}
	if r.activeViews > 0 || !r.closeRequested || r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	closers := append([]closeableTool(nil), r.closers...)
	tools := make([]Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		tools = append(tools, tool)
	}
	parent := r.parent
	r.parent = nil
	r.mu.Unlock()
	err := closeToolRegistryResources(closers, tools)
	if parent != nil {
		err = errors.Join(err, parent.releaseView())
	}
	return err
}

func closeToolRegistryResources(closers []closeableTool, tools []Tool) error {
	var parts []string
	seen := map[closeableTool]bool{}
	for _, closer := range closers {
		if seen[closer] {
			continue
		}
		seen[closer] = true
		if err := closer.Close(); err != nil {
			parts = append(parts, err.Error())
		}
	}
	for _, tool := range tools {
		closer, ok := tool.(closeableTool)
		if !ok || seen[closer] {
			continue
		}
		seen[closer] = true
		if err := closer.Close(); err != nil {
			parts = append(parts, err.Error())
		}
	}
	if len(parts) > 0 {
		return errors.New(strings.Join(parts, "; "))
	}
	return nil
}

func cloneToolAllowlist(values map[string]bool) map[string]bool {
	if values == nil {
		return nil
	}
	cloned := make(map[string]bool, len(values))
	for name, allowed := range values {
		cloned[name] = allowed
	}
	return cloned
}

type ListFilesTool struct {
	root      string
	limit     int
	protected protectedFiles
}

// Name 返回列目录工具名称。
func (t *ListFilesTool) Name() string {
	return "list_files"
}

// Description 返回列目录工具说明。
func (t *ListFilesTool) Description() string {
	return `列出 Agent 工作目录内的文件。`
}

func (t *ListFilesTool) InputSchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{
		"path": toolStringParam("工作目录内的相对目录，省略时列出根目录"),
	})
}

// Run 列出 Agent 工作目录内的文件。
func (t *ListFilesTool) Run(_ context.Context, input map[string]any) (string, error) {
	rel := stringFromInput(input, "path")
	if rel == "" {
		rel = "."
	}
	path, err := safePath(t.root, rel)
	if err != nil {
		return "", err
	}
	if t.protected.blocked(path) {
		return "", errProtectedFile(rel)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}
	limit := t.limit
	if limit <= 0 {
		limit = DefaultListDirectoryLimit
	}
	type entry struct {
		Name string `json:"name"`
		Type string `json:"type"`
		Size int64  `json:"size,omitempty"`
	}
	out := make([]entry, 0, min(len(entries), limit))
	hidden := 0
	for i, item := range entries {
		if i >= limit {
			break
		}
		if t.protected.blocked(filepath.Join(path, item.Name())) {
			hidden++
			continue
		}
		itemType := "file"
		if item.IsDir() {
			itemType = "directory"
		}
		row := entry{Name: item.Name(), Type: itemType}
		if info, err := item.Info(); err == nil && !item.IsDir() {
			row.Size = info.Size()
		}
		out = append(out, row)
	}
	body, err := json.MarshalIndent(map[string]any{
		"path":    rel,
		"entries": out,
		// truncated 告诉模型目录没列完，必要时可以继续读更具体路径。
		"truncated": len(entries) > limit,
		// 运行时配置不出现在列表里，但也不假装目录是干净的：模型该知道有东西被挡了，
		// 免得反复去猜文件名。
		"protected_hidden": hidden,
	}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

type ReadFileTool struct {
	root      string
	maxBytes  int
	protected protectedFiles
}

type RunCommandTool struct {
	root      string
	allowlist map[string]bool
	timeout   time.Duration
	maxBytes  int
	// protected 是凭据配置文件。文件工具按它拒绝读写，沙盒按它把这些路径挡在
	// 命令的视野之外——白名单里配了 cat、grep 时，那是唯一还拦得住的一层。
	protected protectedFiles
	// mcpConfigPath 用来找出 MCP 配置用 ${NAME} 引用的进程环境变量，命令拿不到它们，
	// 见 commandEnvironment。
	mcpConfigPath string
	// sandboxMode 见 CommandSandbox* 常量；sandbox 是当前平台探测到的实现。
	sandboxMode    string
	sandbox        commandSandbox
	sandboxNetwork bool
}

// Name 返回命令执行工具名称。
func (t *RunCommandTool) Name() string {
	return "run_command"
}

// Description 返回命令执行工具说明。
func (t *RunCommandTool) Description() string {
	return `在 Agent 工作目录内执行短时本地命令，不经过 shell。不要用于网页搜索、计时、提醒、周期任务、sleep 或后台驻留；这些场景必须使用对应的专用工具。实时网页搜索必须优先使用 web_search。`
}

func (t *RunCommandTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"command"}, map[string]any{
		"command":    toolStringParam("命令名，必须在允许列表内"),
		"args":       toolStringArrayParam("命令参数，按顺序传入，不经过 shell 解析"),
		"cwd":        toolStringParam("工作目录内的相对执行目录，可选"),
		"timeout_ms": toolIntParam("超时毫秒数，可选"),
	})
}

// Run 在 Agent 工作目录内执行白名单命令。
func (t *RunCommandTool) Run(ctx context.Context, input map[string]any) (string, error) {
	command := stringFromInput(input, "command")
	if command == "" {
		return "", errors.New("command is required")
	}
	if strings.ContainsAny(command, `/\`) {
		return "", errors.New("command must be a binary name, not a path")
	}
	if !t.commandAllowed(command) {
		return "", fmt.Errorf("command %q is not allowed", command)
	}
	cwd, err := safePath(t.root, stringFromInput(input, "cwd"))
	if err != nil {
		return "", err
	}
	timeout := time.Duration(intFromInput(input, "timeout_ms", int(t.timeout.Milliseconds()))) * time.Millisecond
	if timeout <= 0 || timeout > t.timeout {
		timeout = t.timeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := stringSliceFromInput(input, "args")
	cmd, sandboxKind, err := t.commandFor(runCtx, command, args)
	if err != nil {
		return "", err
	}
	cmd.Dir = cwd
	cmd.Env = t.commandEnvironment()
	commandOutput, err := os.CreateTemp("", "diana-agent-command-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(commandOutput.Name()) }()
	defer func() { _ = commandOutput.Close() }()
	cmd.Stdout = commandOutput
	cmd.Stderr = commandOutput
	start := time.Now()
	err = cmd.Run()
	duration := time.Since(start)
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else if runCtx.Err() == context.DeadlineExceeded {
			exitCode = -1
		} else {
			return "", err
		}
	}
	output, truncated, readErr := readCommandOutput(commandOutput, t.maxBytes, exitCode != 0)
	if readErr != nil {
		return "", readErr
	}
	result := map[string]any{
		"command":     command,
		"args":        args,
		"cwd":         relPathForOutput(t.root, cwd),
		"exit_code":   exitCode,
		"timed_out":   runCtx.Err() == context.DeadlineExceeded,
		"duration_ms": duration.Milliseconds(),
		"truncated":   truncated,
		"output":      output,
	}
	// 让「这次到底有没有被隔离」出现在结果里：排查写入失败或网络不通时，
	// 第一个要确认的就是它。
	if sandboxKind != "" {
		result["sandbox"] = sandboxKind
		result["sandbox_network"] = t.sandboxNetwork
	} else {
		result["sandbox"] = "none"
	}
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	if exitCode != 0 {
		return "", errors.New(string(body))
	}
	return string(body), nil
}

func readCommandOutput(file *os.File, maxBytes int, complete bool) (string, bool, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", false, err
	}
	if complete {
		data, err := io.ReadAll(file)
		return string(data), false, err
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxToolOutputChars
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return "", false, err
	}
	truncated := len(data) > maxBytes
	if truncated {
		data = data[:maxBytes]
	}
	return string(data), truncated, nil
}

// commandFor 按沙盒模式决定这条命令怎么起。require 模式下没有可用沙盒就直接拒绝，
// 不能退回裸执行——那正是这个模式要防的事。
func (t *RunCommandTool) commandFor(ctx context.Context, command string, args []string) (*exec.Cmd, string, error) {
	mode := normalizeCommandSandboxMode(t.sandboxMode)
	if mode == CommandSandboxOff {
		return procgroup.CommandContext(ctx, command, args...), "", nil
	}
	if !t.sandbox.available() {
		if mode == CommandSandboxRequire {
			return nil, "", fmt.Errorf("command sandbox is required but unavailable on this host: install bubblewrap (Linux) or run on macOS with sandbox-exec")
		}
		return procgroup.CommandContext(ctx, command, args...), "", nil
	}
	return t.sandbox.wrap(ctx, t.root, t.sandboxNetwork, t.protected.existingPaths(), command, args), t.sandbox.kind, nil
}

// commandEnvironment 是命令继承的环境：Diana 自己的进程环境，去掉凭据。
//
//   - MCP 配置里用 ${NAME} 引用的那些变量：主人常把令牌放在进程环境里、配置只写引用。
//   - 名字像凭据的变量（TAVILY_API_KEY、GITHUB_TOKEN、DIANA_BILI_SESSDATA、编码代理
//     的 api_key_env 之类）和值里嵌着 userinfo 的地址（带账号密码的代理）。
//
// 白名单里有 env、printenv 时这些就是令牌原文——沙箱挡的是文件，挡不住继承下来的
// 环境变量。白名单里的命令是给模型查东西用的，不需要主人的凭据。
func (t *RunCommandTool) commandEnvironment() []string {
	return commandEnvironmentFor(os.Environ(), t.mcpConfigPath)
}

func commandEnvironmentFor(environ []string, mcpConfigPath string) []string {
	drop := secretmask.SensitiveEnvironmentNames(environ)
	for _, item := range environ {
		name, value, _ := strings.Cut(item, "=")
		if secretmask.URLs(value) != value {
			drop[name] = true
		}
	}
	if strings.TrimSpace(mcpConfigPath) != "" {
		// 配置读不出来时只少摘这一类：这时也不知道该摘哪些，拦下命令只会让人摸不着头脑。
		if servers, err := loadMCPServers(mcpConfigPath); err == nil {
			for name := range mcpReferencedEnvironment(servers) {
				drop[name] = true
			}
		}
	}
	return environmentWithout(environ, drop)
}

func (t *RunCommandTool) commandAllowed(command string) bool {
	if t.allowlist["*"] {
		return true
	}
	return t.allowlist[command]
}

func commandAllowlistSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out[value] = true
		}
	}
	return out
}

// Name 返回读文件工具名称。
func (t *ReadFileTool) Name() string {
	return "read_file"
}

// Description 返回读文件工具说明。
func (t *ReadFileTool) Description() string {
	return `按行读取 Agent 工作目录内的文本文件。默认从第 1 行起读 ` + fmt.Sprint(defaultReadFileLines) +
		` 行；文件更长时结果里会写明总行数和下一段的 offset，用 offset 继续读，不要指望一次拿到整个文件。`
}

func (t *ReadFileTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"path"}, map[string]any{
		"path":      toolStringParam("工作目录内的相对文件路径"),
		"offset":    toolIntParam("从第几行开始读，1 表示文件开头，可选"),
		"limit":     toolIntParam("最多读多少行，可选"),
		"max_bytes": toolIntParam("最大读取字节数，可选"),
	})
}

// Run 按行读取 Agent 工作目录内的文本文件。
//
// 以前只有 max_bytes、永远从头读：runner 又会把工具结果统一截到 MaxToolOutputChars，
// 于是一个几千行的文件，模型只看得到开头那几百行，剩下的再也够不着——既浪费了上下文
// 又没读到东西。改成 offset/limit 的分段读，并在结果里写明总行数和下一段从哪开始。
//
// 正文不加行号：Diana 的 Agent 没有按行改文件的工具，行号只会白占预算，
// 位置信息放表头一行就够，模型据此算下一个 offset。
func (t *ReadFileTool) Run(_ context.Context, input map[string]any) (string, error) {
	rel := stringFromInput(input, "path")
	if rel == "" {
		return "", errors.New("path is required")
	}
	path, err := safePath(t.root, rel)
	if err != nil {
		return "", err
	}
	if t.protected.blocked(path) {
		return "", errProtectedFile(rel)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory", rel)
	}
	maxBytes := intFromInput(input, "max_bytes", t.maxBytes)
	if maxBytes <= 0 || maxBytes > t.maxBytes {
		// 用户输入只能缩小读取范围，不能突破工具注册时的最大字节限制。
		maxBytes = t.maxBytes
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := splitFileLines(string(data))

	offset := intFromInput(input, "offset", 1)
	if offset < 1 {
		offset = 1
	}
	limit := intFromInput(input, "limit", defaultReadFileLines)
	if limit <= 0 || limit > maxReadFileLines {
		limit = defaultReadFileLines
	}
	if offset > len(lines) {
		return fmt.Sprintf("%s 共 %d 行，offset=%d 已经越过文件末尾。", rel, len(lines), offset), nil
	}
	end := min(offset-1+limit, len(lines))
	body := strings.Join(lines[offset-1:end], "\n")
	// 字节上限仍然生效，但它现在是兜底而不是主要手段：一行特别长的文件不该把预算吃光。
	byteTruncated := false
	if len(body) > maxBytes {
		body = string([]rune(body)[:len([]rune(body))*maxBytes/len(body)])
		byteTruncated = true
	}

	var out strings.Builder
	fmt.Fprintf(&out, "%s 第 %d-%d 行（共 %d 行）\n", rel, offset, end, len(lines))
	if end < len(lines) {
		fmt.Fprintf(&out, "还有 %d 行未读，用 offset=%d 继续。\n", len(lines)-end, end+1)
	}
	if byteTruncated {
		out.WriteString("这一段超过字节上限，已在中途截断。\n")
	}
	out.WriteString("\n")
	out.WriteString(body)
	return out.String(), nil
}

// splitFileLines 按行切分，并去掉结尾空行带来的那一条空记录，
// 免得「共 N 行」比编辑器里看到的多一行。
func splitFileLines(content string) []string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// safePath 将相对路径限制在 Agent 工作目录内。
func safePath(root, rel string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("agent workdir is empty")
	}
	if strings.TrimSpace(rel) == "" {
		rel = "."
	}
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(rel) {
		return "", errors.New("absolute paths are not allowed")
	}
	candidate, err := filepath.Abs(filepath.Join(cleanRoot, filepath.Clean(rel)))
	if err != nil {
		return "", err
	}
	relation, err := filepath.Rel(cleanRoot, candidate)
	if err != nil {
		return "", err
	}
	if relation == ".." || strings.HasPrefix(relation, ".."+string(filepath.Separator)) {
		// filepath.Clean 后再 Rel 校验，阻止 ../ 逃出 Agent 工作目录。
		return "", errors.New("path escapes agent workdir")
	}
	resolvedRoot, err := filepath.EvalSymlinks(cleanRoot)
	if err != nil {
		return "", err
	}
	resolvedCandidate, err := evalSymlinksAllowMissing(candidate)
	if err != nil {
		return "", err
	}
	relation, err = filepath.Rel(resolvedRoot, resolvedCandidate)
	if err != nil {
		return "", err
	}
	if relation == ".." || strings.HasPrefix(relation, ".."+string(filepath.Separator)) {
		return "", errors.New("path resolves outside agent workdir")
	}
	return candidate, nil
}

func evalSymlinksAllowMissing(path string) (string, error) {
	current := path
	missing := make([]string, 0, 4)
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

// stringFromInput 从工具输入中读取字符串字段。
func stringFromInput(input map[string]any, key string) string {
	if input == nil {
		return ""
	}
	value, _ := input[key].(string)
	return strings.TrimSpace(value)
}

// rawStringFromInput 从工具输入中读取字符串字段，不裁剪空白。
func rawStringFromInput(input map[string]any, key string) string {
	if input == nil {
		return ""
	}
	value, _ := input[key].(string)
	return value
}

// stringSliceFromInput 从工具输入中读取字符串数组字段。
func stringSliceFromInput(input map[string]any, key string) []string {
	if input == nil {
		return nil
	}
	switch values := input[key].(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			text, ok := value.(string)
			if ok {
				out = append(out, text)
			}
		}
		return out
	case string:
		if strings.TrimSpace(values) == "" {
			return nil
		}
		return strings.Fields(values)
	default:
		return nil
	}
}

// boolFromInput 从工具输入中读取布尔字段。
func boolFromInput(input map[string]any, key string, fallback bool) bool {
	if input == nil {
		return fallback
	}
	switch value := input[key].(type) {
	case bool:
		return value
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return fallback
}

// intFromInput 从工具输入中读取整数字段。
func intFromInput(input map[string]any, key string, fallback int) int {
	if input == nil {
		return fallback
	}
	// JSON 反序列化后数字常见为 float64/json.Number，工具参数统一兼容这些类型。
	switch value := input[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		if parsed, err := value.Int64(); err == nil {
			return int(parsed)
		}
	}
	return fallback
}

func relPathForOutput(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return "."
	}
	return filepath.ToSlash(rel)
}

// protectedFiles 是运行时自己的配置文件集合：MCP 配置里存着访问令牌，扩展覆盖文件
// 记着每台机器人的开关。它们碰巧就落在 Agent 工作目录里（`MCPConfigPath` 默认就是
// `<工作目录>/.mcp.json`），而文件工具只拦「不许走出工作目录」，不看读的是什么——
// 于是一句「读一下 .mcp.json」就能把令牌原文打进聊天记录。
//
// 这些文件是给运行时读的，不是给模型读的：要看配置去 WebUI，要用 MCP 由运行时在本地
// 拼请求。所以按路径整个拦掉，读、搜、写都不放行，列目录里也不出现。
//
// 编码代理的登录态也在这里：托管安装的 Claude Code / Codex 放在工作目录下的
// coding-runtime 里，ChatGPT 设备登录拿到的 auth.json、CLI 自己的配置目录（Claude
// Code 的 .credentials.json、Codex 的 auth.json 和会话记录）都在其中。文件名由 CLI
// 决定、会随版本变，所以这两处按目录整个挡：目录下面的任何文件都不给读写。
type protectedFiles struct {
	files map[string]bool
	// dirs 是整个挡掉的目录，绝对路径，同时收了解析软链接之后的真实路径。
	dirs []string
}

// CodingRuntimeDirName 是编码代理托管运行时在工作目录下的目录名。assistant 包按它
// 安装 CLI、存登录态，这里按它挡凭据目录，两边必须是同一个名字。
const CodingRuntimeDirName = "coding-runtime"

// codingRuntimeCredentialDirs 是 coding-runtime 下存登录态的目录：auth 放设备登录
// 拿到的令牌，state 是交给 CLI 当 CLAUDE_CONFIG_DIR / CODEX_HOME 的配置目录。
// 同级的安装目录和 npm 缓存里没有凭据，不挡。
var codingRuntimeCredentialDirs = []string{"auth", "state"}

func agentProtectedFiles(cfg Config) protectedFiles {
	protected := protectedFiles{files: map[string]bool{}}
	for _, path := range []string{
		resolveMCPConfigPath(cfg),
		// 配置改指到工作目录外面时，目录里可能还躺着一份旧的默认配置，里面的令牌
		// 一样是真的。
		filepath.Join(cfg.WorkDir, defaultMCPConfigFileName),
		extensionOverridePath(cfg.WorkDir),
		extensionAudiencePath(cfg.WorkDir),
		filepath.Join(cfg.WorkDir, extensionPathsFileName),
	} {
		protected.add(path, false)
	}
	if strings.TrimSpace(cfg.WorkDir) != "" {
		for _, name := range codingRuntimeCredentialDirs {
			protected.add(filepath.Join(cfg.WorkDir, CodingRuntimeDirName, name), true)
		}
	}
	runtimeSecretPaths.RLock()
	for path, dir := range runtimeSecretPaths.paths {
		protected.add(path, dir)
	}
	runtimeSecretPaths.RUnlock()
	return protected
}

// runtimeSecretPaths 是运行时登记的其他凭据位置：config.yaml（管理员密码、首启播种
// 的 API Key）、SQLite 数据库（全部插件凭据和 LLM 密钥）、日志（首启生成的管理员
// 密码只打印这一次）、内置浏览器的 profile（各站点登录态）、编码代理的登录目录。
// 它们大多在工作目录外面，文件工具本来就够不着；拦的是 run_command——沙箱只限制
// 写入，白名单里有 cat、strings 时一句 `cat ../diana.db` 就把所有凭据打进聊天记录。
var runtimeSecretPaths = struct {
	sync.RWMutex
	paths map[string]bool // 值为 true 表示整个目录
}{paths: map[string]bool{}}

// ProtectRuntimeFiles 登记几份凭据文件，文件工具和命令沙箱都不放行读取。
func ProtectRuntimeFiles(paths ...string) { protectRuntimePaths(false, paths) }

// ProtectRuntimeDirs 登记几个凭据目录，目录下的一切同样不放行。
func ProtectRuntimeDirs(paths ...string) { protectRuntimePaths(true, paths) }

func protectRuntimePaths(dir bool, paths []string) {
	runtimeSecretPaths.Lock()
	defer runtimeSecretPaths.Unlock()
	for _, path := range paths {
		if path = strings.TrimSpace(path); path != "" {
			runtimeSecretPaths.paths[path] = dir
		}
	}
}

// add 登记一个路径：dir 为 true 时整个目录连同下面的一切都挡。
func (p *protectedFiles) add(path string, dir bool) {
	for _, form := range protectedPathForms(path) {
		if !dir {
			p.files[form] = true
			continue
		}
		form = strings.TrimRight(form, string(filepath.Separator))
		if form != "" && !slices.Contains(p.dirs, form) {
			p.dirs = append(p.dirs, form)
		}
	}
}

// protectedPathForms 返回一个路径的绝对形式和解析软链接之后的真实形式：safePath 只
// 保证解析后仍在工作目录内，没说不能指向凭据；macOS 上临时目录本身就是软链接，沙盒
// 按真实路径匹配。
func protectedPathForms(path string) []string {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil
	}
	out := []string{abs}
	if resolved, err := evalSymlinksAllowMissing(abs); err == nil && resolved != abs {
		out = append(out, resolved)
	}
	return out
}

// WorkspaceFileProtected 报告工作目录下的相对路径 rel 是不是运行时自己的凭据配置。
// 给文件工具之外、同样按工作目录读文件的入口用（发附件、看图）：它们以前不看这份
// 名单，一句「把 .mcp.json 当文件发给我」就把令牌原文发进了聊天。路径不合法时
// 返回 false，交给调用方自己的路径校验去报错。
func WorkspaceFileProtected(cfg Config, rel string) bool {
	path, err := safePath(cfg.WorkDir, rel)
	if err != nil {
		return false
	}
	return agentProtectedFiles(cfg).blocked(path)
}

// blocked 判断这个路径是不是运行时凭据配置，或者落在整个挡掉的目录里（含目录本身）。
// path 必须是已经过 safePath 的绝对路径。
func (p protectedFiles) blocked(path string) bool {
	if len(p.files) == 0 && len(p.dirs) == 0 {
		return false
	}
	if p.matches(path) {
		return true
	}
	resolved, err := evalSymlinksAllowMissing(path)
	return err == nil && p.matches(resolved)
}

func (p protectedFiles) matches(path string) bool {
	if p.files[path] {
		return true
	}
	for _, dir := range p.dirs {
		if path == dir || strings.HasPrefix(path, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// existingPaths 返回当前真实存在的凭据文件和凭据目录，排序后给沙盒用。不存在的路径
// 不能交给 bubblewrap：绑定目标不存在会让整条命令起不来，而「配置还没生成」是常态。
// 目录只认显式登记成目录的那些。
func (p protectedFiles) existingPaths() []string {
	if len(p.files) == 0 && len(p.dirs) == 0 {
		return nil
	}
	out := make([]string, 0, len(p.files)+len(p.dirs))
	for path := range p.files {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			out = append(out, path)
		}
	}
	for _, dir := range p.dirs {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			out = append(out, dir)
		}
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

// errProtectedFile 的措辞要让模型能如实转述：这不是「文件不存在」，也不是权限没配好。
func errProtectedFile(rel string) error {
	return fmt.Errorf("%s 是 Diana 的运行时配置或凭据存储，里面可能有 MCP、编码代理的访问令牌、密钥或登录态，工具不提供读写；要查看或修改请在 WebUI 的扩展页或编码代理设置里操作", rel)
}
