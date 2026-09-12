// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/SuInk/diana/model/llm"
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

// publicChatErrorMessage keeps operational details in logs while returning only
// a safe, useful summary to chat users.
// accountSafetyPublicNotice 是账号安全拦截唯一对外可见的说法。改写器会把它换成
// 机器人自己的口吻，但两边说的是同一件事，也同样不提被拦下的是什么。
const accountSafetyPublicNotice = "这条回复没通过账号安全检查，我就不发出来了。"

func publicChatErrorMessage(err error) string {
	if err == nil {
		return "请求处理失败，请稍后重试。"
	}
	if errors.Is(err, errImageMediaUnavailable) {
		return publicImageMediaErrorMessage(err)
	}
	if errors.Is(err, errReplyCompression) {
		return "回复超过字数上限，暂时没能压缩完成，请提高上限或稍后重试。"
	}
	// 账号安全拦截的内部理由里写着「候选回复直接评价了什么」——那正是被拦下来的
	// 内容摘要。原样发进群等于把要拦的东西又说了一遍，所以对外只给一句中性说明；
	// 完整理由仍然留在事件记录、LastError 和运行日志里。
	var safetyErr *replyAccountSafetyRejectedError
	if errors.As(err, &safetyErr) {
		return accountSafetyPublicNotice
	}
	raw := strings.TrimSpace(err.Error())
	if errors.Is(err, llm.ErrUnverifiedRejection) {
		return withProviderAttemptLabel("本次请求收到上游拦截提示，未能完成回复；该提示无法确认来自模型还是中转服务，也不能据此判断你的消息违规。", err)
	}
	lower := strings.ToLower(raw)
	if errors.Is(err, errContentPolicyRejection) || isContentPolicyRejection(err) {
		return "上游报告内容安全拦截，这次未能完成处理。该结果不代表已经确认你的请求违规，请管理员查看拦截记录。"
	}
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
		return withProviderAttemptLabel("上游模型服务响应超时，请稍后重试。", err)
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(lower, "context deadline exceeded") {
		return withProviderAttemptLabel("上游模型服务请求超时，请稍后重试。", err)
	}
	if strings.Contains(lower, "output is empty") {
		return withProviderAttemptLabel("上游模型服务暂时没有返回有效内容，请稍后重试。", err)
	}
	return sanitizePublicErrorDetail(raw)
}

// withProviderAttemptLabel 把「哪个配置档、哪个模型」补回那些改写过正文的提示。
// 这几条提示原本只说「上游超时了」，配了多个配置档时看不出该去查哪一个。
func withProviderAttemptLabel(message string, err error) string {
	label := strings.TrimSpace(llmProviderAttemptLabel(err))
	if label == "" {
		return message
	}
	return message + "（" + sanitizePublicErrorDetail(label) + "）"
}

func publicImageMediaErrorMessage(err error) string {
	raw := strings.TrimSpace(err.Error())
	lower := strings.ToLower(raw)
	switch {
	case errors.Is(err, errForwardMediaUnavailable):
		return "合并转发中的媒体读取失败：机器人未能取得其中部分图片或视频的可用副本，这不代表 QQ 中的原内容无法查看。请将需要分析的图片或视频从转发记录中单独发送；若仍失败，请管理员检查转发媒体解析日志。"
	case errors.Is(err, errLLMImageSourceTooLarge):
		return "图片处理失败：原图超过 32 MiB 源文件上限。请压缩图片或降低分辨率后重试。"
	case errors.Is(err, errLLMImageDimensions):
		return "图片处理失败：图片像素尺寸异常或超过 8000 万像素，无法安全解码。请缩小图片后重试。"
	case errors.Is(err, errLLMImageDecodeFailed):
		return "图片处理失败：文件扩展名看起来是图片，但实际编码无法解码。请转换为标准 JPEG、PNG、GIF 或 WebP 后重试。"
	case errors.Is(err, errLLMImagePayloadTooLarge):
		return "图片处理失败：尝试 PNG、多档 JPEG 质量并逐级缩小尺寸后，base64 仍超过 4.5 MiB。请手动压缩图片后重试。"
	case strings.Contains(lower, "status=400"), strings.Contains(lower, "status=401"), strings.Contains(lower, "status=403"), strings.Contains(lower, "status=404"):
		return "图片读取失败：下载地址已失效或拒绝访问（上游返回 HTTP 4xx），OneBot v11 回退也没有取得可用副本。请重新发送原图后重试。"
	case strings.Contains(lower, "deadline exceeded"), strings.Contains(lower, "timeout"):
		return "图片读取失败：下载或读取图片超时。请检查 OneBot v11 连接和网络后重试。"
	case strings.Contains(lower, "no such file"), strings.Contains(lower, "file does not exist"):
		return "图片读取失败：本地缓存文件已经不存在，且没有可用下载地址。请重新发送原图后重试。"
	case strings.Contains(lower, "not an image"), strings.Contains(lower, "unknown format"):
		return "图片读取失败：取得的内容不是可识别的图片格式。请重新发送标准 JPEG、PNG、GIF 或 WebP 文件。"
	case strings.Contains(lower, "source is unavailable"), strings.Contains(lower, "contains no image"), strings.Contains(lower, "has no onebot message"):
		return "图片读取失败：机器人暂未取得可读取的图片副本，不能据此判断原图是否可用。请单独发送该图片后重试；若仍失败，请管理员检查媒体解析日志。"
	default:
		return "图片读取失败，具体原因：" + sanitizePublicErrorDetail(raw) + "。请根据上述原因处理后重试。"
	}
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
