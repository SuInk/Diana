// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
)

// 生图、改图是群友点名要的东西，后台任务失败后以前只有一句通用的「后台任务执行失败」，
// 看不出是被审核拦了、限流了还是网络断了，也不知道该怎么办。这里把失败归成几类，
// 每类配一句人能照着做的处理建议；发不发仍由「错误提示」开关决定，和别的诊断一致。

// imageFailureAdvice 是一次生图失败对外的说法。Category 进运行日志和调试记录，
// 方便按类统计；Reason、Advice 发进聊天，不含地址、密钥这类细节。
type imageFailureAdvice struct {
	Category string
	Reason   string
	Advice   string
}

func classifyImageFailure(err error, timedOut bool, timeout time.Duration) imageFailureAdvice {
	if timedOut {
		return imageFailureAdvice{
			Category: "task_timeout",
			Reason:   fmt.Sprintf("超过任务时限（%s）还没画完，已停止", formatCodingDuration(timeout)),
			Advice:   "稍后再试一次；经常这样的话请管理员调大图片模型的超时时间，或换一个更快的图片模型。",
		}
	}
	if err == nil {
		return imageFailureAdvice{Category: "unknown", Reason: "原因未知", Advice: "稍后再试一次。"}
	}
	lower := strings.ToLower(err.Error())
	containsAny := func(markers ...string) bool {
		for _, marker := range markers {
			if strings.Contains(lower, marker) {
				return true
			}
		}
		return false
	}
	switch {
	case errors.Is(err, errImageEditSourceNotFound):
		return imageFailureAdvice{
			Category: "source_missing",
			Reason:   "没找到要改的那张图",
			Advice:   "重新发一下图片，或者引用那张图再说要怎么改。",
		}
	case containsAny("llm profile store is not configured", "image model is not configured", "no image provider"):
		return imageFailureAdvice{
			Category: "not_configured",
			Reason:   "还没有配置可用的图片模型",
			Advice:   "请管理员在「模型」页给图片生成指定一个支持生图的配置档。",
		}
	case errors.Is(err, errContentPolicyRejection) || isContentPolicyRejection(err) ||
		containsAny("moderation_blocked", "safety system", "image_generation_user_error"):
		return imageFailureAdvice{
			Category: "content_policy",
			Reason:   "图片接口的安全审核拒绝了这次请求（不代表已经确认请求违规）",
			Advice:   "换个说法、去掉可能被误判的描述后再试。",
		}
	case containsAny("status=401", "status=403", " 401 ", " 403 ", "unauthorized", "forbidden", "invalid api key", "invalid_api_key", "incorrect api key"):
		return imageFailureAdvice{
			Category: "auth",
			Reason:   "图片接口鉴权失败（API Key 无效、过期，或没有图片权限）",
			Advice:   "请管理员检查图片模型配置档的 API Key 和权限。",
		}
	case containsAny("model_not_found", "does not exist", "no such model", "unsupported model", "status=404", " 404 "):
		return imageFailureAdvice{
			Category: "model_not_found",
			Reason:   "图片接口找不到这个模型，或不支持这个操作",
			Advice:   "请管理员确认图片模型名写对了，并且所用的接口支持生图/改图。",
		}
	case containsAny("status=429", " 429 ", "rate limit", "rate_limit", "too many requests", "quota", "insufficient", "billing"):
		return imageFailureAdvice{
			Category: "rate_limited",
			Reason:   "图片接口限流，或额度用完了",
			Advice:   "过几分钟再试；一直这样的话请管理员检查额度，或给图片生成加一个备用配置档。",
		}
	case strings.Contains(lower, "invalid_image_file") || strings.Contains(lower, "invalid image data"):
		return imageFailureAdvice{
			Category: "invalid_source",
			Reason:   "图片接口不接受这张原图（格式不支持或数据损坏）",
			Advice:   "换成 PNG 或 JPEG 图片后再试。",
		}
	case isTimeoutError(err) || containsAny("tls handshake timeout", "i/o timeout"):
		return imageFailureAdvice{
			Category: "timeout",
			Reason:   "连接图片接口超时",
			Advice:   "稍后再试；经常这样的话请管理员检查到图片接口的网络（代理、中转），或调大图片超时。",
		}
	case containsAny("eof", "connection reset", "connection refused", "broken pipe", "no such host",
		"status=500", "status=502", "status=503", "status=504", "status=520", " 500 ", " 502 ", " 503 ", " 504 ", " 520 ", "bad gateway", "service unavailable"):
		return imageFailureAdvice{
			Category: "upstream_unavailable",
			Reason:   "和图片接口的连接断开了，或上游服务暂时不可用",
			Advice:   "稍后再试；反复出现的话请管理员检查中转服务和网络代理。",
		}
	case containsAny("图片接口没有返回图片", "image output is empty", "no image"):
		return imageFailureAdvice{
			Category: "empty_output",
			Reason:   "图片接口这次没有返回图片",
			Advice:   "换个描述再试；反复出现的话请管理员确认所用模型支持生图。",
		}
	}
	return imageFailureAdvice{
		Category: "other",
		Reason:   strings.TrimRight(publicTaskErrorMessage(err), "。"),
		Advice:   "稍后再试；仍然失败的话请管理员到「记录」里查看这条消息的调试记录。",
	}
}

