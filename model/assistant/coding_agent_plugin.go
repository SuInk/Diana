// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

const (
	codingAgentPluginID = "official.coding-agent"

	codingAgentSettingBackend     = "backend"
	codingAgentSettingCommand     = "command"
	codingAgentSettingTemplate    = "command_template"
	codingAgentSettingModel       = "model"
	codingAgentSettingWorkspaces  = "workspaces"
	codingAgentSettingMaxRuntime  = "max_runtime_minutes"
	codingAgentSettingConcurrency = "concurrency"
	codingAgentSettingAPIKey      = "api_key"

	codingAgentSettingApprovalMode     = "approval_mode"
	codingAgentSettingApprovalPatterns = "approval_patterns"
	codingAgentSettingApprovalTimeout  = "approval_timeout_minutes"

	codingBackendClaude = "claude"
	codingBackendCodex  = "codex"
	codingBackendCustom = "custom"

	defaultCodingMaxRuntimeMinutes = 120
	maxCodingMaxRuntimeMinutes     = 1440
	defaultCodingConcurrency       = 1
	maxCodingConcurrency           = 4
)

// codingBackendPreset 是一个编码 CLI 的调用方式。argv 用模板而不是写死：codex 这
// 类 CLI 的参数还在变，模板写错时用户改一个设置项就能救回来，不必等下一个版本。
//
// 占位符：{{instruction}} 指令原文，{{model}} 模型名（未配置时整个参数被丢掉），
// {{session}} 续跑的会话 ID（不续跑时整个参数被丢掉），{{workspace}} 工作目录绝对路径，
// {{settings}} 审批 hook 的设置文件（审批关闭时整个参数被丢掉）。
type codingBackendPreset struct {
	Command  string
	Template string
	// EnvKey 是这个后端读的凭据环境变量名。插件设置里的密钥按它注入。
	EnvKey string
	// StreamJSON 说明标准输出是 Claude Code 那套 stream-json。只有它能被精确解析出
	// 会话 ID、逐步进度和结构化结果；其它后端退化成「读日志尾巴当结果」。
	StreamJSON bool
}

func codingBackendPresets() map[string]codingBackendPreset {
	return map[string]codingBackendPreset{
		codingBackendClaude: {
			Command:    "claude",
			Template:   "-p {{instruction}} --output-format stream-json --verbose --permission-mode auto --add-dir {{workspace}} --settings {{settings}} --model {{model}} --resume {{session}}",
			EnvKey:     "ANTHROPIC_API_KEY",
			StreamJSON: true,
		},
		codingBackendCodex: {
			Command:  "codex",
			Template: "exec {{instruction}} --json --dangerously-bypass-approvals-and-sandbox --cd {{workspace}} --model {{model}}",
			EnvKey:   "OPENAI_API_KEY",
		},
	}
}

// CodingAgentPlugin 把外部编码 CLI（Claude Code、Codex 等）接成机器人的一个工具。
//
// 它自己不解析消息，只提供清单和设置：工具是事件绑定的，由运行时在装配工具表时
// 按主人身份挂上去。插件停用时工具不出现，模型看不到这个能力。
type CodingAgentPlugin struct{}

func NewCodingAgentPlugin() *CodingAgentPlugin { return &CodingAgentPlugin{} }

