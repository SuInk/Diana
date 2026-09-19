// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Device login captures only the public verification URL and one-time code.
// Credential files stay inside the data volume and never enter HTTP responses.
type codingDeviceLogin struct {
	mu     sync.Mutex
	output []byte
	done   bool
	failed bool
	cancel context.CancelFunc
}

func (s *codingDeviceLogin) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.output) < 16384 {
		n := 16384 - len(s.output)
		if n > len(p) {
			n = len(p)
		}
		s.output = append(s.output, p[:n]...)
	}
	return len(p), nil
}

var codingLoginMu sync.Mutex
var codingLogins = map[string]*codingDeviceLogin{}
var codingDeviceCode = regexp.MustCompile(`\b[A-Z0-9]{4}-[A-Z0-9]{5}\b|\b[A-Z0-9]{4}-[A-Z0-9]{4}\b`)
var codingANSI = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func codingAuthDir(cfg codingAgentConfig) string {
	id := strings.ToLower(cfg.Agent)
	if id == "" {
		id = codingDefaultAgent
	}
	return filepath.Join(codingManagedRoot(), "auth", cfg.Backend, id)
}
func CodingAgentDeviceLogin(settings SettingValues, name, operation string) (CodingSetupStatus, error) {
	cfg, err := codingAgentConfigFor(settings, name)
	if err != nil {
		return CodingSetupStatus{}, err
	}
	if cfg.Backend != codingBackendCodex {
		return CodingSetupStatus{}, fmt.Errorf("此登录入口仅支持 Codex；Claude 请配置 API 密钥或复用已有 CLI 登录")
	}
	if cfg.APIKey != "" || cfg.BaseURL != "" {
		return CodingSetupStatus{}, fmt.Errorf("设备登录用于官方 ChatGPT 账户，请先清除该代理的 API 密钥和自定义服务地址")
	}
	dir := codingAuthDir(cfg)
	codingLoginMu.Lock()
	defer codingLoginMu.Unlock()
	state := codingLogins[dir]
	if operation == "login-start" {
		if state != nil {
			state.mu.Lock()
			done := state.done
			state.mu.Unlock()
			if !done {
				return codingLoginStatus(state), nil
			}
		}
		command, err := exec.LookPath(cfg.Command)
		if err != nil {
			return CodingSetupStatus{}, fmt.Errorf("请先安装 Codex CLI")
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return CodingSetupStatus{}, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		state = &codingDeviceLogin{cancel: cancel}
		codingLogins[dir] = state
		cmd := exec.CommandContext(ctx, command, "login", "--device-auth", "-c", `cli_auth_credentials_store="file"`)
		cmd.SysProcAttr = detachedProcAttr()
		cmd.Cancel = func() error { killCodingProcess(cmd.Process.Pid); return nil }
		cmd.WaitDelay = 5 * time.Second
		cmd.Dir = dir
		cmd.Env = append(codingJobEnv(cfg), "CODEX_HOME="+dir)
		cmd.Stdout = state
		cmd.Stderr = state
		if err := cmd.Start(); err != nil {
			cancel()
			delete(codingLogins, dir)
			return CodingSetupStatus{}, fmt.Errorf("无法启动设备登录")
		}
		go func() {
			defer recoverGoroutinePanic("coding.deviceLogin")
			err := cmd.Wait()
			cancel()
			state.mu.Lock()
			state.done = true
			state.failed = err != nil
			state.mu.Unlock()
		}()
	}
	if state == nil {
		return CodingSetupStatus{Message: "尚未发起设备登录", LoginState: "idle"}, nil
	}
	if operation == "login-cancel" {
		state.cancel()
		return CodingSetupStatus{Message: "已取消设备登录", LoginState: "cancelled"}, nil
	}
	return codingLoginStatus(state), nil
}
func codingLoginStatus(s *codingDeviceLogin) CodingSetupStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		if s.failed {
			return CodingSetupStatus{Message: "登录未完成或已过期，请重试；账户需允许设备代码登录", LoginState: "failed"}
		}
		return CodingSetupStatus{Message: "登录成功，凭据已保存在数据目录；可测试连接", LoginState: "success"}
	}
	text := codingANSI.ReplaceAllString(string(s.output), "")
	status := CodingSetupStatus{Message: "正在申请设备登录码…", LoginState: "pending"}
	if strings.Contains(text, "https://auth.openai.com/codex/device") {
		status.LoginURL = "https://auth.openai.com/codex/device"
		status.DeviceCode = codingDeviceCode.FindString(text)
		status.Message = "打开登录页面并输入一次性代码，然后点击刷新登录状态"
	}
	return status
}
