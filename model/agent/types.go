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
	DefaultMaxSteps           = 8
	DefaultMaxToolOutputChars = 8000
	DefaultReadFileMaxBytes   = 64 * 1024
	// DefaultFileWriteMaxBytes 是单次写入的默认上限。比读的上限大一些：模型生成
	// 一个完整文件时经常一次写下去，卡太死会让它退化成反复追加。
	DefaultFileWriteMaxBytes = 256 * 1024
	// 读文件默认一次多少行。工具结果统一被截到 MaxToolOutputChars，一次读太多
	// 只会在截断处白白丢掉，不如让模型按需要翻页。
	defaultReadFileLines            = 200
	maxReadFileLines                = 2000
	DefaultListDirectoryLimit       = 200
	DefaultSkillsListBudget         = 8000
	DefaultMCPStartupTimeoutMS      = 10_000
	DefaultMCPToolTimeoutMS         = 60_000
	DefaultCommandTimeoutMS         = 10_000
	DefaultBrowserTimeoutMS         = 15_000
	DefaultToolTimeoutMS            = 60_000
	DefaultFinalizationReserveMS    = 20_000
	DefaultProtocolRepairLimit      = 3
	MaxAllowedSteps                 = 8
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
//     Diana 自己的 CPU 和内存 diana.host_stats 已经给了，不需要靠它。
//   - git / 包管理器 / 任何写操作 —— 会改磁盘。
//
// 想要更多就自己往里加，那是明确的一次授权动作。
func DefaultCommandAllowlist() []string {
	return []string{"uptime", "free", "df", "uname", "nproc", "date", "hostname", "whoami"}
}

type Config struct {
	WorkDir             string
	MaxSteps            int
	MaxToolOutputChars  int
	ReadFileMaxBytes    int
	ListDirectoryLimit  int
	SkillRoots          []string
	ManagedSkillRoot    string
	SkillsListBudget    int
	MCPConfigPath       string
	MCPStartupTimeoutMS int
	MCPToolTimeoutMS    int
	ExtensionManagement bool
	BuiltinExtensions   []BuiltinExtension
	BuiltinSkills       []SkillMetadata
	ReservedSkillNames  []string
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
	ToolTimeoutMS              int
	FinalizationReserveMS      int
	ProtocolRepairLimit        int
	// EvidenceLedgerAdvisory 让逐主张证据账本只记录不拦截：claims 仍然写进
	// trace 和运行元数据，但不再因为证据绑定失败要求模型重写 final。
	EvidenceLedgerAdvisory bool
}

type Request struct {
	Messages []llm.Message
	TraceID  string
	Observer RunObserver
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
		cfg.MCPConfigPath = filepath.Join(workDir, ".mcp.json")
	}
	cfg.BuiltinExtensions = normalizeBuiltinExtensions(cfg.BuiltinExtensions)
	cfg.BuiltinSkills = normalizeBuiltinSkills(cfg.BuiltinSkills)
	cfg.ReservedSkillNames = cleanStringList(cfg.ReservedSkillNames)
	return cfg
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
