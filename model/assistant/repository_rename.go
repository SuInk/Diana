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

// repositoryRenameLocationPattern 圈出 GitHub 改名重定向的落点：REST 对旧名回 301，
// Location 指向按数字 ID 寻址的 /repositories/{id}，那里的 full_name 就是新名字。
var repositoryRenameLocationPattern = regexp.MustCompile(`^/repositories/[0-9]+$`)

// explainRepositoryRename 把没能自动跟过去的「仓库改名」从笼统的失败里认出来。
//
// 改名后 REST 回 301（redirect_refused），GraphQL 却悄悄跟到新仓库，返回的链接对不上
// 旧名，于是报成 invalid_response——群里看到的就是一句「无法解析的响应」，模型只会让人
// 等网络好了再发，确认码回多少遍都停在同一处。读操作和草稿审批会自己跟到新名字（见
// Run 和 executeDraft）；走到这里的是跟不过去的：上一次写入结果不确定、跟过去之后又被
// 重定向，或者别的调用方。这时至少把新名字和下一步说清楚。
func (t *dianaGitHubTool) explainRepositoryRename(ctx context.Context, result repositoryIssueResult) repositoryIssueResult {
	if result.OK || result.Repository == "" || (result.FailureCode != "redirect_refused" && result.FailureCode != "invalid_response") {
		return result
	}
	renamed, ok := t.resolveRepositoryRename(ctx, result.Repository)
	if !ok {
		return result
	}
	result.FailureCode = "repository_renamed"
	result.RedirectRepository = renamed
	if t.plugin.repositoryUncertain(result.Repository) {
		result.Message = fmt.Sprintf("仓库 %s 已在 GitHub 上改名或转移为 %s。之前有一次写入结果不确定，为免重复写入，没有自动改投到新名字；"+
			"请先到 %s 上确认那次是否已经写进去，没写进去再重新提一份草稿。", result.Repository, renamed, renamed)
		return result
	}
	result.Message = fmt.Sprintf("仓库 %s 已在 GitHub 上改名或转移为 %s，这次没有执行。把 repository 换成 %s 重发即可。",
		result.Repository, renamed, renamed)
	return result
}

// followableRename 判断一次失败的写入能不能换新名字再来一次：失败必须是改名的样子，
// GitHub 确认改过名，而且旧名下没有结果不确定的写入。最后一条是防重复：幂等标记和
// 操作键都含仓库名，旧名下一次说不清落没落地的写入，换新名字查不到它的标记。
func (t *dianaGitHubTool) followableRename(ctx context.Context, repository string, result repositoryIssueResult) (string, bool) {
	if result.OK || len(result.Items) > 0 || (result.FailureCode != "redirect_refused" && result.FailureCode != "invalid_response") {
		return "", false
	}
	if t.plugin.repositoryUncertain(repository) {
		return "", false
	}
	renamed, ok := t.resolveRepositoryRename(ctx, repository)
	if !ok {
		return "", false
	}
	t.noteRename(repository, renamed)
	return renamed, true
}

// retargetDraft 把草稿改投到新名字并先落盘：这次写入没成的话，下一次回确认码也直接
// 走新名字，不必再撞一次旧名。
func (t *dianaGitHubTool) retargetDraft(ctx context.Context, draft *repositoryIssueDraft, renamed string) (string, string) {
	t.noteRename(draft.Repository, renamed)
	draft.Repository = renamed
	if err := t.plugin.updateDraft(ctx, *draft); err != nil {
		return "draft_store_failed", "草稿改投到新仓库名时保存失败。"
	}
	return "", ""
}

func (t *dianaGitHubTool) noteRename(previous, renamed string) {
	if t.renamedFrom == nil {
		t.renamedFrom = map[string]string{}
	}
	t.renamedFrom[strings.ToLower(renamed)] = previous
}

// previousName 返回这次调用里已确认的旧名，没有就是空串。
func (t *dianaGitHubTool) previousName(repository string) string {
	if t == nil {
		return ""
	}
	return t.renamedFrom[strings.ToLower(strings.TrimSpace(repository))]
}

func repositoryRenameNote(previous, renamed string) string {
	return fmt.Sprintf("仓库 %s 已在 GitHub 上改名或转移为 %s，已按新名字执行。", previous, renamed)
}

// finishRead 给跟到新名字的读操作结果补上说明，再照常收尾。
func (t *dianaGitHubTool) finishRead(ctx context.Context, redirectedFrom string, result repositoryIssueResult) (string, error) {
	if redirectedFrom != "" {
		result.RedirectRepository = result.Repository
		result.Message = repositoryRenameNote(redirectedFrom, result.Repository) + result.Message
	}
	return t.finish(ctx, result)
}

// repositoryRenameCandidates 在授权检查里兼认改名前后两个名字。设置里写的可能还是
// 旧名（还没人去改），也可能已经换成新名而草稿记的是旧名；GitHub 确认过它们是同一个
// 仓库，哪个名字过了检查都算。只有原名没过时才去问 GitHub，问一次就记住。
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
		if renamed, ok := c.tool.resolveRepositoryRename(ctx, c.original); ok {
			c.renamed = renamed
			c.tool.noteRename(c.original, renamed)
		}
	}
	if c.renamed != "" {
		if renamedCode, _ := fn(c.renamed); renamedCode == "" {
			return "", ""
		}
	}
	return code, message
}

// resolveRepositoryRename 问 GitHub 这个仓库现在叫什么。只有确认改过名（或转移过）
// 才返回 ok；查不到、没改名、网络失败都当没改名处理，原来的失败照常报出去。
func (t *dianaGitHubTool) resolveRepositoryRename(ctx context.Context, repository string) (string, bool) {
	if t == nil || t.plugin == nil || t.plugin.client == nil || strings.TrimSpace(repository) == "" {
		return "", false
	}
	// 私有仓库要凭据才看得见；凭据按旧名取，改名前绑定的就是这个名字。
	token, _ := t.repositoryPublishCredential(ctx, repository)
	path := "/repos/" + repository
	for hop := 0; hop < 2; hop++ {
		location, fullName := t.probeRepositoryName(ctx, path, token)
		if fullName != "" {
			renamed, err := normalizeGitHubRepository(fullName)
			if err != nil || strings.EqualFold(renamed, repository) {
				return "", false
			}
			return renamed, true
		}
		if location == "" {
			return "", false
		}
		path = location
	}
	return "", false
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
