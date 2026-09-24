// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SuInk/diana/model/browserbox"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// BrowserBoxHandler 提供内置浏览器的管理接口和实时画面。
//
// 实时画面是这一档的关键：浏览器在容器里无头跑着，用户要能看见它在干什么，
// 还要能直接上手——登录、过人机验证、临时接管，都得由人自己来。画面走 CDP 的
// screencast，输入走 Input.dispatch*，不需要 VNC，也不需要虚拟显示器。
type BrowserBoxHandler struct {
	manager  *browserbox.Manager
	logs     AppLogWriter
	upgrader websocket.Upgrader
}

// NewBrowserBoxHandler 创建内置浏览器接口处理器。
func NewBrowserBoxHandler(manager *browserbox.Manager) *BrowserBoxHandler {
	return &BrowserBoxHandler{
		manager: manager,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 64 * 1024,
			// 实时画面端点在 /api 下，已经过了 WebUI 的会话鉴权；这里只要求同源，
			// 不接受跨站页面把它连走。
			CheckOrigin: sameOriginWebSocket,
		},
	}
}

// SetLogStore 注入操作日志写入器。
func (h *BrowserBoxHandler) SetLogStore(store AppLogWriter) { h.logs = store }

// Register 注册管理接口与实时画面端点。
func (h *BrowserBoxHandler) Register(router gin.IRouter) {
	router.GET("/api/browser-box/status", h.status)
	router.PUT("/api/browser-box/settings", h.setSettings)
	router.POST("/api/browser-box/start", h.start)
	router.POST("/api/browser-box/stop", h.stop)
	router.POST("/api/browser-box/takeover", h.setTakeover)
	router.GET("/api/browser-box/tabs", h.listTabs)
	router.POST("/api/browser-box/tabs", h.openTab)
	router.DELETE("/api/browser-box/tabs/:id", h.closeTab)
	router.GET("/api/browser-box/live", h.live)
}

// botFor 取请求里 ?bot= 指的那台机器人的浏览器。每台机器人各有一份登录态，
// 进程相关的操作不指明机器人就无从下手，这里直接拒掉而不是猜一台。
func (h *BrowserBoxHandler) botFor(c *gin.Context) (*browserbox.Bot, bool) {
	botID := strings.TrimSpace(c.Query("bot"))
	if botID == "" {
		writeError(c, http.StatusBadRequest, errors.New("内置浏览器按机器人各用一份登录态，请先选一台机器人"))
		return nil, false
	}
	return h.manager.Bot(botID), true
}

func (h *BrowserBoxHandler) status(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if botID := strings.TrimSpace(c.Query("bot")); botID != "" {
		c.JSON(http.StatusOK, h.manager.Bot(botID).Status())
		return
	}
	c.JSON(http.StatusOK, h.manager.Status())
}

func (h *BrowserBoxHandler) setSettings(c *gin.Context) {
	var payload browserbox.Settings
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, errors.New("内置浏览器配置格式错误"))
		return
	}
	saved, err := h.manager.SetSettings(c.Request.Context(), payload)
	if err != nil {
		logAndWriteError(c, h.logs, http.StatusInternalServerError, "browser_box_settings", err, "", nil)
		return
	}
	recordRequestOperation(c, h.logs, "browser_box_settings", "内置浏览器配置已更新", "", map[string]any{
		"enabled": saved.Enabled,
		"headful": saved.Headful,
	})
	status := h.manager.Status()
	if botID := strings.TrimSpace(c.Query("bot")); botID != "" {
		status = h.manager.Bot(botID).Status()
	}
	c.JSON(http.StatusOK, gin.H{"settings": saved, "status": status})
}

func (h *BrowserBoxHandler) start(c *gin.Context) {
	bot, ok := h.botFor(c)
	if !ok {
		return
	}
	if err := bot.Start(c.Request.Context()); err != nil {
		logAndWriteError(c, h.logs, http.StatusInternalServerError, "browser_box_start", err, bot.ID(), nil)
		return
	}
	recordRequestOperation(c, h.logs, "browser_box_start", "你启动了内置浏览器", bot.ID(), browserBoxLogMetadata(bot, nil))
	c.JSON(http.StatusOK, gin.H{"status": bot.Status()})
}

func (h *BrowserBoxHandler) stop(c *gin.Context) {
	bot, ok := h.botFor(c)
	if !ok {
		return
	}
	bot.Stop()
	recordRequestOperation(c, h.logs, "browser_box_stop", "你停止了内置浏览器", bot.ID(), browserBoxLogMetadata(bot, nil))
	c.JSON(http.StatusOK, gin.H{"status": bot.Status()})
}

