// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// qqcall-agent 跑在 SnowLuma 容器里，替 Diana 操作 Linux QQ 的语音通话：看到来电
// 窗口就报给 Diana，按 Diana 的指令点接听、挂断，接通后把两条虚拟声卡上的声音
// 经 WebSocket 收发。它自己不判断该不该接，也不碰 QQ 进程（重启 QQ 会掉登录）。
// 协议见 model/qqcall，部署见 docs/qq-voice-call.md。
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"github.com/SuInk/diana/model/qqcall"
)

const version = "0.1.0"

type config struct {
	Listen      string
	Token       string
	WindowTitle string
	LogDir      string
	// 来电窗口里的坐标，都相对窗口左上角。QQ 改版挪了按钮时改环境变量就行。
	AnswerPos point
	RejectPos point
	HangupPos point
	NameArea  rect
}

var errNoToken = errors.New("QQCALL_TOKEN 没有设置")

type point struct{ X, Y int }

type rect struct{ X, Y, W, H int }

func loadConfig() (config, error) {
	cfg := config{
		Listen:      envOr("QQCALL_LISTEN", ":6190"),
		Token:       strings.TrimSpace(os.Getenv("QQCALL_TOKEN")),
		WindowTitle: envOr("QQCALL_WINDOW_TITLE", "语音通话"),
		LogDir:      envOr("QQCALL_LOG_DIR", "/app/data/logs"),
	}
	if cfg.Token == "" {
		return cfg, errNoToken
	}
	var err error
	if cfg.AnswerPos, err = parsePoint(envOr("QQCALL_ANSWER_POS", "112,508")); err != nil {
		return cfg, err
	}
	if cfg.RejectPos, err = parsePoint(envOr("QQCALL_REJECT_POS", "244,508")); err != nil {
		return cfg, err
	}
	if cfg.HangupPos, err = parsePoint(envOr("QQCALL_HANGUP_POS", "287,508")); err != nil {
		return cfg, err
	}
	if cfg.NameArea, err = parseRect(envOr("QQCALL_NAME_AREA", "20,255,320,75")); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("qqcall-agent: ")
	cfg, err := loadConfig()
	if errors.Is(err, errNoToken) {
		// 镜像里默认装着 agent，但没配令牌就是没打算用：正常退出，supervisor 不再拉起。
		log.Print("没有设置 QQCALL_TOKEN，不启动")
		return
	}
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := setupAudio(ctx); err != nil {
		log.Fatalf("虚拟声卡没建好: %v", err)
	}
	a := newAgent(cfg)
	go func() {
		defer recoverGoroutinePanic("watch")
		a.watch(ctx)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", a.serveWS)
	srv := &http.Server{Addr: cfg.Listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		defer recoverGoroutinePanic("shutdown")
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("v%s listening on %s", version, cfg.Listen)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

var upgrader = websocket.Upgrader{
	// Diana 不是浏览器，不带 Origin；靠 token 鉴权。
	CheckOrigin: func(*http.Request) bool { return true },
}

func (a *agent) serveWS(w http.ResponseWriter, r *http.Request) {
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if subtle.ConstantTimeCompare([]byte(got), []byte(a.cfg.Token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &client{conn: conn}
	a.attach(c)
	defer a.detach(c)
	go func() {
		defer recoverGoroutinePanic("keepalive")
		c.keepAlive(r.Context())
	}()
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(60 * time.Second)) })
	for {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		switch kind {
		case websocket.BinaryMessage:
			a.playback(data)
		case websocket.TextMessage:
			var msg qqcall.Message
			if json.Unmarshal(data, &msg) != nil {
				c.send(qqcall.Message{Type: qqcall.TypeError, Error: "控制消息不是合法的 JSON"})
				continue
			}
			if err := a.command(msg); err != nil {
				c.send(qqcall.Message{Type: qqcall.TypeError, Call: msg.Call, Error: err.Error()})
			}
		}
	}
}

// client 串行写 WebSocket：状态机、音频和保活都会写。
type client struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (c *client) send(msg qqcall.Message) {
	data, _ := json.Marshal(msg)
	c.write(websocket.TextMessage, data)
}

func (c *client) write(kind int, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = c.conn.WriteMessage(kind, data)
}

func (c *client) keepAlive(ctx context.Context) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.mu.Lock()
			err := c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			c.mu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func parsePoint(s string) (point, error) {
	n, err := parseInts(s, 2)
	if err != nil {
		return point{}, err
	}
	return point{n[0], n[1]}, nil
}

func parseRect(s string) (rect, error) {
	n, err := parseInts(s, 4)
	if err != nil {
		return rect{}, err
	}
	return rect{n[0], n[1], n[2], n[3]}, nil
}

func parseInts(s string, want int) ([]int, error) {
	parts := strings.Split(s, ",")
	if len(parts) != want {
		return nil, errors.New("坐标格式不对: " + s)
	}
	out := make([]int, want)
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, errors.New("坐标格式不对: " + s)
		}
		out[i] = n
	}
	return out, nil
}
