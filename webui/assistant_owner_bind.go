// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// QQ 官方机器人绑定主人：主人在聊天里发「绑定主人」拿到验证码，填进控制台，
// 运行时把这个验证码对应的 openid 存成主人。验证码和校验都在运行时里，见
// model/assistant/qq_official_owner_bind.go。

type ownerBindRuntime interface {
	BindOwnerByCode(profileID, code string) (string, error)
}

// bindOwnerByCode 用聊天里拿到的验证码绑定主人，成功时返回主人 ID。
func (h *BotHandler) bindOwnerByCode(c *gin.Context) {
	profileID := botProfileScope(c)
	if profileID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "必须指定机器人"})
		return
	}
	runtime, ok := h.runtime.(ownerBindRuntime)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "机器人运行时不支持绑定主人"})
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Code) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写验证码"})
		return
	}
	ownerID, err := runtime.BindOwnerByCode(profileID, req.Code)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "owner_bind", err, profileID, nil)
		return
	}
	recordRequestOperation(c, h.logs, "owner_bind", "已绑定主人", profileID, nil)
	c.JSON(http.StatusOK, gin.H{"owner_id": ownerID})
}