func (h *BrowserBoxHandler) setTakeover(c *gin.Context) {
	var payload struct {
		Active bool `json:"active"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, errors.New("请求格式错误"))
		return
	}
	bot, ok := h.botFor(c)
	if !ok {
		return
	}
	bot.SetTakeover(payload.Active)
	message := "你把内置浏览器交还给机器人"
	if payload.Active {
		message = "你接管了内置浏览器"
	}
	recordRequestOperation(c, h.logs, "browser_box_takeover", message, bot.ID(), browserBoxLogMetadata(bot, map[string]any{"active": payload.Active}))
	c.JSON(http.StatusOK, gin.H{"ok": true, "active": payload.Active})
}

func (h *BrowserBoxHandler) listTabs(c *gin.Context) {
	bot, ok := h.botFor(c)
	if !ok {
		return
	}
	base := bot.CDPURL()
	if base == "" {
		writeError(c, http.StatusServiceUnavailable, errors.New("内置浏览器没有运行"))
		return
	}
	tabs, err := browserbox.ListTabs(c.Request.Context(), base)
	if err != nil {
		writeError(c, http.StatusBadGateway, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"tabs": tabs})
}

func (h *BrowserBoxHandler) openTab(c *gin.Context) {
	var payload struct {
		URL string `json:"url"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, errors.New("请求格式错误"))
		return
	}
	bot, ok := h.botFor(c)
	if !ok {
		return
	}
	base := bot.CDPURL()
	if base == "" {
		writeError(c, http.StatusServiceUnavailable, errors.New("内置浏览器没有运行"))
		return
	}
	target := strings.TrimSpace(payload.URL)
	if target == "" {
		target = "about:blank"
	} else if !h.manager.Settings().HostAllowed(target) {
		logAndWriteError(c, h.logs, http.StatusForbidden, "browser_box_tab_open", errors.New("这个地址不在内置浏览器允许的范围内"), target, nil)
		return
	}
	tab, err := browserbox.OpenTab(c.Request.Context(), base, target)
	if err != nil {
		logAndWriteError(c, h.logs, http.StatusBadGateway, "browser_box_tab_open", err, target, nil)
		return
	}
	// 从控制台让内置浏览器打开地址是一次真实的外部访问，要留审计。
	recordRequestOperation(c, h.logs, "browser_box_tab_open", "内置浏览器已打开标签页", target, nil)
	c.JSON(http.StatusOK, gin.H{"tab": tab})
}

