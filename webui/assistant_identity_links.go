// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"net/http"
	"strings"

	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

// 跨平台账号关联：控制台只看和删。建立关联走聊天里的验证码或主人工具，见
// model/assistant/identity_links.go。

type identityLinksRuntime interface {
	ListIdentityLinks(ctx context.Context) ([]assistant.IdentityAccount, error)
	UnlinkIdentityAccount(ctx context.Context, account assistant.IdentityAccount) (bool, error)
}

// listIdentityLinks 按人返回全部账号关联。
func (h *BotHandler) listIdentityLinks(c *gin.Context) {
	runtime, ok := h.runtime.(identityLinksRuntime)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "机器人运行时不支持账号关联"})
		return
	}
	items, err := runtime.ListIdentityLinks(c.Request.Context())
	if err != nil {
		h.writeError(c, http.StatusServiceUnavailable, "identity_links_list", err, "", nil)
		return
	}
	people := assistant.GroupIdentityLinks(items)
	if people == nil {
		people = []assistant.IdentityPerson{}
	}
	c.JSON(http.StatusOK, gin.H{"people": people})
}

// unlinkIdentityAccount 解除一个账号的关联。
func (h *BotHandler) unlinkIdentityAccount(c *gin.Context) {
	runtime, ok := h.runtime.(identityLinksRuntime)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "机器人运行时不支持账号关联"})
		return
	}
	var req struct {
		ProfileID string `json:"profile_id"`
		Platform  string `json:"platform"`
		UserID    string `json:"user_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.ProfileID) == "" || strings.TrimSpace(req.Platform) == "" || strings.TrimSpace(req.UserID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "必须指定机器人、平台和用户 ID"})
		return
	}
	account := assistant.IdentityAccount{ProfileID: req.ProfileID, Platform: req.Platform, UserID: req.UserID}
	removed, err := runtime.UnlinkIdentityAccount(c.Request.Context(), account)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "identity_links_unlink", err, req.ProfileID, nil)
		return
	}
	if !removed {
		c.JSON(http.StatusNotFound, gin.H{"error": "这个账号没有关联"})
		return
	}
	recordRequestOperation(c, h.logs, "identity_links_unlink", "已解除账号关联", req.ProfileID, map[string]any{"platform": req.Platform})
	c.JSON(http.StatusOK, gin.H{"removed": true})
}
