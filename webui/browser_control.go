// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/browserctl"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

const (
	// browserControlHandshakeTimeout 是等 hello 帧的时间。连上来不说话的客户端
	// 不该一直占着一条连接。
	browserControlHandshakeTimeout = 10 * time.Second
	// browserControlReadLimit 是单帧上限。截图是这里最大的一类载荷，
	// 8 MiB 足够一张整屏 PNG，也挡住把控制面当文件上传口用。
	browserControlReadLimit = 8 << 20
	// browserControlWriteTimeout 是单次写超时。对端卡住时写会一直阻塞，
	// 没有它的话一条坏连接能把下发指令的协程挂死。
	browserControlWriteTimeout = 15 * time.Second
	// browserControlPongWait 是两次心跳之间允许的最长沉默。
	browserControlPongWait = 3 * browserctl.DefaultHeartbeatSeconds * time.Second
)

// BrowserControlHandler 提供浏览器控制扩展的接入端点与管理接口。
type BrowserControlHandler struct {
	registry *browserctl.Registry
	hub      *browserctl.Hub
	logs     AppLogWriter
	upgrader websocket.Upgrader
}

// NewBrowserControlHandler 创建浏览器控制接口处理器。
func NewBrowserControlHandler(registry *browserctl.Registry, hub *browserctl.Hub) *BrowserControlHandler {
	h := &BrowserControlHandler{registry: registry, hub: hub}
	h.upgrader = websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		// 来源白名单就是这条连接的第一道门。浏览器扩展的 Service Worker 会带上
		// chrome-extension://<id> 作为 Origin，白名单里没有它就连不上；
		// 白名单为空时谁都连不上，这是有意的失败关闭。
		CheckOrigin: func(r *http.Request) bool {
			return h.registry.Policy().OriginAllowed(r.Header.Get("Origin"))
		},
	}
	return h
}

// SetLogStore 注入操作日志写入器。
func (h *BrowserControlHandler) SetLogStore(store AppLogWriter) {
	h.logs = store
}

// Register 注册管理接口与扩展接入端点。
//
// 管理接口在 /api 下，走 WebUI 会话鉴权；扩展接入端点在 /browser-control 下，
// 有意不在 /api 里：它的鉴权是令牌加来源白名单，跟浏览器登录态无关，
// 也不该因为管理员登出就掉线。
func (h *BrowserControlHandler) Register(router gin.IRouter) {
	router.GET("/api/browser-control/status", h.status)
	router.PUT("/api/browser-control/policy", h.setPolicy)
	router.GET("/api/browser-control/tokens", h.listTokens)
	router.POST("/api/browser-control/tokens", h.createToken)
	router.DELETE("/api/browser-control/tokens/:id", h.revokeToken)
	router.POST("/api/browser-control/connections/:id/takeover", h.setTakeover)
	router.DELETE("/api/browser-control/connections/:id", h.disconnect)

	router.GET("/browser-control/v1/socket", h.socket)
}

func (h *BrowserControlHandler) status(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	policy := h.registry.Policy()
	c.JSON(http.StatusOK, gin.H{
		"policy":      policy,
		"tokens":      h.registry.ListTokens(),
		"connections": h.hub.Connections(),
		"ready":       h.hub.Ready(),
		"endpoint":    "/browser-control/v1/socket",
		"protocol":    browserctl.ProtocolVersion,
	})
}

func (h *BrowserControlHandler) setPolicy(c *gin.Context) {
	var payload browserctl.Policy
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, errors.New("浏览器控制策略格式错误"))
		return
	}
	saved, err := h.registry.SetPolicy(c.Request.Context(), payload)
	if err != nil {
		logAndWriteError(c, h.logs, http.StatusInternalServerError, "browser_control_policy", err, "", nil)
		return
	}
	// 关掉总开关就得当场断开，而不是等下一条指令被拒：用户按下开关的意思是
	// 「现在起别碰我的浏览器」。
	if !saved.Enabled {
		h.hub.CloseAll()
	}
	recordRequestOperation(c, h.logs, "browser_control_policy", "浏览器控制策略已更新", "", map[string]any{
		"enabled":       saved.Enabled,
		"write_enabled": saved.WriteEnabled,
		"allowed_hosts": saved.AllowedHosts,
	})
	c.JSON(http.StatusOK, gin.H{"policy": saved})
}

