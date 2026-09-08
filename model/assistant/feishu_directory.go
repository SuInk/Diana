package assistant

import (
	"context"
	"fmt"
	"net/url"
)

func (c *FeishuChannel) chatDetails(ctx context.Context, id string) (map[string]any, error) {
	if err := requireDirectoryID(id); err != nil {
		return nil, err
	}
	return directoryData(c.CallAPI(ctx, "GET /open-apis/im/v1/chats/"+url.PathEscape(id), map[string]any{"user_id_type": "open_id"}))
}
func (c *FeishuChannel) GroupInfo(ctx context.Context, id string) (GroupInfo, error) {
	d, err := c.chatDetails(ctx, id)
	if err != nil {
		return GroupInfo{}, err
	}
	_, users := d["user_count"]
	_, bots := d["bot_count"]
	return GroupInfo{GroupID: id, GroupName: stringFromAny(d["name"]), MemberCount: directoryInt(d["user_count"]) + directoryInt(d["bot_count"]), MemberCountKnown: users && bots}, nil
}
func (c *FeishuChannel) GroupMembers(ctx context.Context, id string) (GroupMemberDirectory, error) {
	if err := requireDirectoryID(id); err != nil {
		return GroupMemberDirectory{}, err
	}
	result := GroupMemberDirectory{Source: "feishu_human_members", Warnings: []string{"飞书成员列表不含机器人成员，不能视为全群完整名单。"}}
	details, _ := c.chatDetails(ctx, id)
	_, users := details["user_count"]
	_, bots := details["bot_count"]
	result.TotalKnown = users && bots
	result.Total = directoryInt(details["user_count"]) + directoryInt(details["bot_count"])
	owner := stringFromAny(details["owner_id"])
	admins := directoryStrings(details["user_manager_id_list"])
	token := ""
	seen := map[string]bool{}
	members := map[string]bool{}
	for page := 0; page < 20; page++ {
		d, err := directoryData(c.CallAPI(ctx, "GET /open-apis/im/v1/chats/"+url.PathEscape(id)+"/members", map[string]any{"member_id_type": "open_id", "page_size": 100, "page_token": token}))
		if err != nil {
			return result, err
		}
		for _, row := range directoryRows(d["items"]) {
			uid := stringFromAny(row["member_id"])
			if uid == "" || members[uid] {
				continue
			}
			members[uid] = true
			result.Members = append(result.Members, OneBotGroupMemberInfo{GroupID: id, UserID: uid, Nickname: stringFromAny(row["name"]), Role: directoryRole(uid, owner, admins), MembershipVerified: true})
		}
		if more, _ := d["has_more"].(bool); !more {
			return result, nil
		}
		next := stringFromAny(d["page_token"])
		if next == "" || seen[next] {
			return result, fmt.Errorf("飞书分页标记缺失或重复，名单未读取完整")
		}
		seen[next] = true
		token = next
	}
	result.Warnings = append(result.Warnings, "达到本次分页上限，仍有未返回的人类成员。")
	return result, nil
}
func (c *FeishuChannel) GroupMember(ctx context.Context, gid, uid string) (OneBotGroupMemberInfo, error) {
	if e := directoryEvent(ctx); e.UserID == uid && e.UserIDType != "" && e.UserIDType != "open_id" {
		return OneBotGroupMemberInfo{}, fmt.Errorf("当前身份不是 open_id，需先转换标识后核验成员")
	}
	if err := requireDirectoryID(uid); err != nil {
		return OneBotGroupMemberInfo{}, err
	}
	c.mu.RLock()
	appID := c.cfg.AppID
	c.mu.RUnlock()
	if uid == appID {
		d, err := directoryData(c.CallAPI(ctx, "GET /open-apis/im/v1/chats/"+url.PathEscape(gid)+"/members/is_in_chat", nil))
		if err != nil {
			return OneBotGroupMemberInfo{}, err
		}
		if in, _ := d["is_in_chat"].(bool); !in {
			return OneBotGroupMemberInfo{}, fmt.Errorf("机器人不在该飞书群内")
		}
		return OneBotGroupMemberInfo{GroupID: gid, UserID: uid, IsBot: true, Role: "member", MembershipVerified: true}, nil
	}
	d, err := c.GroupMembers(ctx, gid)
	if err != nil {
		return OneBotGroupMemberInfo{}, err
	}
	for _, member := range d.Members {
		if member.UserID == uid {
			return member, nil
		}
	}
	return OneBotGroupMemberInfo{}, fmt.Errorf("未在可读取的人类成员中核验该账号；不可据此认定机器人账号已离群")
}
func (c *FeishuChannel) GroupAvatar(ctx context.Context, gid string) (GroupAvatar, error) {
	d, err := c.chatDetails(ctx, gid)
	if err != nil {
		return GroupAvatar{}, err
	}
	return platformAvatar(ctx, stringFromAny(d["avatar"]))
}
func (c *FeishuChannel) MemberAvatar(ctx context.Context, uid string) (GroupAvatar, error) {
	if e := directoryEvent(ctx); e.UserID == uid && e.UserIDType != "" && e.UserIDType != "open_id" {
		return GroupAvatar{}, fmt.Errorf("当前身份不是 open_id，不能混用头像查询标识")
	}
	if err := requireDirectoryID(uid); err != nil {
		return GroupAvatar{}, err
	}
	c.mu.RLock()
	appID := c.cfg.AppID
	c.mu.RUnlock()
	if uid == appID {
		d, err := directoryData(c.CallAPI(ctx, "GET /open-apis/bot/v3/info", nil))
		if err != nil {
			return GroupAvatar{}, err
		}
		bot, _ := d["bot"].(map[string]any)
		return platformAvatar(ctx, stringFromAny(bot["avatar_url"]))
	}
	d, err := directoryData(c.CallAPI(ctx, "GET /open-apis/contact/v3/users/"+url.PathEscape(uid), map[string]any{"user_id_type": "open_id"}))
	if err != nil {
		return GroupAvatar{}, err
	}
	user, _ := d["user"].(map[string]any)
	avatar, _ := user["avatar"].(map[string]any)
	return platformAvatar(ctx, firstNonEmpty(stringFromAny(avatar["avatar_origin"]), stringFromAny(avatar["avatar_640"]), stringFromAny(avatar["avatar_240"])))
}
