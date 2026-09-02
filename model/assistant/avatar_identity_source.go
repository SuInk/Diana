// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"regexp"
	"strings"
)

var explicitQQAccountPattern = regexp.MustCompile(`[1-9][0-9]{4,13}`)

// 图片编辑要用到的头像来源。
//
// 早期实现是拿四张词表去猜用户想编辑谁的头像：正文里出现「头像」就把被引用者拉
// 进来，出现「群头像」取本群，出现「我的头像」取发送者，出现「你的头像」取机器人；
// 取不到人时还会拿群成员的名片和昵称去正文里做无边界子串匹配，靠命中判断「用户
// 说的是这个人」。这是用关键词判断语义意图——同义说法一变就选错人，选错的代价是
// 把不相干的人的头像喂进图生图管线。
//
// 现在由模型在调用 diana.image 时点名要哪几个来源：本群、机器人、发送者，或某个
// 具体成员。名字到 user_id 的对应关系模型本来就能从上下文和群成员工具里拿到，运行时
// 只负责把 id 换成头像地址，并核对这个人在当前会话里确实存在。
const (
	avatarSourceGroup        = "group_avatar"
	avatarSourceBot          = "bot_avatar"
	avatarSourceSender       = "sender_avatar"
	avatarSourceMemberPrefix = "member_avatar:"
)

// avatarIdentityImageURLs 把模型点名的来源换成图片地址。
//
// 校验放在真正用到的时候，而不是渲染工具参数的时候：列群成员要打一次 OneBot 接口，
// 而工具参数每轮都要渲染，放在那里等于每轮都拉一次名单。member_avatar 只在模型确实
// 选了成员头像时才去核对，绝大多数轮次一次接口都不会打。
func (r *Runtime) avatarIdentityImageURLs(ctx context.Context, event MessageEvent, selected []string) []string {
	if len(selected) == 0 || (event.Kind != EventKindGroup && event.Kind != EventKindPrivate) {
		return nil
	}
	cfg := r.effectiveConfigForEvent(event)
	botID := strings.TrimSpace(cfg.BotAccount)
	if botID == "" {
		botID = strings.TrimSpace(event.SelfID)
	}
	var (
		out         []string
		memberCheck func(string) bool
	)
	for _, raw := range selected {
		id := strings.TrimSpace(raw)
		switch {
		case id == avatarSourceGroup:
			if event.Kind == EventKindGroup && strings.TrimSpace(event.GroupID) != "" {
				out = appendImageEditSourceImages(out, OneBotGroupAvatarURL(event.GroupID))
			}
		case id == avatarSourceBot:
			if botID != "" {
				out = appendImageEditSourceImages(out, OneBotMemberAvatarURL(botID))
			}
		case id == avatarSourceSender:
			if userID := strings.TrimSpace(event.UserID); userID != "" {
				out = appendImageEditSourceImages(out, OneBotMemberAvatarURL(userID))
			}
		case strings.HasPrefix(id, avatarSourceMemberPrefix):
			userID := strings.TrimSpace(strings.TrimPrefix(id, avatarSourceMemberPrefix))
			if userID == "" {
				continue
			}
			if memberCheck == nil {
				memberCheck = r.reachableAvatarUserIDs(ctx, event)
			}
			if !memberCheck(userID) {
				continue
			}
			out = appendImageEditSourceImages(out, OneBotMemberAvatarURL(userID))
		}
		if len(out) >= maxAvatarImageSources {
			break
		}
	}
	return out
}

// reachableAvatarUserIDs 返回一个判定函数：这个 user_id 是不是当前会话里真实可见
// 的人。防的是模型凭空编一个 QQ 号，把陌生人的头像拉进图生图管线。
func (r *Runtime) reachableAvatarUserIDs(ctx context.Context, event MessageEvent) func(string) bool {
	reachable := map[string]bool{}
	for _, userID := range mentionedUserIDs(event.Segments) {
		reachable[strings.TrimSpace(userID)] = true
	}
	// 私聊没有群成员名单可供核验，但用户在当前消息里明确写出的 QQ 号同样是
	// 结构化、可审计的身份指向。只放行消息中逐字出现的号码，避免模型凭空选择
	// 一个不相关账号；群聊仍以真实成员名单为准。
	if event.Kind == EventKindPrivate {
		for _, segment := range event.Segments {
			if segment.Type != "text" {
				continue
			}
			for _, userID := range explicitQQAccountPattern.FindAllString(segment.Data["text"], -1) {
				reachable[userID] = true
			}
		}
	}
	if event.Quoted != nil {
		if userID := strings.TrimSpace(event.Quoted.UserID); userID != "" {
			reachable[userID] = true
		}
	}
	if userID := strings.TrimSpace(event.UserID); userID != "" {
		reachable[userID] = true
	}
	if event.Kind == EventKindGroup && strings.TrimSpace(event.GroupID) != "" {
		if members, err := r.getGroupMemberListForEvent(ctx, event, event.GroupID); err == nil {
			for _, member := range members {
				if userID := strings.TrimSpace(member.UserID); userID != "" {
					reachable[userID] = true
				}
			}
		}
	}
	return func(userID string) bool { return reachable[strings.TrimSpace(userID)] }
}

// defaultAvatarIdentitySources 是模型没有点名时的兜底：只取被 @ 的成员。@ 是用户
// 亲手打出的结构化指向，不需要再去读措辞；其余来源必须由模型显式选择。
func defaultAvatarIdentitySources(event MessageEvent, botID string) []string {
	var ids []string
	for _, userID := range mentionedUserIDs(event.Segments) {
		if strings.TrimSpace(userID) == strings.TrimSpace(botID) {
			continue
		}
		ids = append(ids, avatarSourceMemberPrefix+userID)
	}
	return ids
}
