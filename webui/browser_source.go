// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/browserbox"
	"github.com/SuInk/diana/model/browserctl"
	"github.com/SuInk/diana/model/browsersource"

	"github.com/gin-gonic/gin"
)

// BrowserSourceStore 存浏览器来源的优先级，由 model/storage 实现。
type BrowserSourceStore interface {
	LoadBrowserSource(ctx context.Context) (browsersource.Settings, bool, error)
	SaveBrowserSource(ctx context.Context, doc browsersource.Settings) error
}

// BrowserSourceHandler 管「机器人用哪个浏览器」：内置浏览器和扩展各自开关，外加一个
// 优先级。每一轮取排在最前、开着而且眼下用得上的那个（见 browsersource.Pick）。
//
// 开关本身不存在这里：内置浏览器的开关在它自己的配置里，扩展的开关是它策略里的总
// 开关，这里只是替用户同时拨动它们。这里只存优先级。
type BrowserSourceHandler struct {
	box     *browserbox.Manager
	control *browserctl.Registry
	hub     *browserctl.Hub
	store   BrowserSourceStore
	logs    AppLogWriter

	mu       sync.RWMutex
	settings browsersource.Settings
}

// NewBrowserSourceHandler 创建浏览器来源接口处理器，并读出已保存的优先级。
func NewBrowserSourceHandler(ctx context.Context, box *browserbox.Manager, control *browserctl.Registry, hub *browserctl.Hub, store BrowserSourceStore) *BrowserSourceHandler {
	h := &BrowserSourceHandler{box: box, control: control, hub: hub, store: store, settings: browsersource.Settings{}.WithDefaults()}
	if store != nil {
		if doc, ok, err := store.LoadBrowserSource(ctx); err == nil && ok {
			h.settings = doc.WithDefaults()
		}
	}
	return h
}

// SetLogStore 注入操作日志写入器。
func (h *BrowserSourceHandler) SetLogStore(store AppLogWriter) { h.logs = store }

// Register 注册浏览器来源接口。
func (h *BrowserSourceHandler) Register(router gin.IRouter) {
	router.GET("/api/browser-source", h.get)
	router.PUT("/api/browser-source", h.set)
}

// Current 返回这一轮该用的来源，运行时按它决定登记哪组浏览器工具。
//
// 「用得上」按各自最便宜的判断：内置浏览器开着、本机找得到 Chrome——进程没起来不要紧，
// 工具调用时会按需拉起；扩展要总开关开着、有扩展连着且没被接管。
func (h *BrowserSourceHandler) Current() string {
	h.mu.RLock()
	order := h.settings.Order
	h.mu.RUnlock()
	return browsersource.Pick(order, h.usable)
}

func (h *BrowserSourceHandler) usable(source string) bool {
	switch source {
	case browsersource.Box:
		return h.box.Settings().Enabled && h.box.Status().Available
	case browsersource.Extension:
		return h.hub.Ready()
	}
	return false
}

// browserSourceState 是浏览器页顶部那块要的全部状态。
type browserSourceState struct {
	Order     []string            `json:"order"`
	Active    string              `json:"active"`
	Box       browserSourceSwitch `json:"box"`
	Extension browserSourceSwitch `json:"extension"`
}

type browserSourceSwitch struct {
	Enabled bool `json:"enabled"`
	// Dependencies 是这一个浏览器的运行依赖，和插件页的「运行依赖」同一种形状：缺了
	// 什么、能不能一键装，一眼看得出来。
	Dependencies []assistant.ResolverDependency `json:"dependencies"`
	// Usable 表示这一轮能用上：内置浏览器是找得到 Chrome，扩展是有扩展连着且没被接管。
	Usable bool `json:"usable"`
	// Detected 表示检测到了：内置浏览器是本机找得到 Chrome，扩展是有扩展连上来过。
	Detected bool `json:"detected"`
}

