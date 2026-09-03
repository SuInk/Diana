// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
)

// ExternalMessageTarget 描述对外开放接口要投递到的会话。Platform/ProfileID
// 与出站路由的语义一致：ProfileID 优先精确匹配，缺省时按平台路由，两者都空
// 且只有一条启用通道时投给它。
type ExternalMessageTarget struct {
	Platform  string
	ProfileID string
	GroupID   string
	UserID    string
}

// PushExternalMessage 把外部系统推来的文本作为通知投递到指定会话。走通知
// 投递路径而不是聊天路径：推送正文是调用方生成的完整事实，人设分条只会把
// 它拦腰截断。群聊目标同时带 UserID 时会 @ 那个人，与订阅推送的点名规则一致。
func (r *Runtime) PushExternalMessage(ctx context.Context, target ExternalMessageTarget, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("diana: message text is required")
	}
	event := MessageEvent{
		Kind:      EventKindPrivate,
		Platform:  NormalizePlatformID(target.Platform),
		ProfileID: strings.TrimSpace(target.ProfileID),
		UserID:    strings.TrimSpace(target.UserID),
	}
	if groupID := strings.TrimSpace(target.GroupID); groupID != "" {
		event.Kind = EventKindGroup
		event.GroupID = groupID
	}
	if event.GroupID == "" && event.UserID == "" {
		return fmt.Errorf("diana: either group_id or user_id is required")
	}
	return r.sendNotification(ctx, event, text)
}
