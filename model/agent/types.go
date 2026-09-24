// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

const (
	DefaultMaxSteps           = 12
	DefaultMaxToolOutputChars = 8000
	DefaultReadFileMaxBytes   = 64 * 1024
	// DefaultFileWriteMaxBytes 是单次写入的默认上限。比读的上限大一些：模型生成
	// 一个完整文件时经常一次写下去，卡太死会让它退化成反复追加。
	DefaultFileWriteMaxBytes = 256 * 1024
	// 读文件默认一次多少行。工具结果统一被截到 MaxToolOutputChars，一次读太多
	// 只会在截断处白白丢掉，不如让模型按需要翻页。
	defaultReadFileLines      = 200
	maxReadFileLines          = 2000
	DefaultListDirectoryLimit = 200
	DefaultSkillsListBudget   = 8000
	// ResidentSkillBodyBudget 是常驻 skill 正文在一次请求里的总字符上限。超出的那几个
	// 只留目录行,退回 read_skill,不会把整轮上下文撑爆。
	ResidentSkillBodyBudget = 24000
	// DefaultSkillTriggerScanDepth 是关键词扫描回看的用户消息条数。只看最近几条：
	// 很久以前提过一次的词不该让那份正文从此每轮都在。
	DefaultSkillTriggerScanDepth = 2
	// defaultMCPConfigFileName 里存着 MCP 的访问令牌，对文件工具关闭，见
	// agentProtectedFiles。
	defaultMCPConfigFileName        = ".mcp.json"
	DefaultMCPStartupTimeoutMS      = 10_000
	DefaultMCPToolTimeoutMS         = 60_000
	DefaultCommandTimeoutMS         = 10_000
	DefaultBrowserTimeoutMS         = 15_000
	DefaultToolTimeoutMS            = 60_000
	DefaultFinalizationReserveMS    = 20_000
	DefaultProtocolRepairLimit      = 3
	MaxAllowedSteps                 = 16
	MaxAllowedToolOutputChars       = 20000
	MaxAllowedReadFileMaxBytes      = 512 * 1024
	MaxAllowedFileWriteMaxBytes     = 2 << 20
	MaxAllowedCommandTimeoutMS      = 60_000
	MaxAllowedBrowserTimeoutMS      = 60_000
	MaxAllowedToolTimeoutMS         = 120_000
	MaxAllowedFinalizationReserveMS = 60_000
	MaxAllowedProtocolRepairLimit   = 6
)

type LLMClient interface {
	Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error)
}

// DefaultCommandAllowlist 是新建配置带的命令白名单：一组只报状态、不碰数据的命令。
//
// 收进来的标准有三条，缺一不可：不读任意路径的文件、不出网、不改任何东西。
// 因为白名单里的程序在没有可用沙盒时就是以本进程权限直接跑的，它能碰什么完全
// 由它自己决定，白名单只管得到「能不能跑」。
//
// 于是这些都被刻意排除在外：
//   - cat / ls / head / tail / find —— 能读任意路径，包括 config.yaml 和数据库；
//     工作目录内的读取已经有 read_file / grep / find_files，它们锁在 workspace 里。
//   - curl / wget / nc —— 读到的东西能被发出去，这一层白名单挡不住。
//   - ps —— 进程列表会带上别的进程的完整命令行，那里面可能有别人的密钥。
//     Diana 自己的 CPU 和内存 host_stats 已经给了，不需要靠它。
//   - git / 包管理器 / 任何写操作 —— 会改磁盘。
//
// 想要更多就自己往里加，那是明确的一次授权动作。
func DefaultCommandAllowlist() []string {
	return []string{"uptime", "free", "df", "uname", "nproc", "date", "hostname", "whoami"}
}