func (h *BrowserControlHandler) listTokens(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"tokens": h.registry.ListTokens()})
}

func (h *BrowserControlHandler) createToken(c *gin.Context) {
	var payload struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, errors.New("请求格式错误"))
		return
	}
	info, plaintext, err := h.registry.CreateToken(c.Request.Context(), payload.Name)
	if err != nil {
		status := http.StatusBadRequest
		if !errors.Is(err, browserctl.ErrTokenNameInvalid) {
			status = http.StatusInternalServerError
		}
		logAndWriteError(c, h.logs, status, "browser_control_token_create", err, payload.Name, nil)
		return
	}
	recordRequestOperation(c, h.logs, "browser_control_token_create", "浏览器控制令牌已创建", info.Name, map[string]any{"token_id": info.ID})
	c.Header("Cache-Control", "no-store")
	// 明文只在这一次返回。日志里记的是 ID 和名称，不记明文。
	c.JSON(http.StatusOK, gin.H{"token": info, "plaintext": plaintext})
}

func (h *BrowserControlHandler) revokeToken(c *gin.Context) {
	info, err := h.registry.RevokeToken(c.Request.Context(), c.Param("id"))
	if err != nil {
		status := http.StatusNotFound
		if !errors.Is(err, browserctl.ErrTokenNotFound) {
			status = http.StatusInternalServerError
		}
		logAndWriteError(c, h.logs, status, "browser_control_token_revoke", err, c.Param("id"), nil)
		return
	}
	// 令牌吊销了，拿着它连上来的那条连接也得走。
	for _, conn := range h.hub.Connections() {
		if conn.TokenID != info.ID {
			continue
		}
		if live, ok := h.hub.Connection(conn.ID); ok {
			live.Close()
		}
	}
	recordRequestOperation(c, h.logs, "browser_control_token_revoke", "浏览器控制令牌已吊销", info.Name, map[string]any{"token_id": info.ID})
	c.JSON(http.StatusOK, gin.H{"token": info})
}

func (h *BrowserControlHandler) setTakeover(c *gin.Context) {
	var payload struct {
		Active bool   `json:"active"`
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, errors.New("请求格式错误"))
		return
	}
	conn, ok := h.hub.Connection(c.Param("id"))
	if !ok {
		writeError(c, http.StatusNotFound, errors.New("连接不存在"))
		return
	}
	conn.SetTakeover(payload.Active, strings.TrimSpace(payload.Reason))
	recordRequestOperation(c, h.logs, "browser_control_takeover", "浏览器控制接管状态已切换", conn.ID(), map[string]any{"active": payload.Active})
	c.JSON(http.StatusOK, gin.H{"ok": true, "active": payload.Active})
}

func (h *BrowserControlHandler) disconnect(c *gin.Context) {
	conn, ok := h.hub.Connection(c.Param("id"))
	if !ok {
		writeError(c, http.StatusNotFound, errors.New("连接不存在"))
		return
	}
	conn.Close()
	recordRequestOperation(c, h.logs, "browser_control_disconnect", "浏览器控制连接已断开", c.Param("id"), nil)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// socket 接受浏览器扩展的反向连接。
//
// 顺序是先升级、再读 hello 里的令牌：令牌放在帧里而不是 URL 上，
// 这样它不会出现在访问日志、反代日志和浏览器历史里。
func (h *BrowserControlHandler) socket(c *gin.Context) {
	policy := h.registry.Policy()
	if !policy.Enabled {
		writeError(c, http.StatusServiceUnavailable, errors.New("浏览器控制未启用"))
		return
	}
	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		// Upgrade 失败时它自己已经写过响应了，这里只记一笔。
		recordError(c.Request.Context(), h.logs, "browser_control_upgrade", err, "", nil)
		return
	}
	conn.SetReadLimit(browserControlReadLimit)
	transport := &browserControlConn{conn: conn}

	hello, err := h.readHello(conn)
	if err != nil {
		transport.reject(browserctl.CodeUnauthorized, err.Error())
		return
	}
	token, err := h.registry.Authenticate(c.Request.Context(), hello.Token, hello.ExtensionID)
	if err != nil {
		// 失败原因对用户有用（令牌错了还是扩展换了），但不记扩展报上来的令牌。
		recordError(c.Request.Context(), h.logs, "browser_control_auth", err, hello.ExtensionID, nil)
		transport.reject(browserctl.CodeUnauthorized, err.Error())
		return
	}
	registered, welcome, err := h.hub.Register(transport, hello, token)
	if err != nil {
		code := browserctl.ErrorCode(err)
		if code == "" {
			code = browserctl.CodeBadRequest
		}
		transport.reject(code, err.Error())
		return
	}
	defer registered.Close()
	if err := transport.Send(browserctl.Frame{Type: browserctl.FrameWelcome, Data: browserControlJSON(welcome)}); err != nil {
		return
	}
	recordRequestOperation(c, h.logs, "browser_control_connect", "浏览器控制扩展已连接", registered.ID(), map[string]any{
		"extension_id": hello.ExtensionID,
		"browser":      hello.Browser,
		"token_id":     token.ID,
	})

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		defer recoverGoroutinePanic("browser_control.heartbeat")
		h.heartbeat(transport, stop)
	}()

	_ = conn.SetReadDeadline(time.Now().Add(browserControlPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(browserControlPongWait))
	})
	for {
		messageType, payload, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if messageType != websocket.TextMessage {
			continue
		}
		_ = conn.SetReadDeadline(time.Now().Add(browserControlPongWait))
		frame, err := browserctl.DecodeFrame(payload)
		if err != nil {
			// 读不懂的帧丢掉就好：断连会让扩展重连风暴，而一帧坏了不代表连接坏了。
			continue
		}
		registered.HandleFrame(frame)
	}
}

