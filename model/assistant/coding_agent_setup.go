// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const codingAgentSettingKeys = "agent_api_keys"

func (p *CodingAgentPlugin) MergeSecretSetting(key, previous, submitted string) (string, error) {
	if key != codingAgentSettingKeys {
		return submitted, nil
	}
	current := map[string]string{}
	if previous != "" {
		if err := json.Unmarshal([]byte(previous), &current); err != nil {
			return "", fmt.Errorf("已保存的代理密钥格式无效")
		}
	}
	if current == nil {
		current = map[string]string{}
	}
	var updates map[string]*string
	if err := json.Unmarshal([]byte(submitted), &updates); err != nil {
		return "", fmt.Errorf("代理密钥格式无效")
	}
	for id, value := range updates {
		if id == "" || !validCodingWorkspaceName(id) {
			return "", fmt.Errorf("代理密钥名称无效")
		}
		id = strings.ToLower(id)
		if value == nil || strings.TrimSpace(*value) == "" {
			delete(current, id)
		} else {
			current[id] = strings.TrimSpace(*value)
		}
	}
	data, err := json.Marshal(current)
	return string(data), err
}

func codingSavedKey(settings SettingValues, id string) string {
	var keys map[string]string
	_ = json.Unmarshal([]byte(settings.String(codingAgentSettingKeys, "{}")), &keys)
	return keys[strings.ToLower(id)]
}
func prepareCodingRuntime(cfg codingAgentConfig) error {
	if cfg.ManagedWorkspace && len(cfg.Workspaces) == 1 {
		dir := cfg.Workspaces[0].Dir
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := exec.CommandContext(ctx, "git", "init", dir).Run(); err != nil {
				return fmt.Errorf("无法初始化工作目录，请检查 git 是否安装")
			}
		}
	}
	if cfg.Command != codingManagedCommand(cfg.Backend) {
		return nil
	}
	return os.MkdirAll(filepath.Join(codingManagedRoot(), "state", cfg.Backend), 0700)
}
func codingManagedRoot() string { return filepath.Join(AgentWorkspaceDir(), "coding-runtime") }
func codingManagedCommand(backend string) string {
	name := backend
	if runtime.GOOS == "windows" {
		return filepath.Join(codingManagedRoot(), backend, name+".cmd")
	}
	return filepath.Join(codingManagedRoot(), backend, "bin", name)
}
func resolveCodingCommand(command, backend string) string {
	if command != "" {
		return command
	}
	managed := codingManagedCommand(backend)
	if info, err := os.Stat(managed); err == nil && !info.IsDir() {
		return managed
	}
	return codingBackendPresets()[backend].Command
}

// Setup operates only on saved configurations and fixed official package names.
// Arbitrary shell fragments, package names and prompts never come from the API.
type CodingSetupStatus struct {
	LoginState    string `json:"login_state,omitempty"`
	LoginURL      string `json:"login_url,omitempty"`
	DeviceCode    string `json:"device_code,omitempty"`
	Installed     bool   `json:"installed"`
	KeyConfigured bool   `json:"key_configured"`
	Installable   bool   `json:"installable"`
	Message       string `json:"message"`
}

var codingSetupMu sync.Mutex

