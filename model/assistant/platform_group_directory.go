package assistant

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func eventChannelFor[T any](r *Runtime, event MessageEvent) (T, bool) {
	var zero T
	if r == nil {
		return zero, false
	}
	r.mu.RLock()
	channel := r.channel
	r.mu.RUnlock()
	if multi, ok := channel.(*MultiChannel); ok {
		binding, err := multi.bindingFor(event.ProfileID, event.Platform)
		if err != nil {
			return zero, false
		}
		channel = binding.Channel
	}
	provider, ok := channel.(T)
	return provider, ok
}

func (r *Runtime) groupDirectoryForEvent(ctx context.Context, event MessageEvent, groupID string) (GroupMemberDirectory, error) {
	if provider, ok := eventChannelFor[GroupMemberChannel](r, event); ok {
		if telegram, ok := provider.(*TelegramChannel); ok {
			// Seed candidates from already available local history after a restart;
			// these names never establish current membership or administrator rights.
			history, _ := r.sessionContextHistory(event)
			for _, item := range append(append([]MessageEvent(nil), history...), event) {
				if item.GroupID != groupID || item.UserID == "" || (item.Platform != "" && NormalizePlatformID(item.Platform) != PlatformTelegram) {
					continue
				}
				if id, err := strconv.ParseInt(item.UserID, 10, 64); err == nil && id > 0 {
					telegram.rememberMember(groupID, telegramUser{ID: id, FirstName: item.SenderName}, true)
				}
			}
		}
		ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
		defer cancel()
		return provider.GroupMembers(ctx, groupID)
	}
	if !IsOneBotPlatform(firstNonEmpty(event.Platform, r.effectiveConfigForEvent(event).Platform)) {
		return GroupMemberDirectory{}, fmt.Errorf("当前平台未提供群成员查询能力")
	}
	members, err := r.getGroupMemberList(ctx, groupID, func(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
		return r.callOneBotAPIForEvent(ctx, event, action, params)
	})
	return GroupMemberDirectory{Members: members, Complete: err == nil, Total: len(members), TotalKnown: err == nil, Source: "platform_member_list"}, err
}

func groupToolName(event MessageEvent) string {
	if NormalizePlatformID(event.Platform) == PlatformTelegram {
		return "diana.group"
	}
	return "diana.onebot_group"
}

func groupToolPrompt(event MessageEvent) string {
	if NormalizePlatformID(event.Platform) != PlatformTelegram {
		return promptToolOneBotGroup + "可以读取当前群成员总数。"
	}
	return "用户查询群资料、群人数、成员身份、用户名或头像来源时调用 diana.group。info 获取实时群资料和人数；member 按 user_id 查询当前成员及角色；members 只返回管理员和已知账号候选，绝不是完整名单，不得据此声称已列出所有人或不在列表的人已退群。确定是否在群、是否管理员必须使用 member 实时校验。头像来源通过 diana.image 的 sender_avatar、bot_avatar、group_avatar、member_avatar:用户ID 指定，由运行时获取；不要编 QQ 头像链接或索取 Bot Token。"
}

func (r *Runtime) currentPlatform(event MessageEvent) string {
	return NormalizePlatformID(firstNonEmpty(strings.TrimSpace(event.Platform), r.effectiveConfigForEvent(event).Platform))
}

func groupMemberToolItem(member OneBotGroupMemberInfo) dianaOneBotGroupMemberItem {
	return dianaOneBotGroupMemberItem{UserID: member.UserID, DisplayName: member.DisplayName(), Nickname: member.Nickname, Card: member.Card, Role: member.Role, Title: member.Title, AvatarURL: member.AvatarURL, Mention: mentionMarkerFor(member.UserID), Username: member.Username, IsBot: member.IsBot, MembershipVerified: member.MembershipVerified, AvatarSource: avatarSourceMemberPrefix + member.UserID}
}
