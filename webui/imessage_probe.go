// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

type imessageProbePayload struct {
	ProfileID string `json:"profile_id"`
	ServerURL string `json:"server_url"`
	Password  string `json:"password"`
}

// testIMessageServer 用表单里的地址和密码请求一次 BlueBubbles 的 server/info。
//
// 密码留空时沿用已保存的那个，但只在地址也没改的情况下：否则填一个别处的地址
// 点测试，就能把存着的密码送到任意服务器上。
func (h *BotHandler) testIMessageServer(c *gin.Context) {
	var payload imessageProbePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "imessage_test", err, "", nil)
		return
	}
	serverURL := strings.TrimSpace(payload.ServerURL)
	password := strings.TrimSpace(payload.Password)
	if cfg, ok := h.profiles.Profiles().ConfigForProfile(payload.ProfileID); ok {
		stored := strings.TrimRight(strings.TrimSpace(cfg.IMessageServerURL), "/")
		if serverURL == "" {
			serverURL = stored
		}
		if password == "" && strings.TrimRight(serverURL, "/") == stored {
			password = cfg.IMessagePassword
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	info, err := assistant.ProbeIMessageServer(ctx, &http.Client{Timeout: 10 * time.Second}, serverURL, password)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"connected": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"connected":         true,
		"server_version":    info.ServerVersion,
		"os_version":        info.OSVersion,
		"private_api":       info.PrivateAPI,
		"helper_connected":  info.HelperConnected,
		"detected_imessage": info.DetectedIMessage,
	})
}
