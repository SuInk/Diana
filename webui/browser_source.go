// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"errors"
	"net/http"

	"github.com/SuInk/diana/model/browserbox"
	"github.com/SuInk/diana/model/browserctl"
	"github.com/SuInk/diana/model/browsersource"

	"github.com/gin-gonic/gin"
)

// BrowserSourceHandler 管「机器人用哪个浏览器」：关闭、Diana 内置、用户自己的
// Chrome（扩展）。
//
// 它不存自己的状态：选哪个就是把那一边的总开关打开、另一边关掉，读的时候再从
// 两个开关推回来（见 browsersource.Resolve）。
type BrowserSourceHandler struct {
	box     *browserbox.Manager
	control *browserctl.Registry
	hub     *browserctl.Hub
	logs    AppLogWriter
}

// NewBrowserSourceHandler 创建浏览器来源接口处理器。
func NewBrowserSourceHandler(box *browserbox.Manager, control *browserctl.Registry, hub *browserctl.Hub) *BrowserSourceHandler {
	return &BrowserSourceHandler{box: box, control: control, hub: hub}
}

// SetLogStore 注入操作日志写入器。
func (h *BrowserSourceHandler) SetLogStore(store AppLogWriter) { h.logs = store }

// Register 注册浏览器来源接口。
func (h *BrowserSourceHandler) Register(router gin.IRouter) {
	router.GET("/api/browser-source", h.get)
	router.PUT("/api/browser-source", h.set)
}

// Current 返回当前来源，运行时按它决定登记哪组浏览器工具。
func (h *BrowserSourceHandler) Current() string {
	return browsersource.Resolve(h.box.Settings().Enabled, h.control.Policy().Enabled)
}

func (h *BrowserSourceHandler) get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"source": h.Current()})
}

func (h *BrowserSourceHandler) set(c *gin.Context) {
	var payload struct {
		Source string `json:"source"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil || !browsersource.Valid(payload.Source) {
		writeError(c, http.StatusBadRequest, errors.New("浏览器来源只能是 off、box 或 extension"))
		return
	}
	if err := h.apply(c.Request.Context(), payload.Source); err != nil {
		logAndWriteError(c, h.logs, http.StatusInternalServerError, "browser_source", err, "", map[string]any{"source": payload.Source})
		return
	}
	current := h.Current()
	recordRequestOperation(c, h.logs, "browser_source", "浏览器来源已切换", "", map[string]any{"source": current})
	c.JSON(http.StatusOK, gin.H{"source": current})
}

// apply 先打开目标那一边，再关掉另一边。顺序是有意的：内置浏览器的有头配置可能
// 在打开时被拒，先开就能在失败时什么都不动，而不是把原来那边关了、新的又没开
// 起来。中间短暂两边都开着的那一下，Resolve 按内置浏览器优先解释，工具那一侧
// 看到的始终只有一个。
func (h *BrowserSourceHandler) apply(ctx context.Context, source string) error {
	switch source {
	case browsersource.Box:
		if err := h.setBoxEnabled(ctx, true); err != nil {
			return err
		}
		return h.setExtensionEnabled(ctx, false)
	case browsersource.Extension:
		if err := h.setExtensionEnabled(ctx, true); err != nil {
			return err
		}
		return h.setBoxEnabled(ctx, false)
	default:
		if err := h.setBoxEnabled(ctx, false); err != nil {
			return err
		}
		return h.setExtensionEnabled(ctx, false)
	}
}

func (h *BrowserSourceHandler) setBoxEnabled(ctx context.Context, enabled bool) error {
	settings := h.box.Settings()
	if settings.Enabled == enabled {
		return nil
	}
	settings.Enabled = enabled
	_, err := h.box.SetSettings(ctx, settings)
	return err
}

func (h *BrowserSourceHandler) setExtensionEnabled(ctx context.Context, enabled bool) error {
	policy := h.control.Policy()
	if policy.Enabled == enabled {
		return nil
	}
	policy.Enabled = enabled
	if _, err := h.control.SetPolicy(ctx, policy); err != nil {
		return err
	}
	// 和扩展面板上关总开关一样：关掉就当场断开已连着的扩展。
	if !enabled {
		h.hub.CloseAll()
	}
	return nil
}
