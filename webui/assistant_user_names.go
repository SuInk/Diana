// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// 账号昵称查询：控制台里凡是「填一个 QQ 号 / 用户 ID」的输入框都靠它把一串数字
// 变回看得懂的名字。纯读接口，不写任何东西。
//
// 两个来源，先本地后远端：
//   - 本地人员画像，机器人见过这个人就有名字，零成本；
//   - OneBot get_stranger_info，没见过的人也认得，但要打一次网络请求。
//
// 查不到就返回空，前端什么都不显示——输入框里那串号码本来就还是有效的，
// 没必要为了「查不到」再占一行去解释。

const (
	// userNameLookupLimit 是单次请求最多查几个号。放开等于让一次粘贴把一长串号码
	// 全丢给 OneBot。
	userNameLookupLimit = 20
	// userNameLookupCallTimeout 是单次 OneBot 调用的上限，
	// userNameLookupBudget 是整个请求的上限——不封顶的话 20 个号能串成 50 秒。
	userNameLookupCallTimeout = 2500 * time.Millisecond
	userNameLookupBudget      = 4 * time.Second
	userNameCacheTTL          = 5 * time.Minute
	userNameCacheMaxSize      = 512
)

type userNameCacheEntry struct {
	name      string
	fetchedAt time.Time
}

type assistantUserNamesResponse struct {
	// Names 只放查到的，查不到的号直接不出现在 map 里。
	Names map[string]string `json:"names"`
}

// lookupAssistantUserNames 批量把用户 ID 换成昵称。
func (h *BotHandler) lookupAssistantUserNames(c *gin.Context) {
	ids := parseUserNameLookupIDs(c.Query("ids"))
	names := make(map[string]string, len(ids))
	if len(ids) == 0 {
		c.JSON(http.StatusOK, assistantUserNamesResponse{Names: names})
		return
	}
	profileID := botProfileScope(c)
	pending := make([]string, 0, len(ids))
	for _, id := range ids {
		if name, ok := h.cachedUserName(profileID, id); ok {
			if name != "" {
				names[id] = name
			}
			continue
		}
		pending = append(pending, id)
	}

	lookupCtx, cancel := context.WithTimeout(c.Request.Context(), userNameLookupBudget)
	defer cancel()
	for _, id := range pending {
		if lookupCtx.Err() != nil {
			break
		}
		name := h.resolveUserName(lookupCtx, profileID, id)
		// 预算耗尽导致的空结果不能进缓存，否则一次超时能让这个号五分钟查不出名字。
		if lookupCtx.Err() == nil {
			h.storeUserName(profileID, id, name)
		}
		if name != "" {
			names[id] = name
		}
	}
	c.JSON(http.StatusOK, assistantUserNamesResponse{Names: names})
}

// resolveUserName 先查本地画像，再问 OneBot。
func (h *BotHandler) resolveUserName(ctx context.Context, profileID string, userID string) string {
	if h.sqlite != nil {
		profile, found, err := h.sqlite.GetUserMemory(ctx, profileID, userID)
		if err == nil && found {
			// 画像里没抓到昵称时 DisplayName 会退化成用户 ID 本身，
			// 那不是昵称，原样返回等于在号码旁边再写一遍号码。
			if name := strings.TrimSpace(profile.DisplayName); name != "" && name != userID {
				return name
			}
		}
	}
	return h.strangerNickname(ctx, profileID, userID)
}

// strangerNickname 问 OneBot 要陌生人昵称。
func (h *BotHandler) strangerNickname(ctx context.Context, profileID string, userID string) string {
	// 只有 OneBot 有 get_stranger_info。Telegram 的 Bot API 没有「查任意用户」这种
	// 接口，机器人只认得跟自己说过话的人，所以那边只能靠本地画像。
	if h == nil || h.runtime == nil || !h.isOneBotProfile(profileID) {
		return ""
	}
	if ctx == nil {
		ctx = context.Background()
	}
	callCtx, cancel := context.WithTimeout(ctx, userNameLookupCallTimeout)
	defer cancel()
	data, err := h.runtime.CallOneBotAPI(callCtx, "get_stranger_info", map[string]any{
		"user_id":  oneBotIDParam(userID),
		"no_cache": false,
	})
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(firstNonEmptyWebUI(stringFromAnyWebUI(data["nickname"]), stringFromAnyWebUI(data["nick"])))
	if name == userID {
		return ""
	}
	return name
}

// parseUserNameLookupIDs 解析 ids=1,2,3，顺带去重和限流。
func parseUserNameLookupIDs(raw string) []string {
	seen := make(map[string]struct{}, userNameLookupLimit)
	ids := make([]string, 0, userNameLookupLimit)
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t' }) {
		id := strings.TrimSpace(part)
		// 只认纯数字：这些输入框填的都是 QQ 号或 Telegram 数字 ID，
		// 放开别的字符等于把任意字符串转手发给 OneBot。
		if _, err := strconv.ParseInt(id, 10, 64); err != nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		if len(ids) >= userNameLookupLimit {
			break
		}
	}
	return ids
}

func userNameCacheKey(profileID string, userID string) string {
	return profileID + "\x00" + userID
}

func (h *BotHandler) cachedUserName(profileID string, userID string) (string, bool) {
	h.userNameMu.Lock()
	defer h.userNameMu.Unlock()
	entry, ok := h.userNameCache[userNameCacheKey(profileID, userID)]
	if !ok || time.Since(entry.fetchedAt) >= userNameCacheTTL {
		return "", false
	}
	return entry.name, true
}

// storeUserName 记下结果，查不到也记——不然每敲一次键盘都要再问一次 OneBot。
func (h *BotHandler) storeUserName(profileID string, userID string, name string) {
	h.userNameMu.Lock()
	defer h.userNameMu.Unlock()
	if h.userNameCache == nil {
		h.userNameCache = make(map[string]userNameCacheEntry, userNameCacheMaxSize)
	}
	// 边打字边查会把没写完的号码也攒进来，满了就整份丢掉重来：
	// 丢了最多多打一次请求，不值得为它上一套 LRU。
	if len(h.userNameCache) >= userNameCacheMaxSize {
		h.userNameCache = make(map[string]userNameCacheEntry, userNameCacheMaxSize)
	}
	h.userNameCache[userNameCacheKey(profileID, userID)] = userNameCacheEntry{name: name, fetchedAt: time.Now()}
}