func (p *CodingAgentPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:          codingAgentPluginID,
		Name:        "编码代理",
		Version:     "0.1.0",
		Description: "把 Claude Code、Codex 这类编码 CLI 接进对话：在持久工作区里长时间改代码，完成后汇报，运行途中可以随时查询进度。仅机器人主人可用。",
		Official:    true,
		BuiltIn:     true,
		// 它会在后台改代码、还会自己开口汇报结果，装完就生效等于替用户做了决定。
		DefaultDisabled: true,
		Permissions:     []string{"运行外部编码 CLI", "读写白名单仓库", "执行 git 操作"},
		Settings: []PluginSettingSpec{
			{
				Key:         codingAgentSettingBackend,
				Label:       "后端 CLI",
				Description: "claude 走 Claude Code 的 stream-json，进度和结果解析最完整；codex 与 custom 只能按日志尾巴汇报。",
				Type:        PluginSettingTypeSelect,
				Default:     codingBackendClaude,
				Options: []PluginSettingOption{
					{Value: codingBackendClaude, Label: "Claude Code（claude）"},
					{Value: codingBackendCodex, Label: "Codex（codex）"},
					{Value: codingBackendCustom, Label: "自定义命令"},
				},
			},
			{
				Key:         codingAgentSettingCommand,
				Label:       "可执行文件",
				Description: "留空按后端取默认名（claude / codex），从 PATH 查找。CLI 不在 PATH 里时填绝对路径。",
				Type:        PluginSettingTypeString,
				Default:     "",
			},
			{
				Key:   codingAgentSettingTemplate,
				Label: "命令模板",
				Description: "留空用后端预置。占位符：{{instruction}} 指令、{{model}} 模型、{{session}} 续跑会话 ID、" +
					"{{workspace}} 工作目录。带空值占位符的参数会整个丢掉。自定义后端必须填。",
				Type:    PluginSettingTypeText,
				Default: "",
				Rows:    3,
			},
			{
				Key:         codingAgentSettingModel,
				Label:       "模型",
				Description: "传给 CLI 的模型名，留空用 CLI 自己的默认值。",
				Type:        PluginSettingTypeString,
				Default:     "",
			},
			{
				Key:   codingAgentSettingWorkspaces,
				Label: "工作区白名单",
				Description: "一行一个，格式 名字=本地路径 或 名字=git 仓库地址。只有这里登记过的工作区能被派活；" +
					"填仓库地址时首次使用会 clone 到工作区目录下并常驻。",
				Type:    PluginSettingTypeText,
				Default: "",
				Rows:    6,
			},
			{
				Key:         codingAgentSettingMaxRuntime,
				Label:       "单次最长运行时间",
				Description: "超时后整个进程组被杀掉，已完成的改动留在工作区里。",
				Type:        PluginSettingTypeNumber,
				Default:     defaultCodingMaxRuntimeMinutes,
				Min:         settingRange(5),
				Max:         settingRange(maxCodingMaxRuntimeMinutes),
				Step:        5,
				Unit:        "分钟",
			},
			{
				Key:         codingAgentSettingConcurrency,
				Label:       "同时运行的任务数",
				Description: "同一个工作区永远只允许一个任务：两个代理同时改一份检出会互相覆盖。这里限制的是全局并发。",
				Type:        PluginSettingTypeNumber,
				Default:     defaultCodingConcurrency,
				Min:         settingRange(1),
				Max:         settingRange(maxCodingConcurrency),
				Step:        1,
				Unit:        "个",
			},
			{
				Key:         codingAgentSettingApprovalMode,
				Label:       "聊天里确认",
				Description: "需要点头的操作会在聊天里发给主人，回确认码才继续。危险操作只拦下面那张清单命中的命令；全部写操作连改文件也要确认（改一个文件问一次，很吵，一般只在试水时用）。只支持 Claude Code 后端。",
				Type:        PluginSettingTypeSelect,
				Default:     codingApprovalModeDangerous,
				Options: []PluginSettingOption{
					{Value: codingApprovalModeDangerous, Label: "危险操作要确认"},
					{Value: codingApprovalModeAllWrites, Label: "所有写操作都要确认"},
					{Value: codingApprovalModeOff, Label: "不确认，全程放手"},
				},
			},
			{
				Key:   codingAgentSettingApprovalPatterns,
				Label: "危险操作清单",
				Description: "一行一个命令片段，大小写不敏感，命中即要确认。留空用内置默认：" +
					"git push、git reset --hard、git clean -f、git tag -d、gh pr merge、gh pr create、gh release、npm publish、docker push、rm -rf、sudo。",
				Type:    PluginSettingTypeText,
				Default: "",
				Rows:    6,
			},
			{
				Key:         codingAgentSettingApprovalTimeout,
				Label:       "等确认多久",
				Description: "超过这个时间没回确认码就按拒绝处理。等待期间任务是停着的，这段时间也算在单次最长运行时间里。",
				Type:        PluginSettingTypeNumber,
				Default:     defaultCodingApprovalTimeoutMinutes,
				Min:         settingRange(1),
				Max:         settingRange(maxCodingApprovalTimeoutMinutes),
				Step:        1,
				Unit:        "分钟",
			},
			{
				Key:         codingAgentSettingAPIKey,
				Label:       "API 密钥",
				Description: "按后端注入 ANTHROPIC_API_KEY / OPENAI_API_KEY。留空则依赖 CLI 自己已登录的凭据——注意那是跑 Diana 的那个系统用户的登录态。",
				Type:        PluginSettingTypeString,
				Default:     "",
				Secret:      true,
			},
		},
	}
}

