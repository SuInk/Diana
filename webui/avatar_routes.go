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
	return buildConsoleAvatarURL(kind, profileID, id, h.avatarStore().cachedSHA(avatarCacheKey(kind, profileID, id)))
}

// consoleProfileAvatarURL 给侧栏和机器人列表里的机器人头像拼地址。跟群列表不同，
// 哈希过了校验期也照发旧的，同时在后台回源校验：过期就退回不带哈希的地址的话，
// 每 6 小时地址就在「永久缓存」和「60 秒缓存」之间来回换一次，左上角头像每次都
// 重新下载、闪一下。机器人就那么几台，后台校验的量可以忽略；换了头像的话，校验
// 完的下一次配置刷新就拿到新地址。
func (h *BotHandler) consoleProfileAvatarURL(profileID, id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	key := avatarCacheKey(avatarKindMember, profileID, id)
	sha, stale := h.avatarStore().knownSHA(key)
	if stale {
		go func() {
			defer recoverGoroutinePanic("avatar_routes.consoleProfileAvatarURL")
			ctx, cancel := context.WithTimeout(context.Background(), avatarFetchTimeout)
			defer cancel()
			h.avatarStore().load(ctx, key, func(ctx context.Context, previous *avatarEntry) (*avatarEntry, error) {
				return h.fetchAvatar(ctx, avatarKindMember, profileID, id, previous)
			})
		}()
	}
	return buildConsoleAvatarURL(avatarKindMember, profileID, id, sha)
}

func buildConsoleAvatarURL(kind, profileID, id, sha string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	avatarURL := "/api/assistant/avatars/" + url.PathEscape(kind) + "/" + url.PathEscape(id)
	query := url.Values{}
	if profileID = strings.TrimSpace(profileID); profileID != "" {
		query.Set("bot_profile_id", profileID)
	}
	if sha != "" {
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
	var avatar assistant.GroupAvatar
	var found bool
	switch kind {
	case avatarKindGroup:
		provider, ok := h.runtime.(groupAvatarRuntime)
		if !ok {
			return nil, errUnexpectedAvatarStatus
		}
		avatar, found = provider.GroupAvatarForProfile(ctx, profileID, id)
	case avatarKindMember:
		provider, ok := h.runtime.(memberAvatarRuntime)
		if !ok {
			return nil, errUnexpectedAvatarStatus
		}
		avatar, found = provider.MemberAvatarForProfile(ctx, profileID, id)
	default:
		return nil, errUnexpectedAvatarStatus
	}
	if !found || len(avatar.Data) == 0 || !strings.HasPrefix(avatar.ContentType, "image/") {
		return nil, errUnexpectedAvatarStatus
	}
	return &avatarEntry{sha: avatarContentSHA(avatar.Data), contentType: avatar.ContentType, body: avatar.Data}, nil
}

// withConsoleProfileAvatars 给配置响应补上后台可直接显示的机器人头像地址。
// 地址只指向本机代理；Telegram Bot Token 始终留在服务端。运行态暂时还没有
// self_id 时退回已记住的 bot_account，连接建立后下一次刷新会换成实时账号。
func (h *BotHandler) withConsoleProfileAvatars(payload assistant.ConfigPayload) assistant.ConfigPayload {
	selfIDs := map[string]string{}
	if h != nil && h.runtime != nil {
		status := h.runtime.Status()
		for _, channel := range status.Channels {
			if profileID := strings.TrimSpace(channel.ProfileID); profileID != "" {
				selfIDs[profileID] = strings.TrimSpace(channel.SelfID)
			}
		}
		if len(payload.Profiles) == 1 && selfIDs[payload.Profiles[0].ID] == "" {
			selfIDs[payload.Profiles[0].ID] = strings.TrimSpace(status.Channel.SelfID)
		}
	}
	enrich := func(profile *assistant.ConfigPayload) {
		if profile == nil {
			return
		}
		if stored := strings.TrimSpace(profile.AvatarURL); stored != "" && !isDerivedProfileAvatarURL(stored) {
			return
		}
		account := firstNonEmptyWebUI(selfIDs[strings.TrimSpace(profile.ID)], strings.TrimSpace(profile.BotAccount))
		if account != "" {
			profile.AvatarURL = h.consoleProfileAvatarURL(profile.ID, account)
		} else {
			profile.AvatarURL = ""
		}
	}
	for index := range payload.Profiles {
		enrich(&payload.Profiles[index])
	}
	enrich(&payload)
	return payload
}

// isDerivedProfileAvatarURL 判断机器人头像地址是不是系统按账号推出来的（QQ 头像
// 或本机头像代理），而不是管理员自己填的。旧版前端会把推出来的地址原样存回配置，
// 换了账号或平台后它就指向别人：Telegram 机器人一直显示某个 QQ 号的头像。
// 这类地址每次都按当前账号重新算，不当真。
func isDerivedProfileAvatarURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "/api/assistant/avatars/") {
		return true
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "qlogo.cn" || strings.HasSuffix(host, ".qlogo.cn")
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
