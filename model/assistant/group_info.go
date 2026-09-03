// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// GroupInfo 是控制台展示一个群所需的最小信息。
type GroupInfo struct {
	GroupID     string
	GroupName   string
	MemberCount int
}

// GroupInfoChannel 由「知道自己在哪个群里、但没有『列出全部群』接口」的通道实现。
//
// OneBot 有 get_group_list，一次就能拿到全量；Telegram 的 Bot API 没有对应接口，
// 但只要知道群号就能用 getChat 问到这个群此刻的名字。控制台先从本地事件得到群号，
// 再用这个接口补齐名字，就不必等群里下一条消息才显示得出名称，改过名的群也能立刻
// 跟上。
type GroupInfoChannel interface {
	GroupInfo(ctx context.Context, groupID string) (GroupInfo, error)
}

// GroupInfo 用 getChat 查询群的当前信息。
func (c *TelegramChannel) GroupInfo(ctx context.Context, groupID string) (GroupInfo, error) {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return GroupInfo{}, fmt.Errorf("telegram: group id is required")
	}
	data, err := c.CallAPI(ctx, "getChat", map[string]any{"chat_id": groupID})
	if err != nil {
		return GroupInfo{}, err
	}
	info := GroupInfo{GroupID: groupID}
	// getChat 的响应外面还套一层 result，历史上也出现过直接返回内容的形态，
	// 两种都认。
	fields := data
	if nested, ok := data["result"].(map[string]any); ok {
		fields = nested
	}
	info.GroupName = strings.TrimSpace(stringFromAny(fields["title"]))
	return info, nil
}

// GroupAvatar 是一张群头像的原始内容。故意不返回 URL：Telegram 的文件地址里带着
// Bot Token，只能由服务端自己去取，再由控制台的鉴权接口转发出去。
type GroupAvatar struct {
	Data        []byte
	ContentType string
}

// GroupAvatarChannel 由能按群号取到群头像的通道实现。
type GroupAvatarChannel interface {
	GroupAvatar(ctx context.Context, groupID string) (GroupAvatar, error)
}

// telegramGroupAvatarMaxBytes 是群头像的下载上限。Telegram 的群头像最大边 640，
// 正常在几十 KB；留够余量即可，不必按普通媒体的上限放行。
const telegramGroupAvatarMaxBytes = 4 << 20

// GroupAvatar 取群头像：先用 getChat 拿到头像的 file_id，再下载文件内容。
func (c *TelegramChannel) GroupAvatar(ctx context.Context, groupID string) (GroupAvatar, error) {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return GroupAvatar{}, fmt.Errorf("telegram: group id is required")
	}
	data, err := c.CallAPI(ctx, "getChat", map[string]any{"chat_id": groupID})
	if err != nil {
		return GroupAvatar{}, err
	}
	fields := data
	if nested, ok := data["result"].(map[string]any); ok {
		fields = nested
	}
	photo, _ := fields["photo"].(map[string]any)
	if photo == nil {
		return GroupAvatar{}, fmt.Errorf("telegram: group %s has no photo", groupID)
	}
	// 控制台列表里显示的是小图；拿不到 small 再退回 big。
	fileID := strings.TrimSpace(stringFromAny(photo["small_file_id"]))
	if fileID == "" {
		fileID = strings.TrimSpace(stringFromAny(photo["big_file_id"]))
	}
	if fileID == "" {
		return GroupAvatar{}, fmt.Errorf("telegram: group %s has no photo file id", groupID)
	}
	body, _, err := c.downloadFileByID(ctx, fileID, telegramGroupAvatarMaxBytes)
	if err != nil {
		return GroupAvatar{}, err
	}
	// 内容类型按实际字节判定，不信任文件名后缀。
	return GroupAvatar{Data: body, ContentType: http.DetectContentType(body)}, nil
}

// GroupAvatarForProfile 找到这台机器人对应的通道，取某个群的头像。
// 通道不支持或取不到时返回 false，由调用方决定退回占位显示。
func (r *Runtime) GroupAvatarForProfile(ctx context.Context, profileID, groupID string) (GroupAvatar, bool) {
	provider, ok := channelFor[GroupAvatarChannel](r, profileID)
	if !ok {
		return GroupAvatar{}, false
	}
	avatar, err := provider.GroupAvatar(ctx, groupID)
	if err != nil || len(avatar.Data) == 0 {
		return GroupAvatar{}, false
	}
	return avatar, true
}

// channelFor 找到这台机器人的通道并断言成某个可选能力接口。
func channelFor[T any](r *Runtime, profileID string) (T, bool) {
	var zero T
	if r == nil {
		return zero, false
	}
	r.mu.RLock()
	channel := r.channel
	r.mu.RUnlock()
	if channel == nil {
		return zero, false
	}
	target := channel
	if multi, ok := channel.(*MultiChannel); ok {
		binding, err := multi.bindingFor(strings.TrimSpace(profileID), "")
		if err != nil {
			return zero, false
		}
		target = binding.Channel
	}
	provider, ok := target.(T)
	return provider, ok
}

// GroupInfoForProfile 找到这台机器人对应的通道，问它某个群的当前信息。
// 通道不支持（比如 OneBot 走的是 get_group_list 那条路）时返回 false。
func (r *Runtime) GroupInfoForProfile(ctx context.Context, profileID, groupID string) (GroupInfo, bool) {
	provider, ok := channelFor[GroupInfoChannel](r, profileID)
	if !ok {
		return GroupInfo{}, false
	}
	info, err := provider.GroupInfo(ctx, groupID)
	if err != nil || strings.TrimSpace(info.GroupName) == "" {
		return GroupInfo{}, false
	}
	return info, true
}
