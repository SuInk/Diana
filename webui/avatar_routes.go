// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

const (
	avatarKindGroup  = "group"
	avatarKindMember = "member"
	// profileAvatarWarmWait 是配置接口为补机器人头像哈希最多多等的时间。
	profileAvatarWarmWait = 2 * time.Second
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
// 地址只指向本机代理；Telegram Bot Token 始终留在服务端。
func (h *BotHandler) withConsoleProfileAvatars(payload assistant.ConfigPayload) assistant.ConfigPayload {
	accounts := h.profileAvatarAccounts(payload)
	enrich := func(profile *assistant.ConfigPayload) {
		if account, ok := accounts[strings.TrimSpace(profile.ID)]; ok {
			profile.AvatarURL = h.consoleProfileAvatarURL(profile.ID, account)
		}
	}
	for index := range payload.Profiles {
		enrich(&payload.Profiles[index])
	}
	enrich(&payload)
	return payload
}

// profileAvatarAccounts 列出要由系统推头像的机器人：profile → 取头像用的账号，
// 账号为空表示暂时推不出来。管理员自己填了头像的不在里面。运行态暂时还没有
// self_id 时退回已记住的 bot_account，连接建立后下一次刷新会换成实时账号。
func (h *BotHandler) profileAvatarAccounts(payload assistant.ConfigPayload) map[string]string {
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
	accounts := map[string]string{}
	add := func(profile assistant.ConfigPayload) {
		if stored := strings.TrimSpace(profile.AvatarURL); stored != "" && !isDerivedProfileAvatarURL(stored) {
			return
		}
		profileID := strings.TrimSpace(profile.ID)
		accounts[profileID] = firstNonEmptyWebUI(selfIDs[profileID], strings.TrimSpace(profile.BotAccount))
	}
	for _, profile := range payload.Profiles {
		add(profile)
	}
	add(payload)
	return accounts
}

// warmProfileAvatars 在拼配置响应之前把还不知道哈希的机器人头像取一遍。头像缓存
// 只在进程内，重启（升级）后的头一次配置响应拿不到哈希，只能发不带 v= 的地址，
// 浏览器手上那张永久缓存的图就对不上，每次重启都要重新下载。内容哈希只跟图片
// 有关，这里取到了，地址就和重启前一模一样。机器人就几台，并行取，最多等
// profileAvatarWarmWait；取不到的照旧发不带哈希的地址，失败结果有短缓存，
// 下一次配置请求不会再等。
func (h *BotHandler) warmProfileAvatars(ctx context.Context, payload assistant.ConfigPayload) {
	ctx, cancel := context.WithTimeout(ctx, profileAvatarWarmWait)
	defer cancel()
	store := h.avatarStore()
	var wg sync.WaitGroup
	for profileID, account := range h.profileAvatarAccounts(payload) {
		if account == "" {
			continue
		}
		key := avatarCacheKey(avatarKindMember, profileID, account)
		if sha, _ := store.knownSHA(key); sha != "" {
			// 已经有哈希就不等：过期的由 consoleProfileAvatarURL 在后台校验。
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer recoverGoroutinePanic("avatar_routes.warmProfileAvatars")
			store.load(ctx, key, func(ctx context.Context, previous *avatarEntry) (*avatarEntry, error) {
				return h.fetchAvatar(ctx, avatarKindMember, profileID, account, previous)
			})
		}()
	}
	wg.Wait()
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
	if kind == avatarKindMember {
		// 空 profile 被兜底认成官方机器人时，数字 QQ 号拼不出地址，接着按 OneBot 走。
		if profile, ok := h.qqOfficialProfile(profileID); ok {
			if remote := assistant.QQOfficialMemberAvatarURL(profile.QQAppID, id); remote != "" {
				return remote
			}
		}
	}
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

// qqOfficialProfile 找出这条记录所属的 QQ 官方机器人。单机器人部署的事件里
// profile_id 常是空的：这时只有一台官方机器人就算它的，有几台就不猜。
func (h *BotHandler) qqOfficialProfile(profileID string) (assistant.BotConfig, bool) {
	if h.profiles == nil {
		return assistant.BotConfig{}, false
	}
	set := h.profiles.Profiles()
	if profile, ok := set.ConfigForProfile(profileID); ok {
		return profile, assistant.NormalizePlatformID(profile.Platform) == assistant.PlatformQQOfficial
	}
	if strings.TrimSpace(profileID) != "" {
		return assistant.BotConfig{}, false
	}
	var found assistant.BotConfig
	count := 0
	for _, profile := range set.Profiles {
		if assistant.NormalizePlatformID(profile.Platform) == assistant.PlatformQQOfficial {
			found = profile
			count++
		}
	}
	return found, count == 1
}
