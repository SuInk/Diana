// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type GroupMemberDirectory struct {
	Members    []OneBotGroupMemberInfo
	Complete   bool
	Total      int
	TotalKnown bool
	Source     string
	Warnings   []string
}

type GroupMemberChannel interface {
	GroupMember(context.Context, string, string) (OneBotGroupMemberInfo, error)
	GroupMembers(context.Context, string) (GroupMemberDirectory, error)
}

type telegramKnownMember struct {
	User telegramUser
	Seen time.Time
}
type telegramChatMember struct {
	User        telegramUser `json:"user"`
	Status      string       `json:"status"`
	IsMember    bool         `json:"is_member"`
	CustomTitle string       `json:"custom_title"`
}
type telegramMemberUpdate struct {
	Chat telegramChat       `json:"chat"`
	Old  telegramChatMember `json:"old_chat_member"`
	New  telegramChatMember `json:"new_chat_member"`
}

func (m telegramChatMember) present() bool {
	switch m.Status {
	case "creator", "administrator", "member":
		return true
	case "restricted":
		return m.IsMember
	default:
		return false
	}
}

func telegramMemberInfo(groupID string, user telegramUser) OneBotGroupMemberInfo {
	return OneBotGroupMemberInfo{GroupID: groupID, UserID: strconv.FormatInt(user.ID, 10), Nickname: telegramDisplayName(&user), Username: user.Username, IsBot: user.IsBot}
}

func (c *TelegramChannel) rememberMember(groupID string, user telegramUser, seedOnly ...bool) {
	if groupID == "" || user.ID <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.knownMembers == nil {
		c.knownMembers = map[string]map[int64]telegramKnownMember{}
	}
	if c.knownMembers[groupID] == nil {
		if len(c.knownMembers) >= 256 {
			for old := range c.knownMembers {
				delete(c.knownMembers, old)
				break
			}
		}
		c.knownMembers[groupID] = map[int64]telegramKnownMember{}
	}
	members := c.knownMembers[groupID]
	if len(seedOnly) > 0 && seedOnly[0] {
		if _, exists := members[user.ID]; exists {
			return
		}
	}
	if _, ok := members[user.ID]; !ok && len(members) >= 500 {
		var oldest int64
		var seen time.Time
		for id, item := range members {
			if oldest == 0 || item.Seen.Before(seen) {
				oldest, seen = id, item.Seen
			}
		}
		delete(members, oldest)
	}
	members[user.ID] = telegramKnownMember{User: user, Seen: time.Now()}
}

func (c *TelegramChannel) forgetMember(groupID string, userID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.knownMembers[groupID], userID)
}

func (c *TelegramChannel) observeMemberMessage(m *telegramMessage) {
	if m == nil || m.Chat == nil || (m.Chat.Type != "group" && m.Chat.Type != "supergroup") {
		return
	}
	groupID := strconv.FormatInt(m.Chat.ID, 10)
	// Anonymous administrators and channel senders are not ordinary user accounts.
	if m.From != nil && m.SenderChat == nil {
		c.rememberMember(groupID, *m.From)
	}
	for _, user := range m.NewChatMembers {
		c.rememberMember(groupID, user)
	}
	if m.LeftChatMember != nil {
		c.forgetMember(groupID, m.LeftChatMember.ID)
	}
}

func (c *TelegramChannel) observeMemberUpdate(update *telegramMemberUpdate, self bool) {
	if update == nil {
		return
	}
	groupID := strconv.FormatInt(update.Chat.ID, 10)
	if !update.New.present() {
		if self {
			c.mu.Lock()
			delete(c.knownMembers, groupID)
			c.mu.Unlock()
		} else {
			c.forgetMember(groupID, update.New.User.ID)
		}
		return
	}
	c.rememberMember(groupID, update.New.User)
}

func (c *TelegramChannel) GroupMember(ctx context.Context, groupID, userID string) (OneBotGroupMemberInfo, error) {
	groupID = strings.TrimSpace(groupID)
	id, err := strconv.ParseInt(strings.TrimSpace(userID), 10, 64)
	if err != nil || id <= 0 || groupID == "" {
		return OneBotGroupMemberInfo{}, fmt.Errorf("telegram: valid chat_id and user_id are required")
	}
	raw, err := c.callRaw(ctx, "getChatMember", map[string]any{"chat_id": groupID, "user_id": id})
	if err != nil {
		return OneBotGroupMemberInfo{}, fmt.Errorf("telegram: cannot verify membership (bot administrator rights may be required): %w", err)
	}
	var member telegramChatMember
	if err = json.Unmarshal(raw, &member); err != nil {
		return OneBotGroupMemberInfo{}, err
	}
	if member.User.ID != id {
		return OneBotGroupMemberInfo{}, fmt.Errorf("telegram: member response has a different user_id")
	}
	if !member.present() {
		c.forgetMember(groupID, id)
		return OneBotGroupMemberInfo{}, fmt.Errorf("telegram: user is not a current chat member (status %s)", member.Status)
	}
	c.rememberMember(groupID, member.User)
	info := telegramMemberInfo(groupID, member.User)
	info.Role = "member"
	if member.Status == "creator" {
		info.Role = "owner"
	} else if member.Status == "administrator" {
		info.Role = "admin"
	}
	info.Title = member.CustomTitle
	info.MembershipVerified = true
	return info, nil
}

func (c *TelegramChannel) GroupMembers(ctx context.Context, groupID string) (GroupMemberDirectory, error) {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return GroupMemberDirectory{}, fmt.Errorf("telegram: chat_id is required")
	}
	result := GroupMemberDirectory{Source: "administrators_and_observed", Complete: false}
	members := map[string]OneBotGroupMemberInfo{}
	c.mu.RLock()
	for _, known := range c.knownMembers[groupID] {
		info := telegramMemberInfo(groupID, known.User)
		members[info.UserID] = info
	}
	c.mu.RUnlock()
	raw, err := c.callRaw(ctx, "getChatAdministrators", map[string]any{"chat_id": groupID, "return_bots": true})
	if err != nil {
		result.Warnings = append(result.Warnings, "管理员列表暂不可用："+err.Error())
	} else {
		var admins []telegramChatMember
		if err := json.Unmarshal(raw, &admins); err != nil {
			return result, err
		}
		for _, admin := range admins {
			if !admin.present() || admin.User.ID <= 0 {
				continue
			}
			c.rememberMember(groupID, admin.User)
			info := telegramMemberInfo(groupID, admin.User)
			info.Role = "admin"
			if admin.Status == "creator" {
				info.Role = "owner"
			}
			info.Title = admin.CustomTitle
			info.MembershipVerified = true
			members[info.UserID] = info
		}
	}
	raw, countErr := c.callRaw(ctx, "getChatMemberCount", map[string]any{"chat_id": groupID})
	if countErr == nil {
		var count *int
		if json.Unmarshal(raw, &count) == nil && count != nil && *count >= 0 {
			result.Total = *count
			result.TotalKnown = true
		}
	} else {
		result.Warnings = append(result.Warnings, "群人数暂不可用："+countErr.Error())
	}
	if len(members) == 0 && err != nil {
		return result, err
	}
	for _, member := range members {
		result.Members = append(result.Members, member)
	}
	sort.Slice(result.Members, func(i, j int) bool { return result.Members[i].UserID < result.Members[j].UserID })
	return result, nil
}
