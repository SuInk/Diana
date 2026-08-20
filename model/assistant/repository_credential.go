// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"strings"
)

// 以前只有一个「公共 Token」，个人仓库、组织仓库、协作仓库全挤在同一份凭据上：
// fine-grained token 想覆盖组织仓库就得额外授权，classic token 又把权限放得过大。
// 这里把凭据做成一份列表，每个仓库在「仓库管理」里选用哪一个；没选的仓库继续走公共
// Token，所以旧配置不受影响。
//
// 存储拆成三项，沿用插件既有的「密钥单独存、明文部分另存一份供界面回显」的做法：
// 密钥设置项不会回显，界面读不回来，凭据的名字和类型必须放在非密钥项里。
const (
	repositoryCredentialSettingList     = "github_credentials"
	repositoryCredentialSettingTokens   = "github_credential_tokens"
	repositoryCredentialSettingBindings = "repository_credentials"
	// 密钥项不会回显，界面无从判断哪条凭据已经填过 Token；这里额外存一份纯 ID 列表，
	// 和「已配置 Token 的用户」是同一套做法。
	repositoryCredentialSettingConfigured = "github_credential_ids"

	repositoryCredentialAuthToken = "token"
	repositoryCredentialAuthGH    = "gh"
)

// repositoryCredential 是一条凭据的元信息，不含 Token 本身。
type repositoryCredential struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Auth string `json:"auth"`
}

func (c repositoryCredential) authMode() string {
	if strings.EqualFold(strings.TrimSpace(c.Auth), repositoryCredentialAuthGH) {
		return repositoryCredentialAuthGH
	}
	return repositoryCredentialAuthToken
}

// label 给报错用，优先显示用户起的名字，没起名就退回 ID。
func (c repositoryCredential) label() string {
	if name := strings.TrimSpace(c.Name); name != "" {
		return name
	}
	return strings.TrimSpace(c.ID)
}

// parseRepositoryCredentials 解析凭据列表。配置坏掉时返回空列表而不是报错：凭据是
// 可选增强，解析失败应当退回公共 Token，而不是让所有仓库一起失效。
func parseRepositoryCredentials(raw string) []repositoryCredential {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var items []repositoryCredential
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil
	}
	out := make([]repositoryCredential, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, repositoryCredential{ID: id, Name: strings.TrimSpace(item.Name), Auth: item.authMode()})
	}
	return out
}

func parseRepositoryCredentialTokens(raw string) map[string]string {
	tokens := map[string]string{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return tokens
	}
	if err := json.Unmarshal([]byte(raw), &tokens); err != nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(tokens))
	for id, token := range tokens {
		id, token = strings.TrimSpace(id), strings.TrimSpace(token)
		if id == "" || token == "" {
			continue
		}
		out[id] = token
	}
	return out
}

// parseRepositoryCredentialBindings 返回仓库到凭据 ID 的绑定，键统一小写，便于与
// GitHub 大小写不敏感的仓库名比对。
func parseRepositoryCredentialBindings(raw string) map[string]string {
	bindings := map[string]string{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return bindings
	}
	if err := json.Unmarshal([]byte(raw), &bindings); err != nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(bindings))
	for repository, id := range bindings {
		repository, id = strings.TrimSpace(repository), strings.TrimSpace(id)
		if repository == "" || id == "" {
			continue
		}
		out[strings.ToLower(repository)] = id
	}
	return out
}

// repositoryCredentialFor 找出某个仓库该用哪条凭据。没有绑定、绑定指向已删除的凭据，
// 或者凭据是 token 类型却没填 Token 时，都返回 false，交给调用方回落到公共 Token ——
// 宁可沿用旧行为，也不要因为一处配置残缺就让仓库彻底不可用。
func repositoryCredentialFor(repository string, settings SettingValues) (repositoryCredential, string, bool) {
	repository = strings.ToLower(strings.TrimSpace(repository))
	if repository == "" {
		return repositoryCredential{}, "", false
	}
	bindings := parseRepositoryCredentialBindings(settings.String(repositoryCredentialSettingBindings, ""))
	id := bindings[repository]
	if id == "" {
		return repositoryCredential{}, "", false
	}
	var credential repositoryCredential
	for _, item := range parseRepositoryCredentials(settings.String(repositoryCredentialSettingList, "")) {
		if item.ID == id {
			credential = item
			break
		}
	}
	if credential.ID == "" {
		return repositoryCredential{}, "", false
	}
	if credential.authMode() == repositoryCredentialAuthGH {
		return credential, "", true
	}
	token := parseRepositoryCredentialTokens(settings.String(repositoryCredentialSettingTokens, ""))[credential.ID]
	if strings.TrimSpace(token) == "" {
		return repositoryCredential{}, "", false
	}
	return credential, token, true
}

// repositoryFromGitHubAPIPath 从 GitHub API 路径里取出 owner/repo。两个插件的请求都
// 长成 /repos/{owner}/{repo}/...，从路径反推比把 repository 参数一路传进十几个调用点
// 更省事，也不会漏掉某一条。
func repositoryFromGitHubAPIPath(path string) string {
	path = strings.TrimSpace(path)
	if index := strings.IndexAny(path, "?#"); index >= 0 {
		path = path[:index]
	}
	const prefix = "/repos/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	parts := strings.Split(strings.Trim(path[len(prefix):], "/"), "/")
	if len(parts) < 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return ""
	}
	return parts[0] + "/" + parts[1]
}
