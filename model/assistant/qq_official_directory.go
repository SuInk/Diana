package assistant

import (
	"context"
	"fmt"
	"net/url"
)

func (c *QQOfficialChannel) guildForDirectory(ctx context.Context, channelID string) (string, error) {
	e := directoryEvent(ctx)
	if e.PlatformScope == "qq_group" {
		return "", fmt.Errorf("QQ 官方普通群不支持套用频道成员接口，仅可使用已收到的账号信息")
	}
	if e.GroupID == channelID && e.PlatformScope == "qq_guild" && e.GuildID != "" {
		return e.GuildID, nil
	}
	c.mu.RLock()
	guild := c.guildChannels[channelID]
	c.mu.RUnlock()
	if guild == "" {
		return "", fmt.Errorf("缺少频道 guild_id；不能把 group_openid 或 channel_id 当作 guild_id")
	}
	return guild, nil
}
func (c *QQOfficialChannel) guildDetails(ctx context.Context, channelID string) (map[string]any, error) {
	guild, err := c.guildForDirectory(ctx, channelID)
	if err != nil {
		return nil, err
	}
	return directoryData(c.CallAPI(ctx, "GET /guilds/"+url.PathEscape(guild), nil))
}
func (c *QQOfficialChannel) GroupInfo(ctx context.Context, id string) (GroupInfo, error) {
	d, err := c.guildDetails(ctx, id)
	if err != nil {
		return GroupInfo{}, err
	}
	return GroupInfo{GroupID: id, GroupName: stringFromAny(d["name"]), MemberCount: directoryInt(d["member_count"]), MemberCountKnown: d["member_count"] != nil}, nil
}
func (c *QQOfficialChannel) publicGuild(ctx context.Context, id string) (string, error) {
	guild, err := c.guildForDirectory(ctx, id)
	if err != nil {
		return "", err
	}
	d, err := directoryData(c.CallAPI(ctx, "GET /channels/"+url.PathEscape(id), nil))
	if err != nil {
		return "", err
	}
	if d["private_type"] == nil || directoryInt(d["private_type"]) != 0 {
		return "", fmt.Errorf("私密子频道不能用父频道名单证明可见性，暂不支持该成员核验")
	}
	if stringFromAny(d["guild_id"]) != guild {
		return "", fmt.Errorf("子频道所属 guild_id 不匹配")
	}
	return guild, nil
}
func (c *QQOfficialChannel) guildMemberInfo(gid string, row map[string]any) OneBotGroupMemberInfo {
	u, _ := row["user"].(map[string]any)
	uid := stringFromAny(u["id"])
	role := "member"
	for _, id := range directoryStrings(row["roles"]) {
		if id == "4" {
			role = "owner"
			break
		}
		if id == "2" {
			role = "admin"
		}
	}
	if uid != "" && stringFromAny(u["avatar"]) != "" {
		c.mu.Lock()
		if c.avatarURLs == nil {
			c.avatarURLs = map[string]string{}
		}
		c.avatarURLs[gid+"\x00"+uid] = stringFromAny(u["avatar"])
		c.mu.Unlock()
	}
	bot, _ := u["bot"].(bool)
	return OneBotGroupMemberInfo{GroupID: gid, UserID: uid, Nickname: stringFromAny(u["username"]), Card: stringFromAny(row["nick"]), Role: role, IsBot: bot, MembershipVerified: uid != ""}
}
func (c *QQOfficialChannel) GroupMember(ctx context.Context, gid, uid string) (OneBotGroupMemberInfo, error) {
	if err := requireDirectoryID(uid); err != nil {
		return OneBotGroupMemberInfo{}, err
	}
	guild, err := c.publicGuild(ctx, gid)
	if err != nil {
		return OneBotGroupMemberInfo{}, err
	}
	d, err := directoryData(c.CallAPI(ctx, "GET /guilds/"+url.PathEscape(guild)+"/members/"+url.PathEscape(uid), nil))
	if err != nil {
		return OneBotGroupMemberInfo{}, err
	}
	member := c.guildMemberInfo(gid, d)
	if member.UserID != uid {
		return OneBotGroupMemberInfo{}, fmt.Errorf("频道成员响应账号不匹配")
	}
	return member, nil
}
func (c *QQOfficialChannel) GroupMembers(ctx context.Context, gid string) (GroupMemberDirectory, error) {
	guild, err := c.publicGuild(ctx, gid)
	if err != nil {
		return GroupMemberDirectory{}, err
	}
	info, err := c.GroupInfo(ctx, gid)
	if err != nil {
		return GroupMemberDirectory{}, err
	}
	result := GroupMemberDirectory{Source: "qq_official_public_channel_guild_members", Total: info.MemberCount, TotalKnown: info.MemberCountKnown}
	after := ""
	seen := map[string]bool{}
	for page := 0; page < 20; page++ {
		d, err := directoryData(c.CallAPI(ctx, "GET /guilds/"+url.PathEscape(guild)+"/members", map[string]any{"limit": 100, "after": after}))
		if err != nil {
			return result, err
		}
		rows := directoryRows(d["result"])
		for _, row := range rows {
			member := c.guildMemberInfo(gid, row)
			if member.UserID == "" || seen[member.UserID] {
				continue
			}
			seen[member.UserID] = true
			result.Members = append(result.Members, member)
		}
		if len(rows) < 100 {
			result.Complete = result.TotalKnown && len(result.Members) == result.Total
			return result, nil
		}
		last := c.guildMemberInfo(gid, rows[len(rows)-1]).UserID
		if last == "" || last == after {
			return result, fmt.Errorf("频道分页无进展")
		}
		after = last
	}
	result.Warnings = []string{"达到本次频道成员分页上限"}
	return result, nil
}
func (c *QQOfficialChannel) GroupAvatar(ctx context.Context, gid string) (GroupAvatar, error) {
	d, err := c.guildDetails(ctx, gid)
	if err != nil {
		return GroupAvatar{}, err
	}
	return platformAvatar(ctx, stringFromAny(d["icon"]))
}
func (c *QQOfficialChannel) MemberAvatar(ctx context.Context, uid string) (GroupAvatar, error) {
	e := directoryEvent(ctx)
	c.mu.RLock()
	source := c.avatarURLs[e.GroupID+"\x00"+uid]
	appID := c.cfg.AppID
	c.mu.RUnlock()
	if source == "" && (uid == appID || uid == c.Status().SelfID) {
		d, err := directoryData(c.CallAPI(ctx, "GET /users/@me", nil))
		if err != nil {
			return GroupAvatar{}, err
		}
		source = stringFromAny(d["avatar"])
	}
	if source == "" && e.PlatformScope == "qq_guild" {
		if _, err := c.GroupMember(ctx, e.GroupID, uid); err != nil {
			return GroupAvatar{}, err
		}
		c.mu.RLock()
		source = c.avatarURLs[e.GroupID+"\x00"+uid]
		c.mu.RUnlock()
	}
	return platformAvatar(ctx, source)
}
