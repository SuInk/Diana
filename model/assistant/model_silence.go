// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
)

// 机器人要能自己决定这一轮不说话。
//
// 在此之前，唯一一条通往沉默的路是 [[DIANA_REFUSE_CURRENT]]，而那是拒答：它仍然
// 发一句看得见的话，还要计进拒答次数、喂给 30 分钟暂停的判断。「我拒绝回答」和
// 「我这轮没什么要补的」是两件完全不同的事，用同一个出口表达，结果就是模型在
// 「硬凑一句告别」和「被记一次拒答」之间二选一——两个都不对。
//
// 私聊收尾的兜底（见 private_closing.go）是事后补救：候选回复已经生成出来，审核
// 判出这是第 N 句告别才拦下。这里是事前的：模型自己在 agent.finalize 上填
// silent=true，本轮什么都不发。两者不重复——静默走在前面，兜底仍然守着模型没有
// 自觉的那些轮。

// errModelSilentFinish 是模型主动静默的哨兵。它不是错误，只是「本轮没有要发的
// 东西」这件事沿着既有的 (reply, err) 通道往上传的方式：replyAndRecord 认出它，
// 记一次 ignored_model_silent 就收工。
var errModelSilentFinish = errors.New("diana: model finished the turn without a message")

// modelSilentFinishReasonPrefix 是事件详情里那句话的开头。原因来自模型填的
// silent_reason，只进事件和日志，不发给用户。
const modelSilentFinishReasonPrefix = "模型判断这轮不需要回复"

// modelSilentFinishReasonRunes 限制记进事件的原因长度：这段文本由模型自由书写，
// 事件详情不该被一整段独白撑开。
const modelSilentFinishReasonRunes = 120

// modelSilentFinishError 带着给事件详情看的中文理由，同时保留可以 errors.Is 的
// 哨兵，写法和 conversationClosedError 保持一致。
type modelSilentFinishError struct {
	reason string
}

func newModelSilentFinishError(reason string) *modelSilentFinishError {
	return &modelSilentFinishError{reason: trimModelSilentReason(reason)}
}

func (e *modelSilentFinishError) Error() string {
	if e == nil {
		return ""
	}
	if e.reason == "" {
		return modelSilentFinishReasonPrefix
	}
	return modelSilentFinishReasonPrefix + "：" + e.reason
}

func (e *modelSilentFinishError) Unwrap() error {
	return errModelSilentFinish
}

func trimModelSilentReason(reason string) string {
	reason = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(reason))
	reason = strings.Join(strings.Fields(reason), " ")
	if runes := []rune(reason); len(runes) > modelSilentFinishReasonRunes {
		return string(runes[:modelSilentFinishReasonRunes]) + "…"
	}
	return reason
}

// modelSilenceRefusedReason 报告这一轮为什么不许静默，允许时返回空串。
//
// 静默是「本轮没有要说的」，前提是本轮真的什么都没发生。一旦这一轮已经在外部
// 系统里留下了痕迹、带着插件事实结果、或者刚刚受理了一个后台图片任务，闭嘴就
// 不是克制而是把已经发生的事咽回去：用户只会看到操作凭空消失。
//
// 主人的响应限制命令（「响应限制 解除 …」这类）不在这张单子上，因为它们根本走
// 不到模型：handleOwnerCommand 在生成之前就把它们处理掉并直接回话了。
func modelSilenceRefusedReason(ctx context.Context, pluginResponses []PluginResponse, images *imageAnnouncementSink) string {
	if hasExternalSideEffect(ctx) {
		return "本轮已经产生了不可撤销的外部副作用"
	}
	if len(pluginResponses) > 0 {
		return "本轮带着必须交代的插件结果"
	}
	if images.hasPendingWork() {
		return "本轮已经受理了图片任务"
	}
	return ""
}
