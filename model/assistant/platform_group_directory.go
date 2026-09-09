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
	ctx = withDirectoryEvent(ctx, event)
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
	return "diana.group"
}

func groupToolPrompt(event MessageEvent) string {
	if IsOneBotPlatform(event.Platform) {
		return promptToolOneBotGroup + "可以读取当前群成员总数。"
	}
	if NormalizePlatformID(event.Platform) != PlatformTelegram {
		return "查询群资料、人数、成员身份和头像来源使用 diana.group，members 的完整性看 member_list_complete、member_source 和 warnings，不得把有限分页当全群。member 按平台原始 user_id 核验当前成员；通讯录资料不等于在群，自定义角色名称不等于管理员权限。头像用 diana.image 的身份来源，由服务端读取，不拼 QQ 地址。飞书成员列表不含机器人；钉钉按企业内部群和应用授权查询，未证实管理角色时不授予配置权限；企微只查询获授权的应用群，群头像接口不可用；QQ 官方频道须有 guild_id，普通 QQ 群不能套用频道成员接口。"
	}
	return "用户查询群资料、群人数、成员身份、用户名或头像来源时调用 diana.group。info 获取实时群资料和人数；member 按 user_id 查询当前成员及角色；members 只返回管理员和已知账号候选，绝不是完整名单，不得据此声称已列出所有人或不在列表的人已退群。确定是否在群、是否管理员必须使用 member 实时校验。头像来源通过 diana.image 的 sender_avatar、bot_avatar、group_avatar、member_avatar:用户ID 指定，由运行时获取；不要编 QQ 头像链接或索取 Bot Token。"
}

func (r *Runtime) currentPlatform(event MessageEvent) string {
	return NormalizePlatformID(firstNonEmpty(strings.TrimSpace(event.Platform), r.effectiveConfigForEvent(event).Platform))
}

func groupMemberToolItem(member OneBotGroupMemberInfo) dianaGroupMemberItem {
	return dianaGroupMemberItem{UserID: member.UserID, DisplayName: member.DisplayName(), Nickname: member.Nickname, Card: member.Card, Role: member.Role, Title: member.Title, AvatarURL: member.AvatarURL, Mention: mentionMarkerFor(member.UserID), Username: member.Username, IsBot: member.IsBot, MembershipVerified: member.MembershipVerified, AvatarSource: avatarSourceMemberPrefix + member.UserID}
}
