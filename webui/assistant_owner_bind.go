// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

// QQ 官方机器人绑定主人：控制台生成验证码，主人把「绑定主人 验证码」发给机器人，
// 运行时认出发送者的 openid 存成主人。验证码和校验都在运行时里，见
// model/assistant/qq_official_owner_bind.go。

type ownerBindRuntime interface {
	CreateOwnerBindCode(profileID string) (string, time.Duration, error)
	OwnerBindStatus(profileID string) assistant.OwnerBindStatus
}

func (h *BotHandler) ownerBindRuntime(c *gin.Context) (ownerBindRuntime, string, bool) {
	profileID := botProfileScope(c)
	if profileID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "必须指定机器人"})
		return nil, "", false
	}
	runtime, ok := h.runtime.(ownerBindRuntime)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "机器人运行时不支持绑定主人"})
		return nil, "", false
	}
	return runtime, profileID, true
}

// createOwnerBindCode 生成绑定验证码，旧码同时作废。
func (h *BotHandler) createOwnerBindCode(c *gin.Context) {
	runtime, profileID, ok := h.ownerBindRuntime(c)
	if !ok {
		return
	}
	code, ttl, err := runtime.CreateOwnerBindCode(profileID)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "owner_bind_code", err, profileID, nil)
		return
	}
	recordRequestOperation(c, h.logs, "owner_bind_code", "已生成主人绑定验证码", profileID, nil)
	c.JSON(http.StatusOK, gin.H{"code": code, "command": "绑定主人 " + code, "expires_in_seconds": int(ttl.Seconds())})
}

// ownerBindStatus 给控制台轮询：主人绑上了就带回 owner_id。
func (h *BotHandler) ownerBindStatus(c *gin.Context) {
	runtime, profileID, ok := h.ownerBindRuntime(c)
	if !ok {
		return
	}
	status := runtime.OwnerBindStatus(profileID)
	status.OwnerID = strings.TrimSpace(status.OwnerID)
	c.JSON(http.StatusOK, status)
}