func CodingAgentSetup(ctx context.Context, settings SettingValues, name, operation string) (CodingSetupStatus, error) {
	cfg, err := codingAgentConfigFor(settings, name)
	if err != nil {
		return CodingSetupStatus{}, err
	}
	if cfg.Backend != codingBackendCodex && cfg.Backend != codingBackendClaude {
		return CodingSetupStatus{}, fmt.Errorf("自定义 CLI 请自行安装配置")
	}
	if !codingSetupMu.TryLock() {
		return CodingSetupStatus{}, fmt.Errorf("编码代理安装或检测正在进行，请稍后重试")
	}
	defer codingSetupMu.Unlock()
	path, lookErr := exec.LookPath(cfg.Command)
	if lookErr == nil {
		probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		probe := exec.CommandContext(probeCtx, path, "--version")
		lookErr = probe.Run()
		cancel()
	}
	_, npmErr := exec.LookPath("npm")
	status := CodingSetupStatus{Installed: lookErr == nil, KeyConfigured: cfg.APIKey != "", Installable: npmErr == nil && (cfg.Command == cfg.Backend || cfg.Command == codingManagedCommand(cfg.Backend))}
	switch operation {
	case "status":
		status.Message = "CLI 未安装，请点击安装"
		if status.Installed {
			status.Message = "CLI 已安装；点击测试连接验证凭据和模型"
		} else if !status.Installable {
			status.Message = "未找到 CLI 或 npm；Docker 请升级到包含安装支持的镜像，本机需安装 Node.js/npm"
		}
		return status, nil
	case "install":
		if status.Installed {
			status.Message = "CLI 已安装，无需重复安装"
			return status, nil
		}
		if !status.Installable {
			return status, fmt.Errorf("无法自动安装：需要 npm，且可执行文件必须留空")
		}
		prefix := filepath.Join(codingManagedRoot(), cfg.Backend)
		if err := os.MkdirAll(prefix, 0700); err != nil {
			return status, err
		}
		pkg := "@openai/codex"
		if cfg.Backend == codingBackendClaude {
			pkg = "@anthropic-ai/claude-code"
		}
		runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(runCtx, "npm", "install", "--global", "--prefix", prefix, "--cache", filepath.Join(codingManagedRoot(), "npm-cache"), "--no-audit", "--no-fund", pkg)
		cmd.SysProcAttr = detachedProcAttr()
		cmd.Cancel = func() error { killCodingProcess(cmd.Process.Pid); return nil }
		cmd.WaitDelay = 5 * time.Second
		cmd.Dir = prefix
		// Do not expose npm output (which may contain registry credentials) to HTTP.
		if err := cmd.Run(); err != nil {
			return status, fmt.Errorf("CLI 安装失败，请检查服务器网络、磁盘权限和 npm 可用性")
		}
		if _, err := os.Stat(codingManagedCommand(cfg.Backend)); err != nil {
			return status, fmt.Errorf("安装完成但未找到 CLI")
		}
		probe := exec.CommandContext(runCtx, codingManagedCommand(cfg.Backend), "--version")
		if err := probe.Run(); err != nil {
			return status, fmt.Errorf("CLI 已安装但无法启动，请检查系统架构和运行库")
		}
		status.Installed = true
		status.Message = "CLI 安装完成，已保存到数据目录"
		return status, nil
	case "test":
		if lookErr != nil {
			return status, fmt.Errorf("请先安装 CLI")
		}
		// A real minimal model call in an empty temporary directory. No user project
		// or chat is attached, and the prompt cannot be supplied by the client.
		dir, err := os.MkdirTemp("", "diana-coding-check-")
		if err != nil {
			return status, err
		}
		defer os.RemoveAll(dir)
		runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		args := []string{"exec", "--skip-git-repo-check", "--sandbox", "read-only", "--json", "--cd", dir, "Reply exactly DIANA_CONNECTION_OK. Do not use any tools."}
		if cfg.Backend == codingBackendClaude {
			args = []string{"-p", "Reply exactly DIANA_CONNECTION_OK. Do not use any tools.", "--output-format", "stream-json", "--verbose", "--tools", ""}
		}
		if cfg.Model != "" {
			args = append(args, "--model", cfg.Model)
		}
		if err := prepareCodingRuntime(cfg); err != nil {
			return status, err
		}
		cmd := exec.CommandContext(runCtx, path, codingProviderArgs(cfg, args)...)
		cmd.SysProcAttr = detachedProcAttr()
		cmd.Cancel = func() error { killCodingProcess(cmd.Process.Pid); return nil }
		cmd.WaitDelay = 5 * time.Second
		cmd.Dir = dir
		cmd.Env = codingJobEnv(cfg)
		log, err := os.CreateTemp(dir, "result-")
		if err != nil {
			return status, err
		}
		cmd.Stdout = log
		cmd.Stderr = log
		runErr := cmd.Run()
		log.Close()
		snapshot := parseCodingLog(log.Name())
		if runErr != nil || snapshot.IsError || strings.TrimSpace(snapshot.Result) != "DIANA_CONNECTION_OK" {
			return status, fmt.Errorf("连接测试失败：请检查 API 密钥、模型、登录有效期及网络；本次测试未通过")
		}
		status.Message = "连接成功，模型已返回测试结果"
		return status, nil
	default:
		return status, fmt.Errorf("不支持的配置操作")
	}
}

func codingProviderArgs(cfg codingAgentConfig, args []string) []string {
	if cfg.Backend == codingBackendCodex && cfg.BaseURL != "" {
		args = append(args, "-c", `model_provider="diana"`, "-c", `model_providers.diana.name="Diana"`, "-c", "model_providers.diana.base_url="+strconv.Quote(cfg.BaseURL), "-c", `model_providers.diana.wire_api="responses"`, "-c", `model_providers.diana.requires_openai_auth=true`)
	}
	return args
}
