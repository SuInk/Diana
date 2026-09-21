// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/browserctl"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

type memoryBrowserControlStore struct {
	mu    sync.Mutex
	doc   browserctl.Document
	saved bool
}

func (m *memoryBrowserControlStore) LoadBrowserControl(context.Context) (browserctl.Document, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.doc, m.saved, nil
}

func (m *memoryBrowserControlStore) SaveBrowserControl(_ context.Context, doc browserctl.Document) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.doc = doc
	m.saved = true
	return nil
}

func newBrowserControlTestServer(t *testing.T) (*httptest.Server, *browserctl.Registry, *browserctl.Hub) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	registry := browserctl.NewRegistry(context.Background(), &memoryBrowserControlStore{})
	hub := browserctl.NewHub(registry)
	handler := NewBrowserControlHandler(registry, hub)
	router := gin.New()
	handler.Register(router)
	server := httptest.NewServer(router)
	t.Cleanup(func() {
		hub.CloseAll()
		server.Close()
	})
	return server, registry, hub
}

// enableWithOrigin 打开总开关并放行一个扩展来源，返回一把可用令牌明文。
func enableWithOrigin(t *testing.T, registry *browserctl.Registry, origin string) string {
	t.Helper()
	if _, err := registry.SetPolicy(context.Background(), browserctl.Policy{
		Enabled:        true,
		AllowedOrigins: []string{origin},
		AllowedHosts:   []string{"example.com"},
	}); err != nil {
		t.Fatalf("设置策略失败：%v", err)
	}
	_, plaintext, err := registry.CreateToken(context.Background(), "测试浏览器")
	if err != nil {
		t.Fatalf("签发令牌失败：%v", err)
	}
	return plaintext
}

func socketURL(server *httptest.Server) string {
	return "ws" + strings.TrimPrefix(server.URL, "http") + "/browser-control/v1/socket"
}

func dialSocket(t *testing.T, server *httptest.Server, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	header := http.Header{}
	if origin != "" {
		header.Set("Origin", origin)
	}
	return websocket.DefaultDialer.Dial(socketURL(server), header)
}

func sendFrame(t *testing.T, conn *websocket.Conn, frame browserctl.Frame) {
	t.Helper()
	body, err := browserctl.EncodeFrame(frame)
	if err != nil {
		t.Fatalf("编码帧失败：%v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, body); err != nil {
		t.Fatalf("发送帧失败：%v", err)
	}
}

func readFrame(t *testing.T, conn *websocket.Conn) browserctl.Frame {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("读取帧失败：%v", err)
	}
	frame, err := browserctl.DecodeFrame(payload)
	if err != nil {
		t.Fatalf("解析帧失败：%v", err)
	}
	return frame
}

func helloFrame(token, extensionID string) browserctl.Frame {
	return browserctl.Frame{Type: browserctl.FrameHello, Data: mustMarshal(browserctl.Hello{
		ProtocolVersion: browserctl.ProtocolVersion,
		Token:           token,
		ExtensionID:     extensionID,
	})}
}

func mustMarshal(value any) json.RawMessage {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return body
}

func TestBrowserControlSocketRejectsWhenDisabled(t *testing.T) {
	server, _, _ := newBrowserControlTestServer(t)
	_, resp, err := dialSocket(t, server, "chrome-extension://abc")
	if err == nil {
		t.Fatal("未启用时不该建立连接")
	}
	if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未启用时应返回 503，得到 %v", resp)
	}
}

func TestBrowserControlSocketRejectsUnlistedOrigin(t *testing.T) {
	server, registry, _ := newBrowserControlTestServer(t)
	enableWithOrigin(t, registry, "chrome-extension://allowed")

	_, resp, err := dialSocket(t, server, "chrome-extension://evil")
	if err == nil {
		t.Fatal("白名单外来源不该建立连接")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("白名单外来源应返回 403，得到 %v", resp)
	}
	// 不带 Origin 的客户端同样连不上：这一档没有「本地就放过」的默认。
	if _, _, err := dialSocket(t, server, ""); err == nil {
		t.Fatal("缺 Origin 不该建立连接")
	}
}

func TestBrowserControlSocketRejectsBadToken(t *testing.T) {
	server, registry, hub := newBrowserControlTestServer(t)
	origin := "chrome-extension://allowed"
	enableWithOrigin(t, registry, origin)

	conn, _, err := dialSocket(t, server, origin)
	if err != nil {
		t.Fatalf("握手前的连接应该建立：%v", err)
	}
	defer conn.Close()
	sendFrame(t, conn, helloFrame("dianabx_wrong", "abc"))
	frame := readFrame(t, conn)
	if frame.Type != browserctl.FrameError || frame.Code != browserctl.CodeUnauthorized {
		t.Fatalf("错误令牌应收到 unauthorized 错误帧，得到 %+v", frame)
	}
	if len(hub.Connections()) != 0 {
		t.Fatal("鉴权失败的连接不该登记到 Hub")
	}
}