func (h *BrowserBoxHandler) closeTab(c *gin.Context) {
	bot, ok := h.botFor(c)
	if !ok {
		return
	}
	base := bot.CDPURL()
	if base == "" {
		writeError(c, http.StatusServiceUnavailable, errors.New("内置浏览器没有运行"))
		return
	}
	if err := browserbox.CloseTab(c.Request.Context(), base, c.Param("id")); err != nil {
		logAndWriteError(c, h.logs, http.StatusBadGateway, "browser_box_tab_close", err, c.Param("id"), nil)
		return
	}
	recordRequestOperation(c, h.logs, "browser_box_tab_close", "内置浏览器已关闭标签页", c.Param("id"), nil)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// liveMessage 是前端发过来的指令。画面反过来是 {"type":"frame"}。
type liveMessage struct {
	Type  string                 `json:"type"`
	Mouse *browserbox.MouseEvent `json:"mouse,omitempty"`
	Key   *browserbox.KeyEvent   `json:"key,omitempty"`
	Text  string                 `json:"text,omitempty"`
	URL   string                 `json:"url,omitempty"`
}

const (
	liveWriteTimeout = 10 * time.Second
	liveReadLimit    = 1 << 20
)

// live 把一个标签页的画面推给前端，并把前端的鼠标键盘事件送回浏览器。
func (h *BrowserBoxHandler) live(c *gin.Context) {
	bot, ok := h.botFor(c)
	if !ok {
		return
	}
	base := bot.CDPURL()
	if base == "" {
		writeError(c, http.StatusServiceUnavailable, errors.New("内置浏览器没有运行"))
		return
	}
	tabID := strings.TrimSpace(c.Query("tab"))
	tabs, err := browserbox.ListTabs(c.Request.Context(), base)
	if err != nil {
		writeError(c, http.StatusBadGateway, err)
		return
	}
	target, ok := pickBrowserBoxTab(tabs, tabID)
	if !ok {
		// 一个标签页都没有时开一个空白页，用户至少有个地方输地址。
		opened, err := browserbox.OpenTab(c.Request.Context(), base, "about:blank")
		if err != nil {
			writeError(c, http.StatusBadGateway, err)
			return
		}
		target = opened
	}

	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	conn.SetReadLimit(liveReadLimit)

	settings := h.manager.Settings()
	live, err := browserbox.StartLive(c.Request.Context(), target.WebSocketDebuggerURL, target.URL,
		settings.WindowWidth, settings.WindowHeight)
	if err != nil {
		_ = conn.WriteJSON(gin.H{"type": "error", "message": err.Error()})
		return
	}
	defer live.Close()

	done := make(chan struct{})
	go func() {
		defer recoverGoroutinePanic("browser_box.live")
		defer close(done)
		for {
			var message liveMessage
			if err := conn.ReadJSON(&message); err != nil {
				return
			}
			h.handleLiveMessage(c, bot, live, message)
		}
	}()

	_ = conn.WriteJSON(gin.H{"type": "ready", "tab": target})
	for {
		select {
		case frame, ok := <-live.Frames():
			if !ok {
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(liveWriteTimeout))
			if err := conn.WriteJSON(gin.H{"type": "frame", "frame": frame}); err != nil {
				return
			}
		case <-done:
			return
		case <-c.Request.Context().Done():
			return
		}
	}
}

// handleLiveMessage 把前端的一条指令翻成 CDP 调用。
//
// 用户在这块画面上的操作等于人工接管，所以第一次输入就把接管打开：不这样的话
// 用户正在填表，模型同时在点别的地方，两边抢同一个页面。
func (h *BrowserBoxHandler) handleLiveMessage(c *gin.Context, bot *browserbox.Bot, live *browserbox.Live, message liveMessage) {
	ctx := c.Request.Context()
	switch message.Type {
	case "mouse":
		if message.Mouse == nil {
			return
		}
		h.takeOverFromLive(c, bot)
		_ = live.Mouse(ctx, *message.Mouse)
	case "key":
		if message.Key == nil {
			return
		}
		h.takeOverFromLive(c, bot)
		_ = live.Key(ctx, *message.Key)
	case "text":
		h.takeOverFromLive(c, bot)
		_ = live.Text(ctx, message.Text)
	case "navigate":
		target := strings.TrimSpace(message.URL)
		if target == "" || !h.manager.Settings().HostAllowed(target) {
			return
		}
		h.takeOverFromLive(c, bot)
		recordRequestOperation(c, h.logs, "browser_box_navigate", "你在内置浏览器里打开了网页", target, browserBoxLogMetadata(bot, map[string]any{"url": target}))
		_ = live.Navigate(ctx, target)
	case "reload":
		_ = live.Reload(ctx)
	case "back":
		_ = live.Back(ctx)
	}
}

// takeOverFromLive 在用户第一次在画面上动手时打开接管，并只在这一下记一条：鼠标移动
// 每秒几十次，逐条记会把操作记录淹掉。
func (h *BrowserBoxHandler) takeOverFromLive(c *gin.Context, bot *browserbox.Bot) {
	if bot.Takeover() {
		return
	}
	bot.SetTakeover(true)
	recordRequestOperation(c, h.logs, "browser_box_takeover", "你在画面上动手，内置浏览器已自动转为你接管", bot.ID(), browserBoxLogMetadata(bot, map[string]any{"active": true, "auto": true}))
}

// browserBoxLogMetadata 给内置浏览器的操作记录带上机器人 ID，浏览器页按它筛。
func browserBoxLogMetadata(bot *browserbox.Bot, extra map[string]any) map[string]any {
	metadata := map[string]any{"profile_id": bot.ID(), "source": "box"}
	for key, value := range extra {
		metadata[key] = value
	}
	return metadata
}

func pickBrowserBoxTab(tabs []browserbox.Target, id string) (browserbox.Target, bool) {
	for _, tab := range tabs {
		if id != "" && tab.ID == id {
			return tab, true
		}
	}
	if id == "" && len(tabs) > 0 {
		return tabs[0], true
	}
	return browserbox.Target{}, false
}

// sameOriginWebSocket 只接受同源的升级请求。实时画面端点带着 WebUI 的会话，
// 跨站页面能连上就等于能看你的浏览器，所以这里不留「没有 Origin 就放过」。
func sameOriginWebSocket(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		// 非浏览器客户端（比如命令行）不带 Origin，但它也带不上浏览器里的会话
		// Cookie；这条放行的是本机脚本，不是跨站页面。
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}