// message 拼出发进聊天的那条提示。任务名区分「图片生成」「图片编辑」。
func (a imageFailureAdvice) message(taskName string) string {
	name := strings.TrimSpace(taskName)
	if name == "" {
		name = "图片生成"
	}
	return fmt.Sprintf("%s失败：%s。\n处理建议：%s", name, strings.TrimRight(a.Reason, "。"), a.Advice)
}

// personaSource 是交给人设改写的那句。只给群友听得懂、能照做的部分：处理建议里
// 「请管理员检查配置档、额度、代理」这类话不该经由角色口吻说出来，只有换个说法、
// 重发原图这几类群友自己能做的留下。
func (a imageFailureAdvice) personaSource(taskName string) string {
	name := strings.TrimSpace(taskName)
	if name == "" {
		name = "图片生成"
	}
	text := fmt.Sprintf("%s失败：%s。", name, strings.TrimRight(a.Reason, "。"))
	switch a.Category {
	case "content_policy", "source_missing", "invalid_source":
		text += a.Advice
	}
	return text
}

// imageFailurePersonaAllowed 报告错误提示关着、但主人开了「出错时仍用人设回一句」。
// 停用的机器人和插件自己说了不要发错误的，照样不出声。
func (r *Runtime) imageFailurePersonaAllowed(event MessageEvent, pluginID string) bool {
	if r.profileDisabled(event.ProfileID) || r.errorNoticeAllowed(event) || !r.pluginErrorNoticeAllowed(event, pluginID) {
		return false
	}
	return boolValue(r.effectiveConfigForEvent(event).ErrorPersonaReplyEnabled, false)
}

// handleImageTaskFailure 记下生图失败的分类和建议，并在开关允许时发进聊天。
func (r *Runtime) handleImageTaskFailure(ctx context.Context, item reservedSubagentTask, err error, timedOut bool, timeout time.Duration) {
	advice := classifyImageFailure(err, timedOut, timeout)
	notice := advice.message(item.task.Name)
	pluginID := subagentTaskPluginID(item)
	allowed := r.diagnosticAllowed(item.event, pluginID)
	sent, persona := false, false
	var sendErr error
	if allowed {
		sendErr = r.sendDiagnosticFollowup(ctx, item.event, pluginID, notice)
		sent = sendErr == nil
	} else if r.imageFailurePersonaAllowed(item.event, pluginID) {
		// 错误提示关了、但开着「出错时仍用人设回一句」：和主回复出错一样，让模型按人设
		// 说一句没画出来。不然群友只看到「在画了」，之后再没下文。改写不成就保持静默，
		// 不退回带「处理建议」的原文。
		cfg := r.effectiveConfigForEvent(item.event)
		if rewritten, ok := r.rewriteNoticeText(ctx, item.event, advice.personaSource(item.task.Name), cfg.PromptOverrides.text(promptErrorNoticeSpec), PurposeErrorNotice); ok {
			persona = true
			notice = rewritten
			sendErr = r.sendSubagentFollowup(withoutCarryOverDelivery(ctx), item.event, rewritten)
			sent = sendErr == nil
		}
	}
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	metadata := map[string]any{
		"kind":           item.task.Kind,
		"name":           item.task.Name,
		"task_id":        item.id,
		"group_id":       item.event.GroupID,
		"user_id":        item.event.UserID,
		"message_id":     item.event.MessageID,
		"category":       advice.Category,
		"reason":         advice.Reason,
		"advice":         advice.Advice,
		"notice":         notice,
		"notice_sent":    sent,
		"notice_switch":  allowed,
		"notice_persona": persona,
		"timed_out":      timedOut,
	}
	if label := llmProviderAttemptLabel(err); label != "" {
		metadata["provider_label"] = label
	}
	if sendErr != nil {
		metadata["notice_error"] = sendErr.Error()
	}
	if writer := r.appLogWriter(); writer != nil {
		logCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = writer.AppendLog(logCtx, applog.Entry{
			Kind:     applog.KindError,
			Level:    applog.LevelError,
			Action:   "image_task_failed",
			Message:  item.task.Name + "失败",
			Detail:   detail,
			Actor:    oneBotEventActor(item.event),
			Target:   item.id,
			Metadata: metadata,
		})
		cancel()
	}
	traceMetadata := map[string]any{
		"phase":       "image_task_failed",
		"purpose":     item.task.Name,
		"category":    advice.Category,
		"reason":      advice.Reason,
		"advice":      advice.Advice,
		"notice":      notice,
		"notice_sent": sent,
		"error":       detail,
	}
	if label, ok := metadata["provider_label"]; ok {
		traceMetadata["provider_label"] = label
	}
	r.recordDebugTrace(item.debugTrace, "图片任务失败", traceMetadata)
}