func (h *BrowserSourceHandler) state() browserSourceState {
	h.mu.RLock()
	order := append([]string(nil), h.settings.Order...)
	h.mu.RUnlock()
	boxAvailable := h.box.Status().Available
	boxEnabled := h.box.Settings().Enabled
	extensionEnabled := h.control.Policy().Enabled
	connections := h.hub.Connections()
	return browserSourceState{
		Order:  order,
		Active: browsersource.Pick(order, h.usable),
		Box: browserSourceSwitch{
			Enabled: boxEnabled, Usable: boxEnabled && boxAvailable, Detected: boxAvailable,
			Dependencies: boxDependencies(),
		},
		Extension: browserSourceSwitch{
			Enabled: extensionEnabled, Usable: h.hub.Ready(), Detected: len(connections) > 0,
			Dependencies: []assistant.ResolverDependency{extensionDependency(connections)},
		},
	}
}

// boxDependencies 是内置浏览器的运行依赖。浏览器和中文字体复用网页渲染插件那套探测
// 和一键安装——找的是同一个 Chrome；显示器只影响能不能开真窗口，没有也能无头跑。
func boxDependencies() []assistant.ResolverDependency {
	deps := assistant.BrowserDependencies()
	available, detail := browserbox.DisplayStatus()
	display := assistant.ResolverDependency{
		Name:      "display",
		Purpose:   "开真窗口：图形会话或 Xvfb 虚拟屏（可选）",
		Available: available,
	}
	if available {
		display.Version = detail
	} else {
		display.Detail = detail
	}
	return append(deps, display)
}

// extensionDependency 把「扩展有没有连上来」说成一条运行依赖。它没法一键安装：扩展要
// 用户自己装进自己的 Chrome。
func extensionDependency(connections []browserctl.ConnectionStatus) assistant.ResolverDependency {
	dep := assistant.ResolverDependency{
		Name:    "browser-extension",
		Purpose: "Diana 浏览器控制扩展：装在你的 Chrome 里，反向连到这里",
	}
	if len(connections) == 0 {
		dep.Detail = "还没有扩展连上来。打开开关后，在下面下载扩展源码包，到 chrome://extensions 用「加载已解压的扩展程序」装上"
		return dep
	}
	dep.Available = true
	first := connections[0]
	dep.Version = strings.TrimSpace(first.Browser + " " + first.BrowserVersion)
	if dep.Version == "" {
		dep.Version = "已连接"
	}
	if len(connections) > 1 {
		dep.Version += fmt.Sprintf(" 等 %d 个", len(connections))
	}
	return dep
}

func (h *BrowserSourceHandler) get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, h.state())
}

func (h *BrowserSourceHandler) set(c *gin.Context) {
	var payload struct {
		Order            []string `json:"order"`
		BoxEnabled       *bool    `json:"box_enabled"`
		ExtensionEnabled *bool    `json:"extension_enabled"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, errors.New("浏览器来源格式错误"))
		return
	}
	ctx := c.Request.Context()
	if payload.BoxEnabled != nil {
		if err := h.setBoxEnabled(ctx, *payload.BoxEnabled); err != nil {
			logAndWriteError(c, h.logs, http.StatusInternalServerError, "browser_source", err, "", nil)
			return
		}
	}
	if payload.ExtensionEnabled != nil {
		if err := h.setExtensionEnabled(ctx, *payload.ExtensionEnabled); err != nil {
			logAndWriteError(c, h.logs, http.StatusInternalServerError, "browser_source", err, "", nil)
			return
		}
	}
	if payload.Order != nil {
		next := browsersource.Settings{Order: payload.Order}.WithDefaults()
		if h.store != nil {
			if err := h.store.SaveBrowserSource(ctx, next); err != nil {
				logAndWriteError(c, h.logs, http.StatusInternalServerError, "browser_source", err, "", nil)
				return
			}
		}
		h.mu.Lock()
		h.settings = next
		h.mu.Unlock()
	}
	state := h.state()
	recordRequestOperation(c, h.logs, "browser_source", "浏览器来源已更新", "", map[string]any{
		"order":             state.Order,
		"active":            state.Active,
		"box_enabled":       state.Box.Enabled,
		"extension_enabled": state.Extension.Enabled,
	})
	c.JSON(http.StatusOK, state)
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
