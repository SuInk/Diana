// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
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
	h := &BrowserBoxHandler{
		manager: manager,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 64 * 1024,
			// 实时画面端点在 /api 下，已经过了 WebUI 的会话鉴权；这里只要求同源，
			// 不接受跨站页面把它连走。
			CheckOrigin: sameOriginWebSocket,
		},
	}
	// 闲置交还和手动交还记成同一种操作，浏览器页的操作记录里能看出是自动交还的。
	manager.OnIdleRelease(func(botID string, idle time.Duration) {
		bot := manager.Bot(botID)
		message := fmt.Sprintf("你接管后 %d 分钟没有操作，内置浏览器已自动交还给机器人", int(idle/time.Minute))
		recordOperation(context.Background(), h.logs, "browser_box_takeover", message, bot.ID(),
			browserBoxLogMetadata(bot, map[string]any{"active": false, "auto": true, "idle_seconds": int(idle / time.Second)}))
	})
	return h
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
	recordRequestOperation(c, h.logs, "browser_box_start", "你打开浏览器页，内置浏览器随之启动", bot.ID(), browserBoxLogMetadata(bot, nil))
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

// liveMessage 是前端发过来的指令。画面反过来走二进制帧，见 writeLiveFrame。
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
	// liveFramesInFlight 是发出去、前端还没画完的帧最多几帧。前端每画完一帧回一条
	// ack 才发下一帧，和 VNC 让客户端画完再要下一帧是一个道理：以前有帧就发，远程
	// 连接带宽跟不上时帧在缓冲里越排越长，画面落后好几秒。留两帧是为了传一帧的同时
	// 前端在画上一帧，链路不空转。
	liveFramesInFlight = 2
	// livePingInterval 是画面静止时的保活间隔。页面不动就没有帧，中间的代理把空闲
	// 连接掐掉，画面就一闪一闪地重连。
	livePingInterval = 25 * time.Second
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
	acks := make(chan struct{}, liveFramesInFlight)
	go func() {
		defer recoverGoroutinePanic("browser_box.live")
		defer close(done)
		for {
			var message liveMessage
			if err := conn.ReadJSON(&message); err != nil {
				return
			}
			if message.Type == "ack" {
				select {
				case acks <- struct{}{}:
				default:
				}
				continue
			}
			h.handleLiveMessage(c, bot, live, message)
		}
	}()

	// 接管状态变了当场推给前端：闲置自动交还之后，前端要立刻停止把按键当成接管。
	changes, unwatch := h.manager.Watch()
	defer unwatch()
	takeover := bot.Takeover()
	ping := time.NewTicker(livePingInterval)
	defer ping.Stop()

	writeJSON := func(value any) error {
		_ = conn.SetWriteDeadline(time.Now().Add(liveWriteTimeout))
		return conn.WriteJSON(value)
	}
	if writeJSON(gin.H{"type": "ready", "tab": target, "takeover": takeover}) != nil {
		return
	}
	credits := liveFramesInFlight
	for {
		// 前端手上已经压着 liveFramesInFlight 帧没画完时不取帧：浏览器拿不到回执就
		// 不出下一帧，等前端缓过来，取到的是那时最新的一帧，而不是排了几秒的旧帧。
		frames := live.Frames()
		if credits == 0 {
			frames = nil
		}
		select {
		case frame, ok := <-frames:
			if !ok {
				// 标签页崩了之类的原因要告诉前端，不然它只会一直显示最后一帧或「正在连接」。
				if err := live.Err(); err != nil {
					_ = writeJSON(gin.H{"type": "error", "message": err.Error()})
				}
				return
			}
			// 先回执：传这一帧的同时浏览器就在画下一帧。
			live.Ack(frame)
			if err := writeLiveFrame(conn, frame); err != nil {
				return
			}
			credits--
		case <-acks:
			if credits < liveFramesInFlight {
				credits++
			}
		case <-changes:
			if now := bot.Takeover(); now != takeover {
				takeover = now
				if writeJSON(gin.H{"type": "takeover", "active": now}) != nil {
					return
				}
			}
		case <-ping.C:
			if conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(liveWriteTimeout)) != nil {
				return
			}
		case <-done:
			return
		case <-c.Request.Context().Done():
			return
		}
	}
}

