// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

// interactiveBrowserTools 是接上 CDP 后模型能用的那几个工具，按扩展页展示的顺序列出。
var interactiveBrowserTools = []string{"browser_open", "browser_text", "browser_click", "browser_type", "browser_screenshot"}

type agentBrowserPayload struct {
	ProfileID string `json:"profile_id"`
	CDPURL    string `json:"cdp_url"`
	TimeoutMS int    `json:"timeout_ms"`
}

func (h *BotHandler) agentBrowser(c *gin.Context) {
	profile := c.Query("profile")
	cfg, ok := h.profiles.Profiles().ConfigForProfile(profile)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"tools": interactiveBrowserTools})
		return
	}
	cfg = cfg.WithDefaults()
	c.JSON(http.StatusOK, gin.H{
		"profile_id": cfg.ID,
		"cdp_url":    cfg.AgentBrowserCDPURL,
		"timeout_ms": cfg.AgentBrowserTimeoutMS,
		"tools":      interactiveBrowserTools,
	})
}

func (h *BotHandler) setAgentBrowser(c *gin.Context) {
	var payload agentBrowserPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "agent_browser", err, "", nil)
		return
	}
	if err := validateCDPURL(payload.CDPURL); err != nil {
		h.writeError(c, http.StatusBadRequest, "agent_browser", err, payload.ProfileID, nil)
		return
	}
	next, ok := h.profiles.Profiles().WithAgentBrowser(payload.ProfileID, payload.CDPURL, payload.TimeoutMS)
	if !ok {
		h.writeError(c, http.StatusNotFound, "agent_browser", fmt.Errorf("profile %q not found", payload.ProfileID), payload.ProfileID, nil)
		return
	}
	current, _ := next.ConfigForProfile(payload.ProfileID)
	if err := h.applyProfileSet(next); err != nil && !errors.Is(err, assistant.ErrBotDisabled) {
		h.writeError(c, http.StatusBadRequest, "agent_browser", err, botLogTarget(current), nil)
		return
	}
	if err := h.profiles.SaveProfiles(next); err != nil {
		h.writeError(c, http.StatusInternalServerError, "agent_browser", err, botLogTarget(current), nil)
		return
	}
	recordRequestOperation(c, h.logs, "agent_browser", "交互式浏览器接入已更新", botLogTarget(current), map[string]any{"cdp_url": current.AgentBrowserCDPURL})
	c.JSON(http.StatusOK, gin.H{"cdp_url": current.AgentBrowserCDPURL, "timeout_ms": current.AgentBrowserTimeoutMS, "tools": interactiveBrowserTools})
}

// testAgentBrowser 探一次 CDP 端点。
//
// 保存时不弹错误，用户就只能等模型某次调用失败才知道地址是错的——群里报的
// MCP 预设也是同一个毛病。这里现场探一次，把失败原文直接摆在界面上。
func (h *BotHandler) testAgentBrowser(c *gin.Context) {
	var payload agentBrowserPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "agent_browser_test", err, "", nil)
		return
	}
	target := strings.TrimSpace(payload.CDPURL)
	if target == "" {
		if cfg, ok := h.profiles.Profiles().ConfigForProfile(payload.ProfileID); ok {
			target = cfg.WithDefaults().AgentBrowserCDPURL
		}
	}
	if err := validateCDPURL(target); err != nil {
		c.JSON(http.StatusOK, gin.H{"connected": false, "error": err.Error()})
		return
	}
	timeout := 5 * time.Second
	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(target, "/")+"/json/version", nil)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"connected": false, "error": err.Error()})
		return
	}
	response, err := (&http.Client{Timeout: timeout}).Do(request)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"connected": false, "error": err.Error()})
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		c.JSON(http.StatusOK, gin.H{"connected": false, "error": fmt.Sprintf("CDP 返回 %s", response.Status)})
		return
	}
	var version struct {
		Browser string `json:"Browser"`
	}
	_ = json.NewDecoder(response.Body).Decode(&version)
	c.JSON(http.StatusOK, gin.H{"connected": true, "browser": strings.TrimSpace(version.Browser)})
}

// validateCDPURL 只接受 http/https 的绝对地址。CDP 地址会被服务端直接请求，
// 放开 scheme 等于把控制台变成任意协议的探测器。
func validateCDPURL(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		// 空表示回到默认地址，由 WithDefaults 填。
		return nil
	}
	if !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
		return fmt.Errorf("浏览器 CDP 地址必须以 http:// 或 https:// 开头")
	}
	return nil
}
