// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// 改图的原图从哪来。
//
// 以前是一条固定的优先级：当前消息或引用消息带图就用它，其次才轮到模型点名的头像。
// 2026-09-26 群 1081572710：irony 先甩了一张表情，14 秒后说「能不能把 winter 姐姐
// 头像变成机器人风格」。相邻媒体合并把表情并进了这句话，模型正确地点名了
// member_avatar:<Winter>，可运行时看到「当前消息带图」就直接返回，头像从没用上；
// 工具结果也不说用的是哪张，模型照样告诉大家「用了 Winter 的头像」。
//
// 现在分两层：
//   - 显式：模型给了 identity_sources 或 source_message_ids，就只用它们。它看得到
//     上下文里每张图、每个人，比运行时按位置猜准；解析不出来就报错让它改，不悄悄
//     换成别的图。
//   - 隐式：模型什么都没点名时才按老顺序找——当前消息、引用（含引用链）、指代解析
//     选中的来源、被 @ 的成员头像，最后是同一个人刚发的那批图（只在意图路由那条
//     没有模型点名能力的路径上用，见 allowRecentSenderImages）。
//
// 不管哪一层，最后实际用了哪几张都写进工具结果的 sources_used。

// imageEditSourceUsed 是一张（或一组）原图的来历，原样进工具结果。
// user_id / message_id 用真实值，发给模型前由会话标识隐私代理换成别名。
type imageEditSourceUsed struct {
	Kind      string `json:"kind"`
	MessageID string `json:"message_id,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	User      string `json:"user,omitempty"`
	GroupID   string `json:"group_id,omitempty"`
	Images    int    `json:"images,omitempty"`
}

const (
	imageSourceKindCurrentMessage   = "current_message_image"
	imageSourceKindQuoted           = "quoted_image"
	imageSourceKindQuotedChain      = "quoted_chain_image"
	imageSourceKindSemantic         = "semantic_reference_image"
	imageSourceKindMessage          = "message_image"
	imageSourceKindRecentSender     = "recent_sender_image"
	imageSourceKindPreviousEditTask = "previous_edit_source"
)

type imageEditSourcePlan struct {
	// IdentitySources / SourceMessageIDs 是模型显式点名的来源。
	IdentitySources  []string
	SourceMessageIDs []string
	// DefaultIdentitySources 是模型没点名时的兜底头像（被 @ 的成员），属于隐式来源。
	DefaultIdentitySources []string
	// AllowRecentSenderImages 允许在隐式来源都落空时，拿同一个人刚发的那批图兜底。
	//
	// 只给意图路由那条路（enqueueImageReplyTask）：那里没有模型能填 source_message_ids，
	// 「先发图、隔几秒说改成黑白」全靠它。Agent 调工具时不开：同一个人刚发的图会以
	// 「候选依赖图」带着 message_id 附在这一轮里，模型要用就显式点名；运行时替它挑，
	// 挑中的往往是一张表情（上面那起事故）。
	AllowRecentSenderImages bool
}

func (p imageEditSourcePlan) explicit() bool {
	return len(p.IdentitySources) > 0 || len(p.SourceMessageIDs) > 0
}

// resolveImageEditSources 按 plan 解析原图，同时交出每张图的来历。
func (r *Runtime) resolveImageEditSources(ctx context.Context, event MessageEvent, plan imageEditSourcePlan) ([]string, []imageEditSourceUsed, error) {
	if plan.explicit() {
		return r.resolveExplicitImageEditSources(ctx, event, plan)
	}
	urls, used := r.resolveImplicitImageEditSources(ctx, event, plan)
	return urls, used, nil
}

func (r *Runtime) resolveExplicitImageEditSources(ctx context.Context, event MessageEvent, plan imageEditSourcePlan) ([]string, []imageEditSourceUsed, error) {
	var urls []string
	var used []imageEditSourceUsed
	if len(plan.SourceMessageIDs) > 0 {
		sources, sourceUsed, err := r.imageEditSourcesFromMessagesDetailed(ctx, event, plan.SourceMessageIDs)
		if err != nil {
			return nil, nil, err
		}
		urls = appendImageEditSourceImages(urls, sources...)
		used = append(used, sourceUsed...)
	}
	if len(plan.IdentitySources) > 0 {
		avatars, failed := r.avatarIdentitySources(ctx, event, plan.IdentitySources)
		if len(failed) > 0 {
			// 点名的头像取不到就当场说，不退回当前消息里的图：那正是事故里的错法。
			return nil, nil, fmt.Errorf("identity_sources 里 %s 取不到头像（成员不在当前会话、群号不对，或平台没有头像）。这次没有开始画；请核对 user_id 后重新调用，或改用 source_message_ids 指认图片", strings.Join(failed, "、"))
		}
		for _, avatar := range avatars {
			urls = appendImageEditSourceImages(urls, avatar.URL)
			used = append(used, avatar.Used)
		}
	}
	if len(urls) == 0 {
		return nil, nil, errImageEditSourceNotFound
	}
	return urls, used, nil
}

func (r *Runtime) resolveImplicitImageEditSources(ctx context.Context, event MessageEvent, plan imageEditSourcePlan) ([]string, []imageEditSourceUsed) {
	var out []string
	var used []imageEditSourceUsed
	take := func(kind string, messageID, userID, user string, images []string) {
		before := len(out)
		out = appendImageEditSourceImages(out, images...)
		if added := len(out) - before; added > 0 {
			used = append(used, imageEditSourceUsed{Kind: kind, MessageID: strings.TrimSpace(messageID), UserID: strings.TrimSpace(userID), User: strings.TrimSpace(user), Images: added})
		}
	}
	take(imageSourceKindCurrentMessage, event.MessageID, event.UserID, event.SenderName, availableImageURLs(event.Segments))
	if event.Quoted != nil {
		take(imageSourceKindQuoted, event.Quoted.MessageID, event.Quoted.UserID, event.Quoted.SenderName, availableImageURLs(event.Quoted.Segments))
		if len(out) == 0 {
			take(imageSourceKindQuotedChain, "", "", "", r.quotedChainImageURLs(ctx, event))
		}
	}
	for _, sourceID := range eventSemanticSourceMessageIDs(event) {
		single := event
		setEventSemanticSourceMessageIDs(&single, []string{sourceID})
		take(imageSourceKindSemantic, sourceID, "", "", r.semanticReferenceImageURLs(ctx, single))
	}
	if len(out) > 0 {
		return out, used
	}
	// 引用的是机器人自己的失败通知或「在画了」：用户是在接着上一次改图说话，
	// 上一次的原图比头像和最近聊天记录都更贴近他指的那张。
	if r.quotedBotTextWithoutImage(event) {
		if remembered := r.imageEditSources.recall(sessionKey(event), time.Now()); len(remembered) > 0 {
			return remembered, []imageEditSourceUsed{{Kind: imageSourceKindPreviousEditTask, Images: len(remembered)}}
		}
	}
	avatars, _ := r.avatarIdentitySources(ctx, event, plan.DefaultIdentitySources)
	for _, avatar := range avatars {
		out = appendImageEditSourceImages(out, avatar.URL)
		used = append(used, avatar.Used)
	}
	if len(out) > 0 {
		return out, used
	}
	if plan.AllowRecentSenderImages {
		for _, batch := range r.preparedRecentSenderImageBatch(ctx, r.contextHistory(event), event) {
			take(imageSourceKindRecentSender, batch.MessageID, batch.UserID, batch.SenderName, batch.images)
		}
		if len(out) > 0 {
			return out, used
		}
	}
	if remembered := r.imageEditSources.recall(sessionKey(event), time.Now()); len(remembered) > 0 {
		return remembered, []imageEditSourceUsed{{Kind: imageSourceKindPreviousEditTask, Images: len(remembered)}}
	}
	return nil, nil
}