// writeLiveFrame 把一帧画面作为二进制消息发出去：4 字节大端的元数据长度，元数据
// JSON，然后是 JPEG 原样的字节。以前 JPEG 转成 base64 塞进 JSON，体积大三分之一，
// 前端还要整段解析 JSON、再把几百 KB 的 data URL 交给 img 解码。
func writeLiveFrame(conn *websocket.Conn, frame browserbox.Frame) error {
	meta, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	message := make([]byte, 4, 4+len(meta)+len(frame.JPEG))
	binary.BigEndian.PutUint32(message, uint32(len(meta)))
	message = append(message, meta...)
	message = append(message, frame.JPEG...)
	_ = conn.SetWriteDeadline(time.Now().Add(liveWriteTimeout))
	return conn.WriteMessage(websocket.BinaryMessage, message)
}

// handleLiveMessage 把前端的一条指令翻成 CDP 调用。
//
// 用户在画面上点一下等于人工接管：不这样的话用户正在填表，模型同时在点别的地方，
// 两边抢同一个页面。只是鼠标路过、滚轮蹭到、敲了键盘都不算，见 claimLiveInput。
func (h *BrowserBoxHandler) handleLiveMessage(c *gin.Context, bot *browserbox.Bot, live *browserbox.Live, message liveMessage) {
	if !h.claimLiveInput(c, bot, message) {
		return
	}
	ctx := c.Request.Context()
	switch message.Type {
	case "mouse":
		_ = live.Mouse(ctx, *message.Mouse)
	case "key":
		_ = live.Key(ctx, *message.Key)
	case "text":
		_ = live.Text(ctx, message.Text)
	case "navigate":
		target := strings.TrimSpace(message.URL)
		recordRequestOperation(c, h.logs, "browser_box_navigate", "你在内置浏览器里打开了网页", target, browserBoxLogMetadata(bot, map[string]any{"url": target}))
		_ = live.Navigate(ctx, target)
	case "reload":
		_ = live.Reload(ctx)
	case "back":
		_ = live.Back(ctx)
	}
}

// claimLiveInput 按输入种类决定要不要接管、这条输入还送不送给页面。
//
// 只有在画面上按下鼠标、在地址栏打开网页这两种才接管；其余输入只在已经接管时才
// 送给页面，否则直接丢掉——机器人正在用这个页面时，悬停、滚动、按键同样会搅乱它。
// 判断放在后端，不指望每个前端都自觉不发。
//
// 键盘不接管：画面点过一次之后焦点一直留在上面，闲置交还以后再按 Cmd+Tab 切窗口、
// Cmd+C 复制、Tab 挪焦点，按下的那一下修饰键都会送到这里。以前按下任何键都算动手，
// 于是交还没多久又被这些顺手的按键抢了回去。
func (h *BrowserBoxHandler) claimLiveInput(c *gin.Context, bot *browserbox.Bot, message liveMessage) bool {
	switch message.Type {
	case "mouse":
		if message.Mouse == nil {
			return false
		}
		if message.Mouse.Type == "mousePressed" {
			h.takeOverFromLive(c, bot)
			return true
		}
		if !bot.Takeover() {
			return false
		}
		// 接管期间滚动页面也算人还在；单纯的移动不算，鼠标搁在画面上抖一抖不该让接管永不过期。
		if message.Mouse.Type != "mouseMoved" {
			bot.TouchTakeover()
		}
		return true
	case "key", "text":
		if message.Type == "key" && message.Key == nil {
			return false
		}
		if !bot.Takeover() {
			return false
		}
		bot.TouchTakeover()
		return true
	case "navigate":
		target := strings.TrimSpace(message.URL)
		if target == "" || !h.manager.Settings().HostAllowed(target) {
			return false
		}
		h.takeOverFromLive(c, bot)
		return true
	case "reload", "back":
		bot.TouchTakeover()
		return true
	}
	return false
}

// takeOverFromLive 在用户第一次在画面上有意动手时打开接管，并只在这一下记一条：
// 打字每秒十几次，逐条记会把操作记录淹掉。已经接管时只刷新闲置时间。
func (h *BrowserBoxHandler) takeOverFromLive(c *gin.Context, bot *browserbox.Bot) {
	if bot.Takeover() {
		bot.TouchTakeover()
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
