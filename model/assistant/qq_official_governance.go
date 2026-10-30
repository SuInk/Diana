// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// QQ 官方机器人的群管能力按场景分两档：
//
//   - QQ 群（group_openid）：撤回机器人自己的消息、成员禁言/解禁、移出成员（可同时
//     拉黑）有接口，见 qq_official_group.go；禁言和入群审批要求机器人是群管理员，
//     移出、成员名单只对白名单机器人开放。全员禁言只能查不能设，精华、公告没有接口。
//   - 单聊：只能撤回。
//   - QQ 频道（guild）：成员禁言、全员禁言、移出成员、子频道精华（pins）都有接口，
//     机器人需要在频道里有管理权限。
//
// platformWriteOperationSupport 里 QQ 官方列的是频道能做的全集，这里再按当前消息
// 来自哪个场景挡一次。

const qqOfficialGroupSceneRefusal = "QQ 官方机器人在 QQ 群里只能撤回自己的消息、禁言和移出成员，全员禁言、精华这类群管接口开放平台只在 QQ 频道里提供"

// qqOfficialSceneSupports 判断当前场景能不能做这项操作；非 QQ 官方平台一律放行。
func (t *dianaPlatformTool) qqOfficialSceneSupports(platform, operation string) error {
	if NormalizePlatformID(platform) != PlatformQQOfficial || operation == platformOpRecall {
		return nil
	}
	if t.event.PlatformScope == "qq_group" && t.event.Kind == EventKindGroup {
		switch operation {
		case platformOpMute, platformOpUnmute, platformOpKick:
			return nil
		}
	}
	if t.event.PlatformScope != "qq_guild" || strings.TrimSpace(t.event.GuildID) == "" {
		return fmt.Errorf("%s", qqOfficialGroupSceneRefusal)
	}
	return nil
}

// qqOfficialGuildTarget 返回当前子频道所属的频道 ID。频道接口按 guild_id 寻址，
// 而工具的 group_id 是子频道 ID；只认当前这条消息带来的映射，不拿别的子频道去猜。
func (t *dianaPlatformTool) qqOfficialGuildTarget(channelID string) (string, error) {
	guild := strings.TrimSpace(t.event.GuildID)
	if t.event.PlatformScope != "qq_guild" || guild == "" {
		return "", fmt.Errorf("%s", qqOfficialGroupSceneRefusal)
	}
	if strings.TrimSpace(channelID) != strings.TrimSpace(t.event.GroupID) {
		return "", fmt.Errorf("QQ 频道的群管操作只能作用于当前子频道")
	}
	return guild, nil
}

func (t *dianaPlatformTool) callQQOfficial(ctx context.Context, method, path string, params map[string]any) (map[string]any, error) {
	return t.runtime.callPlatformAPIForEvent(ctx, t.event, method+" "+path, params)
}

// dispatchQQOfficialModeration 对应 mute/unmute/kick。
func (t *dianaPlatformTool) dispatchQQOfficialModeration(ctx context.Context, operation, channelID, target string, duration int, rejectAdd bool) (map[string]any, error) {
	if t.event.PlatformScope == "qq_group" {
		return t.dispatchQQGroupModeration(ctx, operation, channelID, target, duration, rejectAdd)
	}
	guild, err := t.qqOfficialGuildTarget(channelID)
	if err != nil {
		return nil, err
	}
	member := "/guilds/" + url.PathEscape(guild) + "/members/" + url.PathEscape(target)
	switch operation {
	case platformOpMute:
		// mute_seconds 是字符串，0 表示解除。
		return t.callQQOfficial(ctx, "PATCH", member+"/mute", map[string]any{"mute_seconds": strconv.Itoa(duration)})
	case platformOpUnmute:
		return t.callQQOfficial(ctx, "PATCH", member+"/mute", map[string]any{"mute_seconds": "0"})
	case platformOpKick:
		// add_blacklist 对应 reject_add_request：移出的同时拉黑，不让再加回来。
		return t.callQQOfficial(ctx, "DELETE", member, map[string]any{"add_blacklist": rejectAdd, "delete_history_msg_days": 0})
	}
	return nil, fmt.Errorf("当前平台暂不支持此操作")
}