type Config struct {
	WorkDir            string
	MaxSteps           int
	MaxToolOutputChars int
	ReadFileMaxBytes   int
	ListDirectoryLimit int
	SkillRoots         []string
	ManagedSkillRoot   string
	SkillsListBudget   int
	// SkillTriggerScanDepth 是关键词扫描回看的用户消息条数。
	SkillTriggerScanDepth int
	MCPConfigPath         string
	MCPStartupTimeoutMS   int
	MCPToolTimeoutMS      int
	ExtensionManagement   bool
	BuiltinExtensions     []BuiltinExtension
	BuiltinSkills         []SkillMetadata
	ReservedSkillNames    []string
	// FileWriteEnabled 打开 write_file / edit_file。默认关闭：读错文件浪费一次
	// 调用，写错文件改的是磁盘，这一档该由部署方显式点头。
	FileWriteEnabled bool
	// FileWriteMaxBytes 是单次写入的字节上限，留空按 DefaultFileWriteMaxBytes。
	FileWriteMaxBytes int
	CommandAllowlist  []string
	CommandTimeoutMS  int
	// CommandSandbox 见 CommandSandbox* 常量，默认 auto。
	CommandSandbox string
	// CommandSandboxAllowNetwork 放开沙盒内的网络访问，默认关闭。
	CommandSandboxAllowNetwork bool
	BrowserCDPURL              string
	BrowserTimeoutMS           int
	// BrowserToolsDisabled 为 true 时 browser_open 那组 CDP 工具不登记：浏览器来源
	// 选的不是内置浏览器，这台机器人也没另配外部 CDP 地址。
	BrowserToolsDisabled bool
	// BrowserControl 是浏览器控制扩展的控制面句柄，由运行时注入，不是可序列化
	// 的配置项：为 nil 时 browser_ext_* 那组工具根本不登记。共享扩展底座按
	// ExtensionScope 取字段，它不在其中，所以不会被带进缓存键。
	BrowserControl BrowserControlBridge `json:"-"`
	// BuiltinBrowser 是内置浏览器（model/browserbox）的句柄，同样由运行时注入。
	// 它在时 browser_* 那组 CDP 工具就接到 Diana 自己那个常驻浏览器上，带着
	// 用户在里面建立的登录态；不在时沿用 BrowserCDPURL 指的外部浏览器。
	BuiltinBrowser        BuiltinBrowserBridge `json:"-"`
	ToolTimeoutMS         int
	FinalizationReserveMS int
	ProtocolRepairLimit   int
	// CoreTools 是每一步都带完整定义的工具；其余工具按需加载，见 deferred_tools.go。
	// 留空时全部工具都带完整定义。
	CoreTools []string
}

type Request struct {
	Messages []llm.Message
	TraceID  string
	Observer RunObserver
	// LoadedTools carries session discoveries, never tool instances or authority.
	LoadedTools []string
	// ToolsLoaded is called immediately, including when a later model call fails.
	ToolsLoaded func([]string)
	// RequireEvidence 让本轮必须先检索再收口：模型不调用 web_search 就直接
	// 给终稿时会被打回，要求它先查。调用方判断这一轮在问外部事实时置位。
	// 没有 web_search 工具时该标记自动失效，不会把回复卡死。
	RequireEvidence bool
}

type Response struct {
	Text         string       `json:"text"`
	Steps        []Step       `json:"steps,omitempty"`
	Provider     llm.Provider `json:"provider,omitempty"`
	Model        string       `json:"model,omitempty"`
	Usage        llm.Usage    `json:"usage,omitempty"`
	TraceID      string       `json:"trace_id,omitempty"`
	ModelTurns   int          `json:"model_turns,omitempty"`
	FinishReason string       `json:"finish_reason,omitempty"`
	DurationMS   int64        `json:"duration_ms,omitempty"`
	Claims       []ClaimTrace `json:"claims,omitempty"`
	// Silent 表示模型调用 agent_finalize 时自己选择了不发消息。Text 为空但这不是
	// 生成失败：调用方必须按「本轮不发送」处理，不要用任何兜底文案补一句。
	Silent bool `json:"silent,omitempty"`
	// SilentReason 是模型给出的一句原因，只用于事件记录和日志，不发给用户。
	SilentReason string `json:"silent_reason,omitempty"`
}

type Step struct {
	Index      int            `json:"index,omitempty"`
	Tool       string         `json:"tool"`
	Input      map[string]any `json:"input,omitempty"`
	Output     string         `json:"output,omitempty"`
	Error      string         `json:"error,omitempty"`
	Skipped    bool           `json:"skipped,omitempty"`
	DurationMS int64          `json:"duration_ms,omitempty"`
}

type RunPhase string

const (
	RunPhaseStarted        RunPhase = "started"
	RunPhaseModelCompleted RunPhase = "model_completed"
	RunPhaseProtocolRepair RunPhase = "protocol_repair"
	RunPhaseToolStarted    RunPhase = "tool_started"
	RunPhaseToolCompleted  RunPhase = "tool_completed"
	RunPhaseCompleted      RunPhase = "completed"
	RunPhaseFailed         RunPhase = "failed"
)

