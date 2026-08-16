// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

var (
	publicErrorURLPattern         = regexp.MustCompile(`(?i)\b(?:https?|wss?)://[^\s"'<>]+`)
	publicErrorHostPattern        = regexp.MustCompile(`(?i)\b(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+(?:[a-z]{2,63}|example|test|local)(?::[0-9]{1,5})?\b`)
	publicErrorAuthorization      = regexp.MustCompile(`(?i)\b(?:proxy-)?authorization\s*[:=]\s*(?:(?:bearer|token|basic)\s+)?[^\s,;\)\]}'"]+`)
	publicErrorBearerPattern      = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[a-z0-9._~+/=-]{6,}`)
	publicErrorGitHubTokenPattern = regexp.MustCompile(`\b(?:gh[pousr]_|github_pat_)[A-Za-z0-9_]{12,}\b`)
	publicErrorCredentialPattern  = regexp.MustCompile(`(?i)\b(api[_-]?key|access[_-]?token|refresh[_-]?token|token|secret|password|passwd|signature|credential|cookie)\s*[:=]\s*["']?[^\s,;&"'\)\]}]+["']?`)
	publicErrorJSONCredential     = regexp.MustCompile(`(?i)(["'](?:api[_-]?key|access[_-]?token|refresh[_-]?token|token|secret|password|passwd|signature|credential|cookie)["']\s*:\s*)["'][^"'\r\n]+["']`)
	publicErrorUnixPathPattern    = regexp.MustCompile(`(?:/Users|/home|/var|/private|/tmp)/[^\s"'<>]+`)
)

// publicQQErrorMessage keeps operational details in logs while returning only
// a safe, useful summary to QQ users.
func publicQQErrorMessage(err error) string {
	if err == nil {
		return "请求处理失败，请稍后重试。"
	}
	if errors.Is(err, errImageMediaUnavailable) {
		return "图片读取失败：原图片地址不可用，OneBot v11 回退也没有取得可读取的本地文件或下载地址。请重新发送图片后再试。"
	}
	raw := strings.TrimSpace(err.Error())
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "github") {
		for _, marker := range []string{"请求额度", "rate limit", "限流", "too many requests"} {
			if strings.Contains(lower, marker) {
				return "GitHub API 请求额度已耗尽；公开仓库的匿名访问也受限。请管理员前往「插件 → 仓库更新订阅 → 设置」配置 GitHub Token，或等待额度恢复。"
			}
		}
		if strings.Contains(lower, "token 无效") || strings.Contains(lower, "token 已过期") {
			return "GitHub Token 无效或已过期，请管理员前往「插件 → 仓库更新订阅 → 设置」重新配置。"
		}
		if strings.Contains(lower, "contents: read") || strings.Contains(lower, "token 权限不足") {
			return "GitHub 仓库不可访问，请管理员检查仓库地址，并在「插件 → 仓库更新订阅 → 设置」确认 Token 已获得目标仓库的 Contents: read 权限。"
		}
	}
	if strings.Contains(lower, "client.timeout exceeded while awaiting headers") ||
		strings.Contains(lower, "timeout awaiting response headers") {
		return "上游模型服务响应超时，请稍后重试。"
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(lower, "context deadline exceeded") {
		return "上游模型服务请求超时，请稍后重试。"
	}
	if strings.Contains(lower, "output is empty") {
		return "上游模型服务暂时没有返回有效内容，请稍后重试。"
	}
	return sanitizePublicErrorDetail(raw)
}

func sanitizePublicErrorDetail(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "请求处理失败，请稍后重试。"
	}
	value = publicErrorJSONCredential.ReplaceAllString(value, `${1}"[REDACTED]"`)
	value = publicErrorAuthorization.ReplaceAllString(value, "Authorization=[REDACTED]")
	value = publicErrorBearerPattern.ReplaceAllString(value, "Bearer [REDACTED]")
	value = publicErrorGitHubTokenPattern.ReplaceAllString(value, "[REDACTED_TOKEN]")
	value = publicErrorCredentialPattern.ReplaceAllString(value, `${1}=[REDACTED]`)
	value = publicErrorURLPattern.ReplaceAllString(value, "[REDACTED_URL]")
	value = publicErrorHostPattern.ReplaceAllString(value, "[REDACTED_HOST]")
	value = publicErrorUnixPathPattern.ReplaceAllString(value, "[REDACTED_PATH]")
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "请求处理失败，请稍后重试。"
	}
	return value
}