// dispatchQQOfficialGovernance 对应精华（子频道 pins）和全员禁言。
func (t *dianaPlatformTool) dispatchQQOfficialGovernance(ctx context.Context, operation, channelID string, req governanceRequest) (map[string]any, error) {
	guild, err := t.qqOfficialGuildTarget(channelID)
	if err != nil {
		return nil, err
	}
	pin := "/channels/" + url.PathEscape(channelID) + "/pins/" + url.PathEscape(req.messageID)
	switch operation {
	case platformOpEssenceSet:
		return t.callQQOfficial(ctx, "PUT", pin, nil)
	case platformOpEssenceUnset:
		return t.callQQOfficial(ctx, "DELETE", pin, nil)
	case platformOpMuteAll:
		// 频道的全员禁言必须带时长，没有「直到手动解除」；取和成员禁言相同的 30 天上限。
		return t.callQQOfficial(ctx, "PATCH", "/guilds/"+url.PathEscape(guild)+"/mute", map[string]any{"mute_seconds": strconv.Itoa(oneBotMaxMuteSeconds)})
	case platformOpUnmuteAll:
		return t.callQQOfficial(ctx, "PATCH", "/guilds/"+url.PathEscape(guild)+"/mute", map[string]any{"mute_seconds": "0"})
	}
	return nil, fmt.Errorf("当前平台暂不支持此操作")
}

// qqOfficialDeleteMessagePath 按会话场景挑撤回接口。QQ 群和单聊只能撤回机器人自己
// 两分钟内的消息；频道里有管理权限时也能撤别人的，hidetip 不留「撤回了一条消息」的提示。
func qqOfficialDeleteMessagePath(event MessageEvent, messageID string) (string, error) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return "", fmt.Errorf("缺少消息 ID")
	}
	id := url.PathEscape(messageID)
	switch {
	case event.Kind == EventKindGroup && event.PlatformScope == "qq_guild":
		return "/channels/" + url.PathEscape(strings.TrimSpace(event.GroupID)) + "/messages/" + id + "?hidetip=true", nil
	case event.Kind == EventKindGroup && strings.TrimSpace(event.GroupID) != "":
		return "/v2/groups/" + url.PathEscape(strings.TrimSpace(event.GroupID)) + "/messages/" + id, nil
	case strings.TrimSpace(event.UserID) != "":
		return "/v2/users/" + url.PathEscape(strings.TrimSpace(event.UserID)) + "/messages/" + id, nil
	}
	return "", fmt.Errorf("缺少会话 ID")
}

// runQQOfficialGuildList 列出机器人加入的 QQ 频道。QQ 群没有列表接口：机器人只在
// 群里收到消息或群事件时才知道 group_openid，所以结果里要说清楚群不在其中。
func (t *dianaPlatformTool) runQQOfficialGuildList(ctx context.Context, operation string, owner bool, access string) (string, error) {
	data, err := t.callQQOfficial(ctx, "GET", "/users/@me/guilds", map[string]any{"limit": 100})
	if err != nil {
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", err)
		return "", err
	}
	// 接口直接回数组，CallAPI 把它原样放在 result 里。
	var raw []any
	encoded, ok := data["result"].(json.RawMessage)
	if ok {
		ok = json.Unmarshal(encoded, &raw) == nil
	}
	if !ok {
		err := fmt.Errorf("频道列表返回格式无法识别")
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", err)
		return "", err
	}
	groups := make([]dianaPlatformGroupItem, 0, len(raw))
	for _, value := range raw {
		item, ok := value.(map[string]any)
		if !ok {
			continue
		}
		id := strings.TrimSpace(stringFromAny(item["id"]))
		if id == "" {
			continue
		}
		count, _ := strconv.Atoi(strings.TrimSpace(stringFromAny(item["member_count"])))
		groups = append(groups, dianaPlatformGroupItem{GroupID: id, GroupName: strings.TrimSpace(stringFromAny(item["name"])), MemberCount: count})
	}
	t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", nil)
	return t.marshalGroupList(operation, access, owner, groups, map[string]any{
		"scope_note": "这是机器人加入的 QQ 频道；QQ 群开放平台不提供列表接口，不在其中。",
	})
}
