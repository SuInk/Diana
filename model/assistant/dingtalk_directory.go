package assistant

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

func (c *DingTalkChannel) groupBase(ctx context.Context, id string) (map[string]any, error) {
	if err := requireDirectoryID(id); err != nil {
		return nil, err
	}
	return directoryData(c.CallAPI(ctx, "POST /v1.0/im/groups/baseInfos/query", map[string]any{"openConversationId": id}))
}
func (c *DingTalkChannel) GroupInfo(ctx context.Context, id string) (GroupInfo, error) {
	d, err := c.groupBase(ctx, id)
	if err != nil {
		return GroupInfo{}, err
	}
	return GroupInfo{GroupID: id, GroupName: stringFromAny(d["title"]), MemberCount: directoryInt(d["memberCount"]), MemberCountKnown: d["memberCount"] != nil}, nil
}
func (c *DingTalkChannel) GroupMembers(ctx context.Context, id string) (GroupMemberDirectory, error) {
	if directoryEvent(ctx).UserIDType == "dingtalk_sender_id" {
		return GroupMemberDirectory{}, fmt.Errorf("外部联系人 senderId 不能作为企业 userId 查询成员")
	}
	actor := directoryEvent(ctx).UserID
	if actor == "" {
		return GroupMemberDirectory{}, fmt.Errorf("钉钉成员枚举需要当前企业内操作者 userId，不能混用外部联系人 senderId")
	}
	info, err := c.GroupInfo(ctx, id)
	if err != nil {
		return GroupMemberDirectory{}, err
	}
	result := GroupMemberDirectory{Source: "dingtalk_authorized_inner_group", Total: info.MemberCount, TotalKnown: info.MemberCountKnown, Warnings: []string{"名单范围由应用授权与群类型决定；自定义群角色名称不授予管理权限。"}}
	next := 0
	seen := map[int]bool{}
	ids := map[string]bool{}
	for page := 0; page < 20; page++ {
		d, err := directoryData(c.CallAPI(ctx, "POST /v1.0/im/innerGroups/memberLists/query", map[string]any{"openConversationId": id, "userId": actor, "maxResults": 100, "nextToken": next}))
		if err != nil {
			return result, err
		}
		for _, row := range directoryRows(d["list"]) {
			uid := stringFromAny(row["userId"])
			if uid == "" || ids[uid] {
				continue
			}
			ids[uid] = true
			result.Members = append(result.Members, OneBotGroupMemberInfo{GroupID: id, UserID: uid, Nickname: stringFromAny(row["name"]), Card: stringFromAny(row["nickName"]), MembershipVerified: true})
		}
		if more, _ := d["hasMore"].(bool); !more {
			result.Complete = result.TotalKnown && len(result.Members) == result.Total
			return result, nil
		}
		if d["nextToken"] == nil {
			return result, fmt.Errorf("钉钉分页标记缺失")
		}
		next = directoryInt(d["nextToken"])
		if seen[next] {
			return result, fmt.Errorf("钉钉分页标记重复")
		}
		seen[next] = true
	}
	result.Warnings = append(result.Warnings, "达到本次成员读取上限。")
	return result, nil
}
func (c *DingTalkChannel) GroupMember(ctx context.Context, gid, uid string) (OneBotGroupMemberInfo, error) {
	if e := directoryEvent(ctx); e.UserID == uid && e.UserIDType == "dingtalk_sender_id" {
		return OneBotGroupMemberInfo{}, fmt.Errorf("senderId 尚未映射为企业 userId，不能核验")
	}
	if err := requireDirectoryID(gid); err != nil {
		return OneBotGroupMemberInfo{}, err
	}
	if err := requireDirectoryID(uid); err != nil {
		return OneBotGroupMemberInfo{}, err
	}
	d, err := directoryData(c.CallAPI(ctx, "POST /v1.0/im/innerGroups/members/check", map[string]any{"openConversationId": gid, "userId": uid}))
	if err != nil {
		return OneBotGroupMemberInfo{}, err
	}
	if present, _ := d["result"].(bool); !present {
		return OneBotGroupMemberInfo{}, fmt.Errorf("未在该企业内部群内核验到账号")
	}
	member := OneBotGroupMemberInfo{GroupID: gid, UserID: uid, MembershipVerified: true}
	user, e := directoryData(c.CallAPI(ctx, "POST /topapi/v2/user/get", map[string]any{"userid": uid}))
	if e == nil {
		member.Nickname = stringFromAny(user["name"])
	}
	actor := directoryEvent(ctx).UserID
	if actor != "" {
		roles, e := directoryData(c.CallAPI(ctx, "POST /v1.0/im/groupRoles/users/query", map[string]any{"openConversationId": gid, "userId": actor, "viewedUserId": uid}))
		if e == nil {
			var titles []string
			for _, role := range directoryRows(roles["groupRoles"]) {
				titles = append(titles, stringFromAny(role["roleName"]))
			}
			member.Title = strings.Join(titles, "、")
		}
	}
	return member, nil
}
func (c *DingTalkChannel) MemberAvatar(ctx context.Context, uid string) (GroupAvatar, error) {
	if e := directoryEvent(ctx); e.UserID == uid && e.UserIDType == "dingtalk_sender_id" {
		return GroupAvatar{}, fmt.Errorf("senderId 不能直接用于企业用户头像查询")
	}
	if err := requireDirectoryID(uid); err != nil {
		return GroupAvatar{}, err
	}
	d, err := directoryData(c.CallAPI(ctx, "POST /topapi/v2/user/get", map[string]any{"userid": uid}))
	if err != nil {
		return GroupAvatar{}, err
	}
	return platformAvatar(ctx, stringFromAny(d["avatar"]))
}
func (c *DingTalkChannel) GroupAvatar(ctx context.Context, gid string) (GroupAvatar, error) {
	d, err := c.groupBase(ctx, gid)
	if err != nil {
		return GroupAvatar{}, err
	}
	icon := stringFromAny(d["icon"])
	if strings.HasPrefix(icon, "https://") || strings.HasPrefix(icon, "http://") {
		return platformAvatar(ctx, icon)
	}
	if icon == "" {
		return GroupAvatar{}, fmt.Errorf("钉钉未返回群头像")
	}
	token, err := c.tokens.Get(ctx)
	if err != nil {
		return GroupAvatar{}, err
	}
	return platformAvatar(ctx, "https://oapi.dingtalk.com/media/get?access_token="+url.QueryEscape(token)+"&media_id="+url.QueryEscape(icon))
}