func TestBrowserControlSocketHandshakeAndDispatch(t *testing.T) {
	server, registry, hub := newBrowserControlTestServer(t)
	origin := "chrome-extension://allowed"
	token := enableWithOrigin(t, registry, origin)

	conn, _, err := dialSocket(t, server, origin)
	if err != nil {
		t.Fatalf("连接失败：%v", err)
	}
	defer conn.Close()
	sendFrame(t, conn, helloFrame(token, "abcdefabcdefabcd"))

	frame := readFrame(t, conn)
	if frame.Type != browserctl.FrameWelcome {
		t.Fatalf("应收到 welcome，得到 %+v", frame)
	}
	var welcome browserctl.Welcome
	if err := json.Unmarshal(frame.Data, &welcome); err != nil {
		t.Fatalf("解析 welcome 失败：%v", err)
	}
	if welcome.ProtocolVersion != browserctl.ProtocolVersion || welcome.ConnectionID == "" {
		t.Fatalf("welcome 内容不完整：%+v", welcome)
	}
	if len(welcome.Policy.AllowedHosts) != 1 || welcome.Policy.AllowedHosts[0] != "example.com" {
		t.Fatalf("welcome 应回带站点白名单，得到 %+v", welcome.Policy)
	}
	if welcome.Policy.WriteEnabled {
		t.Fatal("默认应是只读")
	}

	// 上报标签页，然后从工具侧下发一条读取指令，扩展这边回执。
	sendFrame(t, conn, browserctl.Frame{Type: browserctl.FrameTabs, Data: mustMarshal(browserctl.TabsPayload{
		Tabs: []browserctl.TabInfo{{ID: 11, URL: "https://example.com/page", Title: "页面", Active: true}},
	})})

	// 上报是异步的：等控制面真的收下这条标签页，再下发指令。否则指令会
	// 因为「没有已授权站点的标签页」被拦下，测试也就成了看运气。
	waitForAllowedTabs(t, hub, 1)

	done := make(chan error, 1)
	go func() {
		_, err := hub.Dispatch(context.Background(), browserctl.Command{Op: browserctl.OpPageRead})
		done <- err
	}()

	command := readFrame(t, conn)
	// 心跳或其他帧可能先到，这里只关心指令帧。
	for command.Type != browserctl.FrameCommand {
		command = readFrame(t, conn)
	}
	if command.Op != browserctl.OpPageRead || command.ID == "" {
		t.Fatalf("指令帧内容不对：%+v", command)
	}
	sendFrame(t, conn, browserctl.Frame{Type: browserctl.FrameResult, ID: command.ID, Data: mustMarshal(map[string]any{
		"url":  "https://example.com/page",
		"text": "正文",
	})})
	if err := <-done; err != nil {
		t.Fatalf("下发指令失败：%v", err)
	}
}

// waitForAllowedTabs 等控制面缓存里出现指定数量的可操作标签页。
func waitForAllowedTabs(t *testing.T, hub *browserctl.Hub, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conns := hub.Connections()
		if len(conns) == 1 && conns[0].AllowedTabs == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等不到 %d 个可操作标签页，当前连接状态 %+v", want, hub.Connections())
}

func TestBrowserControlDisablingPolicyClosesConnections(t *testing.T) {
	server, registry, hub := newBrowserControlTestServer(t)
	origin := "chrome-extension://allowed"
	token := enableWithOrigin(t, registry, origin)

	conn, _, err := dialSocket(t, server, origin)
	if err != nil {
		t.Fatalf("连接失败：%v", err)
	}
	defer conn.Close()
	sendFrame(t, conn, helloFrame(token, "abcdefabcdefabcd"))
	if frame := readFrame(t, conn); frame.Type != browserctl.FrameWelcome {
		t.Fatalf("应收到 welcome，得到 %+v", frame)
	}

	handler := NewBrowserControlHandler(registry, hub)
	router := gin.New()
	handler.Register(router)
	body := strings.NewReader(`{"enabled":false}`)
	request := httptest.NewRequest(http.MethodPut, "/api/browser-control/policy", body)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("关闭总开关应成功，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if len(hub.Connections()) != 0 {
		t.Fatal("关掉总开关应当场断开所有连接")
	}
}

func TestBrowserControlTokenAPIHidesPlaintextAfterCreation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	registry := browserctl.NewRegistry(context.Background(), &memoryBrowserControlStore{})
	hub := browserctl.NewHub(registry)
	handler := NewBrowserControlHandler(registry, hub)
	router := gin.New()
	handler.Register(router)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/browser-control/tokens", strings.NewReader(`{"name":"我的 Chrome"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("签发令牌应成功，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	var created struct {
		Token     browserctl.TokenInfo `json:"token"`
		Plaintext string               `json:"plaintext"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if created.Plaintext == "" {
		t.Fatal("创建时应返回一次明文")
	}

	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/browser-control/tokens", nil))
	if strings.Contains(recorder.Body.String(), created.Plaintext) {
		t.Fatal("列表接口不能再返回明文")
	}

	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/api/browser-control/tokens/"+created.Token.ID, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("吊销应成功，得到 %d", recorder.Code)
	}
	if _, err := registry.Authenticate(context.Background(), created.Plaintext, "abc"); err == nil {
		t.Fatal("吊销后令牌不该还能用")
	}
}

func TestBrowserControlStatusReportsEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	registry := browserctl.NewRegistry(context.Background(), &memoryBrowserControlStore{})
	handler := NewBrowserControlHandler(registry, browserctl.NewHub(registry))
	router := gin.New()
	handler.Register(router)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/browser-control/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态接口应成功，得到 %d", recorder.Code)
	}
	var payload struct {
		Endpoint string `json:"endpoint"`
		Protocol int    `json:"protocol"`
		Ready    bool   `json:"ready"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析状态失败：%v", err)
	}
	if payload.Endpoint != "/browser-control/v1/socket" || payload.Protocol != browserctl.ProtocolVersion {
		t.Fatalf("状态应报出接入端点与协议版本，得到 %+v", payload)
	}
	if payload.Ready {
		t.Fatal("没有连接时不该报就绪")
	}
}
