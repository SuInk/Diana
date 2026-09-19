// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const codingAgentSettingProfiles = "agents"
const codingDefaultAgent = "default"

// Credentials are referenced by server environment variable, never stored in
// this non-secret setting or exposed in tool results.
type codingAgentProfile struct {
	ID               string `json:"id"`
	BaseURL          string `json:"base_url,omitempty"`
	ManagedWorkspace bool   `json:"managed_workspace,omitempty"`
	Backend          string `json:"backend"`
	Command          string `json:"command"`
	Template         string `json:"command_template"`
	Model            string `json:"model"`
	APIKeyEnv        string `json:"api_key_env"`
	ApprovalMode     string `json:"approval_mode"`
	Default          bool   `json:"default"`
}

var codingEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func normalizeCodingAgentProfiles(raw any) ([]codingAgentProfile, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("编码代理配置必须是列表")
	}
	profiles := []codingAgentProfile{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&profiles); err != nil || profiles == nil {
		return nil, fmt.Errorf("编码代理配置格式无效，请检查字段；密钥请使用服务端环境变量名")
	}
	if len(profiles) > 16 {
		return nil, fmt.Errorf("最多配置 16 个额外编码代理")
	}
	seen := map[string]bool{codingDefaultAgent: true}
	hasDefault := false
	for i := range profiles {
		p := &profiles[i]
		p.ID = strings.TrimSpace(p.ID)
		p.Command = strings.TrimSpace(p.Command)
		p.Template = strings.TrimSpace(p.Template)
		p.Model = strings.TrimSpace(p.Model)
		p.APIKeyEnv = strings.TrimSpace(p.APIKeyEnv)
		p.BaseURL = strings.TrimSpace(p.BaseURL)
		if p.BaseURL != "" {
			u, err := url.Parse(p.BaseURL)
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return nil, fmt.Errorf("代理服务地址必须是有效的 HTTP(S) 地址，不能包含凭据或查询参数")
			}
		}
		if p.ID == "" || !validCodingWorkspaceName(p.ID) || seen[strings.ToLower(p.ID)] {
			return nil, fmt.Errorf("编码代理名称必须唯一，使用字母、数字、点、下划线或连字符，且不能是 default")
		}
		seen[strings.ToLower(p.ID)] = true
		if _, ok := codingBackendPresets()[p.Backend]; !ok && p.Backend != codingBackendCustom {
			return nil, fmt.Errorf("编码代理 %s 的后端无效", p.ID)
		}
		if p.Backend == codingBackendCustom && (p.Command == "" || p.Template == "") {
			return nil, fmt.Errorf("自定义编码代理 %s 必须填写可执行文件和命令模板", p.ID)
		}
		if p.Template != "" && !strings.Contains(p.Template, "{{instruction}}") {
			return nil, fmt.Errorf("编码代理 %s 的模板必须包含 {{instruction}}", p.ID)
		}
		if p.APIKeyEnv != "" && (!codingEnvName.MatchString(p.APIKeyEnv) || p.Backend == codingBackendCustom) {
			return nil, fmt.Errorf("编码代理 %s 的密钥环境变量名无效，自定义后端请使用自身环境配置", p.ID)
		}
		switch p.ApprovalMode {
		case codingApprovalModeDangerous, codingApprovalModeAllWrites, codingApprovalModeOff:
		default:
			return nil, fmt.Errorf("编码代理 %s 必须选择审批模式", p.ID)
		}
		if p.Backend != codingBackendClaude && p.ApprovalMode != codingApprovalModeOff {
			return nil, fmt.Errorf("编码代理 %s 不支持聊天审批，请明确选择关闭或使用 Claude Code", p.ID)
		}
		if p.Default && hasDefault {
			return nil, fmt.Errorf("只能设置一个默认编码代理")
		}
		hasDefault = hasDefault || p.Default
	}
	return profiles, nil
}

func codingProfiles(settings SettingValues) ([]codingAgentProfile, error) {
	raw, ok := settings[codingAgentSettingProfiles]
	if !ok {
		return nil, nil
	}
	return normalizeCodingAgentProfiles(raw)
}

func codingAgentConfigFor(settings SettingValues, agent string) (codingAgentConfig, error) {
	profiles, err := codingProfiles(settings)
	if err != nil {
		return codingAgentConfig{}, err
	}
	agent = strings.TrimSpace(agent)
	if agent == "" {
		agent = codingDefaultAgent
		for _, p := range profiles {
			if p.Default {
				agent = p.ID
			}
		}
	}
	if strings.EqualFold(agent, codingDefaultAgent) {
		cfg, err := codingAgentConfigFromSettings(settings)
		cfg.Agent = codingDefaultAgent
		return cfg, err
	}
	for _, p := range profiles {
		if !strings.EqualFold(p.ID, agent) {
			continue
		}
		merged := SettingValues{}
		for k, v := range settings {
			merged[k] = v
		}
		merged[codingAgentSettingBackend] = p.Backend
		merged[codingAgentSettingCommand] = p.Command
		merged[codingAgentSettingTemplate] = p.Template
		merged[codingAgentSettingModel] = p.Model
		merged[codingAgentSettingApprovalMode] = p.ApprovalMode
		// Extra profiles never inherit the legacy profile's explicit API key.
		merged[codingAgentSettingAPIKey] = codingSavedKey(settings, p.ID)
		if p.APIKeyEnv != "" && codingSavedKey(settings, p.ID) == "" {
			key, ok := os.LookupEnv(p.APIKeyEnv)
			if !ok || strings.TrimSpace(key) == "" {
				return codingAgentConfig{}, fmt.Errorf("编码代理 %s 的密钥环境变量未配置", p.ID)
			}
			merged[codingAgentSettingAPIKey] = key
		}
		cfg, err := codingAgentConfigFromSettings(merged)
		cfg.Agent = p.ID
		cfg.BaseURL = p.BaseURL
		cfg.ManagedWorkspace = p.ManagedWorkspace
		if p.ManagedWorkspace {
			cfg.Workspaces = []codingWorkspace{{Name: p.ID, Dir: filepath.Join(CodingWorkspaceRoot(), "managed-"+strings.ToLower(p.ID))}}
		}
		return cfg, err
	}
	return codingAgentConfig{}, fmt.Errorf("未找到编码代理 %q，请用 agents 查看可用配置", agent)
}

func codingAgentFingerprint(cfg codingAgentConfig) string {
	data, _ := json.Marshal([]string{cfg.Backend, cfg.Command, cfg.Template, cfg.Model, cfg.BaseURL})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
