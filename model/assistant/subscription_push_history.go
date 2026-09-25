// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
)

// 订阅推送进历史要带标记。仓库动态、RSS 这类事实卡片以前和机器人的发言一样只记
// outbound:true，给模型看时就是一条 assistant 发言。模型于是把「GitHub 动态：…」
// 当成自己的说话格式学去：刚建完 Issue，就在回复里自己拼一张卡片，二十几秒后真正的
// 订阅推送再来一张，群里同一张卡片出现两次。
//
// 标记落在事件本身（PushKind），随 payload 一起持久化；给模型看时由
// subscriptionPushHistoryPromptText 渲染成旁白，和戳一戳同一个做法。旧事件没有标记，
// 渲染结果保持原样，已有历史的前缀缓存不受影响。
//
// assistantHistoryEvent 不改：群友消息长度统计、复读自检这些地方仍把推送算作机器人
// 这一侧，免得一张长卡片被当成群友发言拉高长度基准。

const (
	subscriptionPushRepositoryWatch = "repository_watch"
	subscriptionPushRSSWatch        = "rss_watch"
	subscriptionPushExternal        = "external_push"
)

type subscriptionPushContextKey struct{}

// withSubscriptionPush 标记这次投递是订阅推送，出站写历史时会带上 PushKind。
func withSubscriptionPush(ctx context.Context, kind string) context.Context {
	return context.WithValue(ctx, subscriptionPushContextKey{}, kind)
}

func subscriptionPushKindFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	kind, _ := ctx.Value(subscriptionPushContextKey{}).(string)
	return strings.TrimSpace(kind)
}

func isSubscriptionPushEvent(event MessageEvent) bool {
	return strings.TrimSpace(event.PushKind) != ""
}

// subscriptionPushLabel 是给模型看的来源说明。
func subscriptionPushLabel(event MessageEvent) string {
	switch strings.TrimSpace(event.PushKind) {
	case "":
		return ""
	case subscriptionPushRepositoryWatch:
		return "仓库订阅推送"
	case subscriptionPushRSSWatch:
		return "RSS 订阅推送"
	case subscriptionPushExternal:
		return "外部接口推送"
	default:
		return "订阅推送"
	}
}

// subscriptionPushHistoryPromptText 把推送渲染成旁白。只依赖事件自身的时间和正文，
// 同一条推送每一轮渲染出来都一样，不破坏前缀缓存。
func subscriptionPushHistoryPromptText(event MessageEvent) string {
	text := strings.TrimSpace(historyPlainText(event))
	if text == "" {
		return ""
	}
	return historyLinePrefix(event) + "[" + subscriptionPushLabel(event) + "，系统自动发出，不是你说的话，回复里不要模仿这种卡片] " + text
}
