// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
)

// RepositoryCredentialDefaultKey 是「默认凭据」（公共 Token / 全局认证方式）在检测结果里的键。
const RepositoryCredentialDefaultKey = "default"

// repositoryCredentialProbeRepositoryLimit 限制每条凭据顺带检测的仓库数，免得一次检测
// 打出几十个请求。
const repositoryCredentialProbeRepositoryLimit = 8

// TestCredentials 逐条检测 GitHub 凭据实际登录的是哪个账号，并顺带看它对绑定仓库有没有
// 写权限。换了 Token 却不知道到底生效的是哪个号，是这里要回答的问题。
//
// settings 是订阅插件的设置（凭据列表、仓库绑定、公共 Token），另外约定带上发布插件的
// github_auth_mode——默认凭据走 Token 还是 gh 由它决定。
func (p *RepositoryWatchPlugin) TestCredentials(ctx context.Context, settings SettingValues) []CredentialCheck {
	ghToken := p.ghAuthToken
	if ghToken == nil {
		ghToken = repositoryPublishGHAuthToken
	}
	bindings := parseRepositoryCredentialBindings(settings.String(repositoryCredentialSettingBindings, ""))
	boundRepositories := map[string][]string{}
	for repository, id := range bindings {
		boundRepositories[id] = append(boundRepositories[id], repository)
	}
	for id := range boundRepositories {
		sort.Strings(boundRepositories[id])
	}

	checks := []CredentialCheck{}
	publicToken := strings.TrimSpace(settings.String(repositoryWatchSettingToken, ""))
	mode := repositoryPublishAuthMode(settings)
	switch {
	case mode == repositoryPublishAuthToken || mode == repositoryPublishAuthAuto && publicToken != "":
		checks = append(checks, p.checkGitHubToken(ctx, RepositoryCredentialDefaultKey, "默认凭据", publicToken, nil))
	default:
		checks = append(checks, p.checkGitHubGH(ctx, RepositoryCredentialDefaultKey, "默认凭据", ghToken, nil))
	}

	tokens := parseRepositoryCredentialTokens(settings.String(repositoryCredentialSettingTokens, ""))
	for _, credential := range parseRepositoryCredentials(settings.String(repositoryCredentialSettingList, "")) {
		repositories := boundRepositories[credential.ID]
		if credential.authMode() == repositoryCredentialAuthGH {
			checks = append(checks, p.checkGitHubGH(ctx, credential.ID, credential.label(), ghToken, repositories))
			continue
		}
		checks = append(checks, p.checkGitHubToken(ctx, credential.ID, credential.label(), tokens[credential.ID], repositories))
	}
	return checks
}

func (p *RepositoryWatchPlugin) checkGitHubGH(ctx context.Context, key, label string, ghToken func(context.Context) (string, error), repositories []string) CredentialCheck {
	if ghToken == nil {
		return CredentialCheck{Key: key, Label: label, Configured: true, State: CredentialError, Message: "服务器上没有可用的 GitHub CLI（gh）。"}
	}
	probeCtx, cancel := context.WithTimeout(ctx, credentialCheckTimeout)
	token, err := ghToken(probeCtx)
	cancel()
	token = strings.TrimSpace(token)
	if errors.Is(err, errRepositoryPublishGHUnavailable) {
		return CredentialCheck{Key: key, Label: label, Configured: true, State: CredentialError, Message: "服务器上没有安装 GitHub CLI（gh）。"}
	}
	if err != nil || token == "" {
		return CredentialCheck{Key: key, Label: label, Configured: true, State: CredentialInvalid, Message: "gh 没有登录，或取不到登录 Token；在服务器上执行 gh auth login。"}
	}
	check := p.checkGitHubToken(ctx, key, label, token, repositories)
	check.Message = strings.TrimSpace("通过服务器 gh CLI。" + check.Message)
	return check
}

func (p *RepositoryWatchPlugin) checkGitHubToken(ctx context.Context, key, label, token string, repositories []string) CredentialCheck {
	token = strings.TrimSpace(token)
	if token == "" {
		check := unconfiguredCredential(key, label)
		if key == RepositoryCredentialDefaultKey {
			check.Message = "未填写 Token，公开仓库会匿名读取，请求额度较低。"
		} else {
			check.Message = "未填写 Token，绑定到它的仓库会改用默认凭据。"
		}
		return check
	}
	check := CredentialCheck{Key: key, Label: label, Configured: true}
	status, body, err := p.fetchGitHubCredentialProbe(ctx, "/user", token)
	var user struct {
		Login string `json:"login"`
	}
	switch {
	case err != nil:
		check.State, check.Message = CredentialError, "连不上 GitHub，稍后再试。"
		return check
	case status == http.StatusUnauthorized:
		check.State, check.Message = CredentialInvalid, "Token 无效或已过期。"
		return check
	case status != http.StatusOK || json.Unmarshal([]byte(body), &user) != nil || strings.TrimSpace(user.Login) == "":
		check.State, check.Message = CredentialError, "GitHub 没有返回账号信息。"
		return check
	}
	check.State, check.Account = CredentialValid, strings.TrimSpace(user.Login)
	check.Message = p.describeRepositoryAccess(ctx, token, repositories)
	return check
}

// describeRepositoryAccess 用同一份凭据看绑定仓库的权限。GitHub 对无权访问的私有仓库
// 同样回 404，所以「看不到」不区分仓库不存在还是没授权。
func (p *RepositoryWatchPlugin) describeRepositoryAccess(ctx context.Context, token string, repositories []string) string {
	if len(repositories) == 0 {
		return ""
	}
	parts := make([]string, 0, len(repositories))
	for index, repository := range repositories {
		if index >= repositoryCredentialProbeRepositoryLimit {
			parts = append(parts, "等 "+itoa(len(repositories))+" 个仓库")
			break
		}
		status, body, err := p.fetchGitHubCredentialProbe(ctx, "/repos/"+repository, token)
		var meta struct {
			FullName    string `json:"full_name"`
			Permissions struct {
				Push bool `json:"push"`
			} `json:"permissions"`
		}
		name := repository
		switch {
		case err != nil:
			parts = append(parts, name+" 检测失败")
		case status == http.StatusOK && json.Unmarshal([]byte(body), &meta) == nil:
			if strings.TrimSpace(meta.FullName) != "" {
				name = meta.FullName
			}
			if meta.Permissions.Push {
				parts = append(parts, name+" 可写")
			} else {
				parts = append(parts, name+" 只读")
			}
		default:
			parts = append(parts, name+" 看不到")
		}
	}
	return "绑定仓库：" + strings.Join(parts, "、") + "。"
}

func (p *RepositoryWatchPlugin) fetchGitHubCredentialProbe(ctx context.Context, path, token string) (int, string, error) {
	return fetchCredentialProbe(ctx, p.client, p.baseURL+path, map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
		"User-Agent":           "Diana-Repository-Watch",
		"Authorization":        "Bearer " + token,
	})
}