// RunEvent is emitted by the Agent harness. Normal observers should continue to
// use the summary fields; raw fields are for an explicitly enabled debug sink.
type RunEvent struct {
	TraceID        string
	Phase          RunPhase
	ModelTurn      int
	ToolCall       int
	MaxToolCalls   int
	Tool           string
	InputKeys      []string
	ToolInput      map[string]any
	ToolOutput     string
	Metadata       map[string]any
	AvailableTools []ToolCatalogItem
	OutputChars    int
	DurationMS     int64
	Error          string
	FinishReason   string
	Usage          llm.Usage
}

type RunObserver func(context.Context, RunEvent)

// WithDefaults 补齐 Agent 配置默认值并限制上限。
func (cfg Config) WithDefaults() Config {
	if cfg.WorkDir == "" {
		cfg.WorkDir = "."
	}
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = DefaultMaxSteps
	}
	if cfg.MaxSteps > MaxAllowedSteps {
		// Agent 步数设置硬上限，避免模型反复调用工具导致一次回复无限拖长。
		cfg.MaxSteps = MaxAllowedSteps
	}
	if cfg.MaxToolOutputChars <= 0 {
		cfg.MaxToolOutputChars = DefaultMaxToolOutputChars
	}
	if cfg.MaxToolOutputChars > MaxAllowedToolOutputChars {
		// 工具输出会回填给模型，过长会撑爆上下文，所以这里做全局上限。
		cfg.MaxToolOutputChars = MaxAllowedToolOutputChars
	}
	if cfg.ReadFileMaxBytes <= 0 {
		cfg.ReadFileMaxBytes = DefaultReadFileMaxBytes
	}
	if cfg.FileWriteMaxBytes <= 0 {
		cfg.FileWriteMaxBytes = DefaultFileWriteMaxBytes
	}
	if cfg.FileWriteMaxBytes > MaxAllowedFileWriteMaxBytes {
		cfg.FileWriteMaxBytes = MaxAllowedFileWriteMaxBytes
	}
	if cfg.ReadFileMaxBytes > MaxAllowedReadFileMaxBytes {
		// 文件读取限制按字节控制，防止工具误读大文件。
		cfg.ReadFileMaxBytes = MaxAllowedReadFileMaxBytes
	}
	if cfg.ListDirectoryLimit <= 0 {
		cfg.ListDirectoryLimit = DefaultListDirectoryLimit
	}
	if cfg.SkillTriggerScanDepth <= 0 {
		cfg.SkillTriggerScanDepth = DefaultSkillTriggerScanDepth
	}
	if cfg.SkillsListBudget <= 0 {
		cfg.SkillsListBudget = DefaultSkillsListBudget
	}
	if cfg.MCPStartupTimeoutMS <= 0 {
		cfg.MCPStartupTimeoutMS = DefaultMCPStartupTimeoutMS
	}
	if cfg.MCPToolTimeoutMS <= 0 {
		cfg.MCPToolTimeoutMS = DefaultMCPToolTimeoutMS
	}
	if cfg.CommandTimeoutMS <= 0 {
		cfg.CommandTimeoutMS = DefaultCommandTimeoutMS
	}
	if cfg.CommandTimeoutMS > MaxAllowedCommandTimeoutMS {
		cfg.CommandTimeoutMS = MaxAllowedCommandTimeoutMS
	}
	cfg.CommandAllowlist = cleanStringList(cfg.CommandAllowlist)
	cfg.CommandSandbox = normalizeCommandSandboxMode(cfg.CommandSandbox)
	if cfg.BrowserTimeoutMS <= 0 {
		cfg.BrowserTimeoutMS = DefaultBrowserTimeoutMS
	}
	if cfg.BrowserTimeoutMS > MaxAllowedBrowserTimeoutMS {
		cfg.BrowserTimeoutMS = MaxAllowedBrowserTimeoutMS
	}
	if cfg.ToolTimeoutMS <= 0 {
		cfg.ToolTimeoutMS = DefaultToolTimeoutMS
	}
	if cfg.ToolTimeoutMS > MaxAllowedToolTimeoutMS {
		cfg.ToolTimeoutMS = MaxAllowedToolTimeoutMS
	}
	if cfg.FinalizationReserveMS <= 0 {
		cfg.FinalizationReserveMS = DefaultFinalizationReserveMS
	}
	if cfg.FinalizationReserveMS > MaxAllowedFinalizationReserveMS {
		cfg.FinalizationReserveMS = MaxAllowedFinalizationReserveMS
	}
	if cfg.ProtocolRepairLimit <= 0 {
		cfg.ProtocolRepairLimit = DefaultProtocolRepairLimit
	}
	if cfg.ProtocolRepairLimit > MaxAllowedProtocolRepairLimit {
		cfg.ProtocolRepairLimit = MaxAllowedProtocolRepairLimit
	}
	if strings.TrimSpace(cfg.BrowserCDPURL) == "" {
		cfg.BrowserCDPURL = "http://127.0.0.1:9222"
	}
	cfg.SkillRoots = defaultSkillRoots(cfg.WorkDir, cfg.SkillRoots)
	workDir, err := filepath.Abs(cfg.WorkDir)
	if err != nil {
		workDir = cfg.WorkDir
	}
	if strings.TrimSpace(cfg.ManagedSkillRoot) == "" {
		cfg.ManagedSkillRoot = filepath.Join(workDir, ".agents", "skills")
	}
	if !filepath.IsAbs(cfg.ManagedSkillRoot) {
		cfg.ManagedSkillRoot = filepath.Join(workDir, cfg.ManagedSkillRoot)
	}
	cfg.ManagedSkillRoot = filepath.Clean(cfg.ManagedSkillRoot)
	if strings.TrimSpace(cfg.MCPConfigPath) == "" {
		cfg.MCPConfigPath = defaultMCPConfigPath(workDir)
	}
	cfg.BuiltinExtensions = normalizeBuiltinExtensions(cfg.BuiltinExtensions)
	cfg.BuiltinSkills = normalizeBuiltinSkills(cfg.BuiltinSkills)
	cfg.ReservedSkillNames = cleanStringList(cfg.ReservedSkillNames)
	return cfg
}