// Handle 不做任何事：这个插件的能力全在工具里，不往上下文里塞东西。
func (p *CodingAgentPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

type codingWorkspace struct {
	Name string
	// Dir 是工作目录绝对路径。登记本地路径时就是它本身，登记仓库地址时是
	// <工作区根>/coding/<名字>。
	Dir string
	// RepoURL 非空表示这个工作区由 Diana 负责 clone。
	RepoURL string
}

type codingAgentConfig struct {
	Backend     string
	Command     string
	Template    string
	Model       string
	EnvKey      string
	APIKey      string
	StreamJSON  bool
	MaxRuntime  time.Duration
	Concurrency int
	Workspaces  []codingWorkspace

	ApprovalMode     string
	ApprovalPatterns []string
	ApprovalTimeout  time.Duration
}

func codingAgentConfigFromSettings(settings SettingValues) (codingAgentConfig, error) {
	backend := strings.TrimSpace(settings.String(codingAgentSettingBackend, codingBackendClaude))
	preset, known := codingBackendPresets()[backend]
	if !known && backend != codingBackendCustom {
		return codingAgentConfig{}, fmt.Errorf("编码代理：未知后端 %q", backend)
	}
	cfg := codingAgentConfig{
		Backend:    backend,
		Command:    strings.TrimSpace(settings.String(codingAgentSettingCommand, "")),
		Template:   strings.TrimSpace(settings.String(codingAgentSettingTemplate, "")),
		Model:      strings.TrimSpace(settings.String(codingAgentSettingModel, "")),
		EnvKey:     preset.EnvKey,
		APIKey:     strings.TrimSpace(settings.String(codingAgentSettingAPIKey, "")),
		StreamJSON: preset.StreamJSON,
	}
	if cfg.Command == "" {
		cfg.Command = preset.Command
	}
	if cfg.Template == "" {
		cfg.Template = preset.Template
	}
	if cfg.Command == "" {
		return codingAgentConfig{}, fmt.Errorf("编码代理：自定义后端必须配置可执行文件")
	}
	if cfg.Template == "" {
		return codingAgentConfig{}, fmt.Errorf("编码代理：自定义后端必须配置命令模板")
	}
	if !strings.Contains(cfg.Template, "{{instruction}}") {
		return codingAgentConfig{}, fmt.Errorf("编码代理：命令模板必须包含 {{instruction}}")
	}
	minutes := settings.Int(codingAgentSettingMaxRuntime, defaultCodingMaxRuntimeMinutes)
	if minutes <= 0 {
		minutes = defaultCodingMaxRuntimeMinutes
	}
	if minutes > maxCodingMaxRuntimeMinutes {
		minutes = maxCodingMaxRuntimeMinutes
	}
	cfg.MaxRuntime = time.Duration(minutes) * time.Minute
	cfg.Concurrency = settings.Int(codingAgentSettingConcurrency, defaultCodingConcurrency)
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = defaultCodingConcurrency
	}
	if cfg.Concurrency > maxCodingConcurrency {
		cfg.Concurrency = maxCodingConcurrency
	}
	cfg.ApprovalMode = normalizeCodingApprovalMode(settings.String(codingAgentSettingApprovalMode, codingApprovalModeDangerous))
	cfg.ApprovalPatterns = parseCodingApprovalPatterns(settings.String(codingAgentSettingApprovalPatterns, ""))
	approvalMinutes := settings.Int(codingAgentSettingApprovalTimeout, defaultCodingApprovalTimeoutMinutes)
	if approvalMinutes <= 0 {
		approvalMinutes = defaultCodingApprovalTimeoutMinutes
	}
	if approvalMinutes > maxCodingApprovalTimeoutMinutes {
		approvalMinutes = maxCodingApprovalTimeoutMinutes
	}
	cfg.ApprovalTimeout = time.Duration(approvalMinutes) * time.Minute
	workspaces, err := parseCodingWorkspaces(settings.String(codingAgentSettingWorkspaces, ""))
	if err != nil {
		return codingAgentConfig{}, err
	}
	cfg.Workspaces = workspaces
	return cfg, nil
}

