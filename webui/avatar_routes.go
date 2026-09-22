// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

const (
	avatarKindGroup  = "group"
	avatarKindMember = "member"
)

// registerAvatarRoutes 注册按内容寻址的头像端点。
func (h *BotHandler) registerAvatarRoutes(router gin.IRouter) {
	router.GET("/api/assistant/avatars/:kind/:id", h.serveAvatar)
}

// avatarCacheKey 把「哪台机器人的哪个对象」拼成缓存键。同一个群号在不同平台上
// 是不同的东西，profile 也要进键。
func avatarCacheKey(kind, profileID, id string) string {
	return kind + "\x00" + strings.TrimSpace(profileID) + "\x00" + strings.TrimSpace(id)
}

// consoleAvatarURL 拼控制台用的头像地址。已经知道内容哈希就带上 v=：带哈希的地址
// 按 immutable 缓存，浏览器一年内不会再问；不知道就先不带，等浏览器取过一次之后
// 下次列表就有了。
func (h *BotHandler) consoleAvatarURL(kind, profileID, id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	avatarURL := "/api/assistant/avatars/" + url.PathEscape(kind) + "/" + url.PathEscape(id)
	query := url.Values{}
	if profileID = strings.TrimSpace(profileID); profileID != "" {
		query.Set("bot_profile_id", profileID)
	}
	if sha := h.avatarStore().cachedSHA(avatarCacheKey(kind, profileID, id)); sha != "" {
		query.Set("v", sha)
	}
	if len(query) > 0 {
		avatarURL += "?" + query.Encode()
	}
	return avatarURL
}

func (h *BotHandler) serveAvatar(c *gin.Context) {
	kind := strings.TrimSpace(c.Param("kind"))
	id := strings.TrimSpace(c.Param("id"))
	if id == "" || (kind != avatarKindGroup && kind != avatarKindMember) {
		c.Status(http.StatusNotFound)
		return
	}
	profileID := strings.TrimSpace(c.Query("bot_profile_id"))
	entry, ok := h.avatarStore().load(c.Request.Context(), avatarCacheKey(kind, profileID, id), func(ctx context.Context, previous *avatarEntry) (*avatarEntry, error) {
		return h.fetchAvatar(ctx, kind, profileID, id, previous)
	})
	if !ok || entry == nil || len(entry.body) == 0 {
		// 没有头像、机器人已退群、平台不支持——对前端都是一回事：显示占位图。
		c.Status(http.StatusNotFound)
		return
	}
	etag := `"` + entry.sha + `"`
	c.Header("ETag", etag)
	if requested := strings.TrimSpace(c.Query("v")); requested != "" && requested == entry.sha {
		// 地址里带的哈希就是当前内容，这条地址永远对应这张图，可以放心让浏览器
		// 一直留着。换了头像列表会给出新地址。
		c.Header("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		// 地址没带哈希（或带的是旧的）：短缓存 + ETag，浏览器下次拿 304。
		c.Header("Cache-Control", "private, max-age=60, must-revalidate")
	}
	if match := strings.TrimSpace(c.GetHeader("If-None-Match")); match != "" && strings.Contains(match, entry.sha) {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, entry.contentType, entry.body)
}

// fetchAvatar 按平台取头像原件。QQ 有稳定的头像地址，直接带条件请求去取；
// 其它平台的地址里带着 Bot Token 之类的东西，只能由通道自己取回字节。
func (h *BotHandler) fetchAvatar(ctx context.Context, kind, profileID, id string, previous *avatarEntry) (*avatarEntry, error) {
	if remote := h.remoteAvatarURL(kind, profileID, id); remote != "" {
		return h.avatarStore().fetchRemoteAvatar(ctx, remote, previous)
	}
	if kind != avatarKindGroup {
		return nil, errUnexpectedAvatarStatus
	}
	provider, ok := h.runtime.(groupAvatarRuntime)
	if !ok {
		return nil, errUnexpectedAvatarStatus
	}
	avatar, found := provider.GroupAvatarForProfile(ctx, profileID, id)
	if !found || len(avatar.Data) == 0 || !strings.HasPrefix(avatar.ContentType, "image/") {
		return nil, errUnexpectedAvatarStatus
	}
	return &avatarEntry{sha: avatarContentSHA(avatar.Data), contentType: avatar.ContentType, body: avatar.Data}, nil
}

// remoteAvatarURL 给出可以直接下载的头像地址，没有就返回空串。
func (h *BotHandler) remoteAvatarURL(kind, profileID, id string) string {
	// 用 isOneBotProfile 而不是直接看配置：它带着老部署的兜底（事件里 profile_id
	// 为空、整个部署又只有 OneBot 机器人时仍按 OneBot 算）。
	if !h.isOneBotProfile(profileID) {
		return ""
	}
	if kind == avatarKindGroup {
		return assistant.OneBotGroupAvatarURL(id)
	}
	return assistant.OneBotMemberAvatarURL(id)
}