// defaultMCPConfigPath 把 MCP 配置放在 Agent 工作目录的隔壁，而不是里面。
//
// 这个文件里是 access token 原文。放在工作目录里，它就落在文件工具的可达范围内——
// 工具只校验「不许走出工作目录」，不看读的是什么，于是一句「读一下 .mcp.json」就能
// 把令牌打进聊天记录。放到外面，safePath 那道边界本身就够了，不用指望黑名单记全。
//
// 黑名单仍然留着（见 agentProtectedFiles）：用户可以把路径显式指回工作目录里，
// 扩展开关和对象名单也仍然住在里面。run_command 两头都挡不住——命令沙箱只限制写入，
// 读是放开的。
func defaultMCPConfigPath(workDir string) string {
	parent := filepath.Dir(workDir)
	if parent == "" || parent == workDir {
		// 工作目录已经是根了，再往上没有位置可放，只能退回原处，靠黑名单挡。
		return filepath.Join(workDir, defaultMCPConfigFileName)
	}
	return filepath.Join(parent, defaultMCPConfigFileName)
}

func cleanStringList(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func defaultSkillRoots(workDir string, configured []string) []string {
	base, err := filepath.Abs(workDir)
	if err != nil {
		base = workDir
	}
	seen := map[string]bool{}
	var roots []string
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(base, path)
		}
		cleaned := filepath.Clean(path)
		if !seen[cleaned] {
			seen[cleaned] = true
			roots = append(roots, cleaned)
		}
	}
	for _, path := range configured {
		add(path)
	}
	add(filepath.Join(base, ".agents", "skills"))
	add(filepath.Join(base, "skills"))
	return roots
}

// ExtensionScope 只保留扩展管理（Skills 目录、MCP 配置与进程）用到的字段。
// 共享扩展底座按它区分：各机器人的步数、命令白名单、沙盒这些设置不影响扩展本身，
// 不该让同一套 MCP 服务因此被拉起好几份。
func (cfg Config) ExtensionScope() Config {
	cfg = cfg.WithDefaults()
	return Config{
		WorkDir:             cfg.WorkDir,
		SkillRoots:          append([]string(nil), cfg.SkillRoots...),
		ManagedSkillRoot:    cfg.ManagedSkillRoot,
		MCPConfigPath:       cfg.MCPConfigPath,
		MCPStartupTimeoutMS: cfg.MCPStartupTimeoutMS,
		MCPToolTimeoutMS:    cfg.MCPToolTimeoutMS,
		ExtensionManagement: cfg.ExtensionManagement,
		ReservedSkillNames:  append([]string(nil), cfg.ReservedSkillNames...),
	}.WithDefaults()
}
