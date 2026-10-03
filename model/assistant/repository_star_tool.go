package assistant

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

func (t *dianaGitHubTool) validateStarAccess(ctx context.Context) (string, string) {
	if !t.settings.Bool(repositoryPublishSettingStarEnabled, false) {
		return "star_disabled", "GitHub Star 默认关闭，请主人在插件设置中开启后再提出请求。"
	}
	if !t.trustedWebOwner && (t.runtime == nil || !t.runtime.relationshipPolicy(ctx, t.event).Owner) {
		return "permission_denied", "GitHub Star 仅限主人提出和确认，仓库 Issue 授权不包含账号 Star 权限。"
	}
	return "", ""
}

func (t *dianaGitHubTool) star(ctx context.Context, repository string) repositoryIssueResult {
	result := repositoryIssueResult{Operation: "star", Repository: repository}
	if code, message := t.validateStarAccess(ctx); code != "" {
		return result.fail(code, message)
	}
	parts := strings.Split(repository, "/")
	path := "/user/starred/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1])
	// PUT 是幂等操作：网络中断后重新确认也不会重复创建 Star。
	if apiErr := t.doJSONStatus(ctx, http.MethodPut, path, nil, nil, http.StatusNoContent); apiErr != nil {
		return result.fail(apiErr.Code, t.failureMessage(apiErr.Code)+" Star 需要用户账号凭据；Fine-grained Token 需 Starring: write 权限，GitHub App 安装凭据不支持此操作。")
	}
	result.OK = true
	result.Outcome = "starred"
	result.Message = "已为 " + repository + " 点 Star（使用该仓库配置的 GitHub 账号）。重复执行保持已 Star 状态。"
	return result
}