// readHello 在握手超时内读第一帧，并要求它就是 hello。
func (h *BrowserControlHandler) readHello(conn *websocket.Conn) (browserctl.Hello, error) {
	_ = conn.SetReadDeadline(time.Now().Add(browserControlHandshakeTimeout))
	messageType, payload, err := conn.ReadMessage()
	if err != nil {
		return browserctl.Hello{}, errors.New("握手超时或连接中断")
	}
	if messageType != websocket.TextMessage {
		return browserctl.Hello{}, errors.New("握手帧必须是文本帧")
	}
	frame, err := browserctl.DecodeFrame(payload)
	if err != nil || frame.Type != browserctl.FrameHello {
		return browserctl.Hello{}, errors.New("第一帧必须是 hello")
	}
	var hello browserctl.Hello
	if len(frame.Data) == 0 || json.Unmarshal(frame.Data, &hello) != nil {
		return browserctl.Hello{}, errors.New("hello 帧格式错误")
	}
	if strings.TrimSpace(hello.Token) == "" {
		return browserctl.Hello{}, errors.New("hello 帧缺少令牌")
	}
	return hello, nil
}

// heartbeat 定期发协议心跳。发现半开连接靠它：对端掉线但 TCP 没断时，
// 读侧的 deadline 会到期，连接被回收。
func (h *BrowserControlHandler) heartbeat(transport *browserControlConn, stop <-chan struct{}) {
	ticker := time.NewTicker(browserctl.DefaultHeartbeatSeconds * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if err := transport.Send(browserctl.Frame{Type: browserctl.FramePing}); err != nil {
				return
			}
		}
	}
}

// browserControlJSON 把握手回执编码成帧载荷。回执的字段都是控制面自己给的，
// 不会编码失败；真出错就发一个空载荷，扩展会因为拿不到策略而重连。
func browserControlJSON(value any) json.RawMessage {
	body, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return body
}

// browserControlConn 把 WebSocket 连接适配成 browserctl.Conn。
// gorilla 的连接不允许并发写，所以写侧统一加锁。
type browserControlConn struct {
	conn *websocket.Conn

	mu     sync.Mutex
	closed bool
}

func (t *browserControlConn) Send(frame browserctl.Frame) error {
	body, err := browserctl.EncodeFrame(frame)
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return errors.New("连接已关闭")
	}
	if err := t.conn.SetWriteDeadline(time.Now().Add(browserControlWriteTimeout)); err != nil {
		return err
	}
	return t.conn.WriteMessage(websocket.TextMessage, body)
}

func (t *browserControlConn) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()
	return t.conn.Close()
}

// reject 在握手阶段把原因告诉对端再关连接。扩展据此在界面上显示
// 「令牌无效」还是「站点未授权」，而不是无限重连。
func (t *browserControlConn) reject(code, message string) {
	_ = t.Send(browserctl.Frame{Type: browserctl.FrameError, Code: code, Error: message})
	_ = t.Close()
}
