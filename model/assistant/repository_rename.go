// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// 仓库改名之后，草稿、设置、凭据绑定里写的都还是旧名。executeDraft 已经会在写入前
// 问一次新名字（currentRepositoryName）；这里补上它够不着的几处：没有 Token 时怎么
// 认出改名、设置已经换成新名时授权怎么认、按旧名绑定的凭据怎么找、读操作怎么跟过去。

// repositoryRenameLocationPattern 圈出 GitHub 改名重定向的落点：REST 对旧名回 301，
// Location 指向按数字 ID 寻址的 /repositories/{id}，那里的 full_name 就是新名字。
var repositoryRenameLocationPattern = regexp.MustCompile(`^/repositories/[0-9]+$`)

// restRepositoryName 在 GraphQL 用不上（没有 Token、查询失败）时，靠 REST 的重定向
// 认出新名字：旧名 → 301 → /repositories/{id} → full_name。公开仓库不带凭据也能问。
func (t *dianaGitHubTool) restRepositoryName(ctx context.Context, repository string) string {
	token, credentialErr := t.repositoryPublishCredential(ctx, repository)
	if credentialErr != nil {
		token = ""
	}
	path := "/repos/" + repository
	for hop := 0; hop < 2; hop++ {
		location, fullName := t.probeRepositoryName(ctx, path, token)
		if fullName != "" {
			if _, _, ok := strings.Cut(fullName, "/"); ok {
				return fullName
			}
			return ""
		}
		if location == "" {
			return ""
		}
		path = location
	}
	return ""
}

// probeRepositoryName 读一次仓库元信息：200 返回 full_name；重定向只接受同一 API 主机
// 上的 /repositories/{id}，返回它的路径供下一跳使用。
func (t *dianaGitHubTool) probeRepositoryName(ctx context.Context, path, token string) (string, string) {
	requestCtx, cancel := context.WithTimeout(ctx, t.requestTimeout())
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, t.plugin.baseURL+path, nil)
	if err != nil {
		return "", ""
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "Diana-Repository-Issues")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := t.plugin.client.Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var meta struct {
			FullName string `json:"full_name"`
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, repositoryIssueResponseLimit))
		if err != nil || json.Unmarshal(body, &meta) != nil {
			return "", ""
		}
		return "", strings.TrimSpace(meta.FullName)
	case http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		base, err := url.Parse(t.plugin.baseURL)
		if err != nil {
			return "", ""
		}
		location, err := url.Parse(strings.TrimSpace(resp.Header.Get("Location")))
		if err != nil {
			return "", ""
		}
		target := base.ResolveReference(location)
		if !strings.EqualFold(target.Scheme, base.Scheme) || !strings.EqualFold(target.Host, base.Host) {
			return "", ""
		}
		next := strings.TrimPrefix(target.Path, strings.TrimRight(base.Path, "/"))
		if !repositoryRenameLocationPattern.MatchString(next) {
			return "", ""
		}
		return next, ""
	}
	return "", ""
}

// renamedRepository 返回 GitHub 确认过的新名字；没改名、只差大小写或者问不出来都返回
// false。同一次调用里问过的仓库不再问第二遍，确认改名后记下旧名供凭据查找兼认。
//
// 只在已经碰壁之后调用（授权没过、读操作收到重定向），所以 GraphQL 问不到时可以再花
// 一次 REST 请求去认。
func (t *dianaGitHubTool) renamedRepository(ctx context.Context, repository string) (string, bool) {
	current := t.currentRepositoryName(ctx, repository)
	if current == "" {
		current = t.restRepositoryName(ctx, repository)
		if current != "" && !strings.EqualFold(current, repository) {
			t.noteRename(repository, current)
		}
	}
	if current == "" || strings.EqualFold(current, repository) {
		return "", false
	}
	return current, true
}

func (t *dianaGitHubTool) noteRename(previous, renamed string) {
	if t.renamedFrom == nil {
		t.renamedFrom = map[string]string{}
	}
	t.renamedFrom[strings.ToLower(strings.TrimSpace(renamed))] = previous
}

// previousName 返回这次调用里已确认的旧名，没有就是空串。
func (t *dianaGitHubTool) previousName(repository string) string {
	if t == nil {
		return ""
	}
	return t.renamedFrom[strings.ToLower(strings.TrimSpace(repository))]
}

// repositoryRenameCandidates 在授权检查里兼认改名前后两个名字。设置里写的可能还是
// 旧名（还没人去改），也可能已经换成新名而草稿记的是旧名；GitHub 确认过它们是同一个
// 仓库，哪个名字过了检查都算。只有原名没过时才去问 GitHub。
type repositoryRenameCandidates struct {
	tool     *dianaGitHubTool
	original string
	renamed  string
	lookedUp bool
}

func (c *repositoryRenameCandidates) check(ctx context.Context, fn func(string) (string, string)) (string, string) {
	code, message := fn(c.original)
	if code == "" {
		return "", ""
	}
	if !c.lookedUp {
		c.lookedUp = true
		c.renamed, _ = c.tool.renamedRepository(ctx, c.original)
	}
	if c.renamed != "" {
		if renamedCode, _ := fn(c.renamed); renamedCode == "" {
			return "", ""
		}
	}
	return code, message
}

func repositoryRenameNote(previous, renamed string) string {
	return fmt.Sprintf("仓库 %s 已改名为 %s，已按新名执行。", previous, renamed)
}

// finishRead 给跟到新名字的读操作结果补上说明，再照常收尾。
func (t *dianaGitHubTool) finishRead(ctx context.Context, redirectedFrom string, result repositoryIssueResult) (string, error) {
	if redirectedFrom != "" {
		result.Message = repositoryRenameNote(redirectedFrom, result.Repository) + result.Message
	}
	return t.finish(ctx, result)
}