func (cfg codingAgentConfig) workspace(name string) (codingWorkspace, bool) {
	name = strings.TrimSpace(name)
	if name == "" && len(cfg.Workspaces) == 1 {
		// 只登记了一个工作区时不强迫模型点名：那唯一的一个就是答案。
		return cfg.Workspaces[0], true
	}
	for _, item := range cfg.Workspaces {
		if strings.EqualFold(item.Name, name) {
			return item, true
		}
	}
	return codingWorkspace{}, false
}

func (cfg codingAgentConfig) workspaceNames() []string {
	names := make([]string, 0, len(cfg.Workspaces))
	for _, item := range cfg.Workspaces {
		names = append(names, item.Name)
	}
	return names
}

// parseCodingWorkspaces 解析工作区白名单。名字要能安全地当目录名用：它会被拼进
// 工作区根路径，放开分隔符等于让一行设置把写入范围挪到别处去。
func parseCodingWorkspaces(raw string) ([]codingWorkspace, error) {
	lines := strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == '\r' })
	out := make([]codingWorkspace, 0, len(lines))
	seen := map[string]bool{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, target, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("编码代理：工作区 %q 缺少 = 号，格式是 名字=路径或仓库地址", line)
		}
		name = strings.TrimSpace(name)
		target = strings.TrimSpace(target)
		if name == "" || target == "" {
			return nil, fmt.Errorf("编码代理：工作区 %q 的名字或目标为空", line)
		}
		if !validCodingWorkspaceName(name) {
			return nil, fmt.Errorf("编码代理：工作区名 %q 只能用字母、数字、下划线、点和连字符", name)
		}
		key := strings.ToLower(name)
		if seen[key] {
			return nil, fmt.Errorf("编码代理：工作区名 %q 重复", name)
		}
		seen[key] = true
		item := codingWorkspace{Name: name}
		if isCodingRepoURL(target) {
			item.RepoURL = target
			item.Dir = filepath.Join(CodingWorkspaceRoot(), name)
		} else {
			abs, err := filepath.Abs(target)
			if err != nil {
				return nil, fmt.Errorf("编码代理：工作区 %q 路径无法解析：%w", name, err)
			}
			item.Dir = abs
		}
		out = append(out, item)
	}
	return out, nil
}

func validCodingWorkspaceName(name string) bool {
	if name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

func isCodingRepoURL(target string) bool {
	if strings.HasPrefix(target, "git@") {
		return true
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "ssh", "git":
		return parsed.Host != ""
	}
	return false
}
