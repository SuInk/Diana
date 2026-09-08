package assistant

import (
	"context"
	"fmt"
)

func (c *WeComChannel) appChat(ctx context.Context, id string) (map[string]any, error) {
	if err := requireDirectoryID(id); err != nil {
		return nil, err
	}
	d, err := directoryData(c.CallAPI(ctx, "GET /cgi-bin/appchat/get", map[string]any{"chatid": id}))
	if err != nil {
		return nil, fmt.Errorf("只能查询本应用获授权的应用群，非任意普通群或客户群: %w", err)
	}
	chat, ok := d["chat_info"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("企微未返回应用群资料")
	}
	return chat, nil
}
func (c *WeComChannel) GroupInfo(ctx context.Context, id string) (GroupInfo, error) {
	d, err := c.appChat(ctx, id)
	if err != nil {
		return GroupInfo{}, err
	}
	return GroupInfo{GroupID: id, GroupName: stringFromAny(d["name"]), MemberCount: len(directoryStrings(d["userlist"])), MemberCountKnown: d["userlist"] != nil}, nil
}
func (c *WeComChannel) GroupMembers(ctx context.Context, id string) (GroupMemberDirectory, error) {
	d, err := c.appChat(ctx, id)
	if err != nil {
		return GroupMemberDirectory{}, err
	}
	ids := directoryStrings(d["userlist"])
	result := GroupMemberDirectory{Source: "wecom_appchat", Total: len(ids), TotalKnown: d["userlist"] != nil, Complete: d["userlist"] != nil}
	for _, uid := range ids {
		result.Members = append(result.Members, OneBotGroupMemberInfo{GroupID: id, UserID: uid, Role: directoryRole(uid, stringFromAny(d["owner"]), nil), MembershipVerified: true})
	}
	return result, nil
}
func (c *WeComChannel) GroupMember(ctx context.Context, gid, uid string) (OneBotGroupMemberInfo, error) {
	d, err := c.GroupMembers(ctx, gid)
	if err != nil {
		return OneBotGroupMemberInfo{}, err
	}
	for _, member := range d.Members {
		if member.UserID == uid {
			user, userErr := directoryData(c.CallAPI(ctx, "GET /cgi-bin/user/get", map[string]any{"userid": uid}))
			if userErr == nil {
				member.Nickname = stringFromAny(user["name"])
			}
			return member, nil
		}
	}
	return OneBotGroupMemberInfo{}, fmt.Errorf("未在该应用群内核验到成员")
}
func (c *WeComChannel) MemberAvatar(ctx context.Context, uid string) (GroupAvatar, error) {
	if err := requireDirectoryID(uid); err != nil {
		return GroupAvatar{}, err
	}
	c.mu.RLock()
	agentID := c.cfg.AgentID
	c.mu.RUnlock()
	if uid == agentID {
		d, err := directoryData(c.CallAPI(ctx, "GET /cgi-bin/agent/get", map[string]any{"agentid": uid}))
		if err != nil {
			return GroupAvatar{}, err
		}
		return platformAvatar(ctx, stringFromAny(d["square_logo_url"]))
	}
	d, err := directoryData(c.CallAPI(ctx, "GET /cgi-bin/user/get", map[string]any{"userid": uid}))
	if err != nil {
		return GroupAvatar{}, err
	}
	return platformAvatar(ctx, firstNonEmpty(stringFromAny(d["avatar"]), stringFromAny(d["thumb_avatar"])))
}
