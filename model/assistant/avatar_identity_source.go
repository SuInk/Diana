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
// 现在由模型在调用 image 时点名要哪几个来源：本群、机器人、发送者，或某个
// 具体成员。名字到 user_id 的对应关系模型本来就能从上下文和群成员工具里拿到，运行时
// 只负责把 id 换成头像地址，并核对这个人在当前会话里确实存在。
const (
	avatarSourceGroup        = "group_avatar"
	avatarSourceGroupPrefix  = "group_avatar:"
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
	resolved, _ := r.avatarIdentitySources(ctx, event, selected)
	var out []string
	for _, source := range resolved {
		out = appendImageEditSourceImages(out, source.URL)
	}
	return out
}

// resolvedImageEditSource 是一张解析出来的原图和它的来历。来历要一路带到工具结果
// 里：模型只有看到「这次真正用的是谁的头像、哪条消息的图」，才不会嘴上说用了 A、
// 实际交给图片模型的是 B。
type resolvedImageEditSource struct {
	URL  string
	Used imageEditSourceUsed
}

// avatarIdentitySources 和 avatarIdentityImageURLs 同一套解析规则，另外交出每张头像
// 的来历，以及没能解析的来源（成员核对不过、群号不在当前会话里）。
func (r *Runtime) avatarIdentitySources(ctx context.Context, event MessageEvent, selected []string) ([]resolvedImageEditSource, []string) {
	if len(selected) == 0 || (event.Kind != EventKindGroup && event.Kind != EventKindPrivate) {
		return nil, selected
	}
	botID := r.avatarBotID(event)
	var (
		out         []resolvedImageEditSource
		failed      []string
		seen        = map[string]bool{}
		memberCheck func(string) bool
	)
	add := func(url string, used imageEditSourceUsed) bool {
		url = strings.TrimSpace(url)
		if url == "" {
			return false
		}
		if !seen[url] {
			seen[url] = true
			out = append(out, resolvedImageEditSource{URL: url, Used: used})
		}
		return true
	}
	for _, raw := range selected {
		if len(out) >= maxAvatarImageSources {
			break
		}
		id := strings.TrimSpace(raw)
		ok := false
		switch {
		case id == avatarSourceGroup:
			if event.Kind == EventKindGroup && strings.TrimSpace(event.GroupID) != "" {
				ok = add(r.avatarSourceURL(ctx, event, event.GroupID, true), imageEditSourceUsed{Kind: avatarSourceGroup, GroupID: strings.TrimSpace(event.GroupID)})
			}
		case strings.HasPrefix(id, avatarSourceGroupPrefix):
			groupID := strings.TrimSpace(strings.TrimPrefix(id, avatarSourceGroupPrefix))
			if groupID == "" {
				break
			}
			if (event.Kind == EventKindGroup && groupID == strings.TrimSpace(event.GroupID)) ||
				(event.Kind == EventKindPrivate && r.privateGroupAvatarAllowed(ctx, event, groupID)) {
				ok = add(r.avatarSourceURL(ctx, event, groupID, true), imageEditSourceUsed{Kind: avatarSourceGroup, GroupID: groupID})
			}
		case id == avatarSourceBot:
			if botID != "" {
				ok = add(r.avatarSourceURL(ctx, event, botID, false), imageEditSourceUsed{Kind: avatarSourceBot, UserID: botID})
			}
		case id == avatarSourceSender:
			if userID := strings.TrimSpace(event.UserID); userID != "" {
				ok = add(r.avatarSourceURL(ctx, event, userID, false), imageEditSourceUsed{Kind: avatarSourceSender, UserID: userID, User: strings.TrimSpace(event.SenderName)})
			}
		case strings.HasPrefix(id, avatarSourceMemberPrefix):
			userID := strings.TrimSpace(strings.TrimPrefix(id, avatarSourceMemberPrefix))
			if userID == "" {
				break
			}
			if memberCheck == nil {
				memberCheck = r.reachableAvatarUserIDs(ctx, event)
			}
			if !memberCheck(userID) {
				break
			}
			ok = add(r.avatarSourceURL(ctx, event, userID, false), imageEditSourceUsed{Kind: "member_avatar", UserID: userID, User: r.knownDisplayName(event, userID)})
		}
		if !ok && id != "" {
			failed = append(failed, id)
		}
	}
	return out, failed
}

// knownDisplayName 从当前会话的历史里找这个人的昵称，找不到就留空，不去打接口。
func (r *Runtime) knownDisplayName(event MessageEvent, userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ""
	}
	if userID == strings.TrimSpace(event.UserID) && strings.TrimSpace(event.SenderName) != "" {
		return strings.TrimSpace(event.SenderName)
	}
	history := r.contextHistory(event)
	for index := len(history) - 1; index >= 0; index-- {
		item := history[index]
		if strings.TrimSpace(item.UserID) == userID && strings.TrimSpace(item.SenderName) != "" {
			return strings.TrimSpace(item.SenderName)
		}
		if item.Quoted != nil && strings.TrimSpace(item.Quoted.UserID) == userID && strings.TrimSpace(item.Quoted.SenderName) != "" {
			return strings.TrimSpace(item.Quoted.SenderName)
		}
	}
	return ""
}

// avatarBotID 是 bot_avatar 指向的账号：配置里写了机器人账号就用它，否则用事件上报的自身 ID。
func (r *Runtime) avatarBotID(event MessageEvent) string {
	if botID := strings.TrimSpace(r.effectiveConfigForEvent(event).BotAccount); botID != "" {
		return botID
	}
	return strings.TrimSpace(event.SelfID)
}

func (r *Runtime) privateGroupAvatarAllowed(ctx context.Context, event MessageEvent, groupID string) bool {
	if IsOneBotPlatform(r.currentPlatform(event)) {
		return explicitAccountIDs(event.Segments)[groupID]
	}
	if groupID == "" {
		return false
	}
	// A private Telegram group ID is negative. Knowing its ID alone does not
	// authorize an outsider to see its avatar through the bot.
	pattern := regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])` + regexp.QuoteMeta(groupID) + `(?:[^A-Za-z0-9_-]|$)`)
	if !pattern.MatchString(PlainText(event.Segments)) {
		return false
	}
	_, err := r.getGroupMemberInfoForEvent(ctx, event, groupID, event.UserID)
	return err == nil
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
		for userID := range explicitAccountIDs(event.Segments) {
			reachable[userID] = true
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
	if event.Kind == EventKindGroup && strings.TrimSpace(event.GroupID) != "" && IsOneBotPlatform(r.currentPlatform(event)) {
		if members, err := r.getGroupMemberListForEvent(ctx, event, event.GroupID); err == nil {
			for _, member := range members {
				if userID := strings.TrimSpace(member.UserID); userID != "" {
					reachable[userID] = true
				}
			}
		}
	}
	return func(userID string) bool {
		userID = strings.TrimSpace(userID)
		if reachable[userID] {
			return true
		}
		if !IsOneBotPlatform(r.currentPlatform(event)) && event.Kind == EventKindGroup {
			_, err := r.getGroupMemberInfoForEvent(ctx, event, event.GroupID, userID)
			return err == nil
		}
		return false
	}
}

func explicitAccountIDs(segments []MessageSegment) map[string]bool {
	ids := map[string]bool{}
	for _, segment := range segments {
		if segment.Type != "text" {
			continue
		}
		for _, id := range explicitQQAccountPattern.FindAllString(segment.Data["text"], -1) {
			ids[id] = true
		}
	}
	return ids
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
