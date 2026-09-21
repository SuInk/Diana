// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserctl

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// fakeConn 是测试用的连接：记下发出去的帧，并按 respond 自动回执。
type fakeConn struct {
	mu      sync.Mutex
	sent    []Frame
	closed  bool
	respond func(Frame) *Frame
	deliver func(Frame)
}

func (f *fakeConn) Send(frame Frame) error {
	f.mu.Lock()
	f.sent = append(f.sent, frame)
	respond, deliver := f.respond, f.deliver
	f.mu.Unlock()
	if respond == nil || deliver == nil {
		return nil
	}
	if reply := respond(frame); reply != nil {
		// 回执走的是真实路径：HandleFrame 解析并配对。
		go deliver(*reply)
	}
	return nil
}

func (f *fakeConn) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeConn) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakeConn) frames() []Frame {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Frame(nil), f.sent...)
}

// newTestHub 建好一个启用中的控制面和一条已握手的连接。
func newTestHub(t *testing.T, policy Policy, tabs []TabInfo) (*Hub, *Connection, *fakeConn) {
	t.Helper()
	registry := NewRegistry(context.Background(), &memoryStore{})
	if _, err := registry.SetPolicy(context.Background(), policy); err != nil {
		t.Fatalf("设置策略失败：%v", err)
	}
	hub := NewHub(registry)
	conn := &fakeConn{}
	c, welcome, err := hub.Register(conn, Hello{ProtocolVersion: ProtocolVersion, ExtensionID: "abc"}, TokenInfo{ID: "t1"})
	if err != nil {
		t.Fatalf("握手失败：%v", err)
	}
	if welcome.ConnectionID == "" {
		t.Fatal("握手回执应带连接 ID")
	}
	conn.respond = func(frame Frame) *Frame {
		return &Frame{Type: FrameResult, ID: frame.ID, Data: rawJSON(map[string]any{"ok": true})}
	}
	conn.deliver = c.HandleFrame
	if tabs != nil {
		c.HandleFrame(Frame{Type: FrameTabs, Data: rawJSON(TabsPayload{Tabs: tabs})})
	}
	return hub, c, conn
}

func readWritePolicy() Policy {
	return Policy{
		Enabled:      true,
		WriteEnabled: true,
		AllowedHosts: []string{"example.com"},
	}
}

func TestDispatchRejectsWhenDisabled(t *testing.T) {
	hub, _, _ := newTestHub(t, readWritePolicy(), nil)
	if _, err := hub.registry.SetPolicy(context.Background(), Policy{}); err != nil {
		t.Fatalf("关闭总开关失败：%v", err)
	}
	_, err := hub.Dispatch(context.Background(), Command{Op: OpTabsList})
	if ErrorCode(err) != CodeDisabled {
		t.Fatalf("总开关关闭时应报 disabled，得到 %v", err)
	}
}

func TestDispatchRejectsUnknownOp(t *testing.T) {
	hub, _, _ := newTestHub(t, readWritePolicy(), nil)
	_, err := hub.Dispatch(context.Background(), Command{Op: "page.eval", Text: "alert(1)"})
	if ErrorCode(err) != CodeUnsupportedOp {
		t.Fatalf("协议外指令应被拒，得到 %v", err)
	}
}

func TestDispatchRejectsWriteOpWhenReadOnly(t *testing.T) {
	policy := readWritePolicy()
	policy.WriteEnabled = false
	hub, _, conn := newTestHub(t, policy, []TabInfo{{ID: 7, URL: "https://example.com/a", Active: true}})
	_, err := hub.Dispatch(context.Background(), Command{Op: OpPageClick, Selector: "#go"})
	if ErrorCode(err) != CodeWriteDisabled {
		t.Fatalf("只读模式下点击应被拒，得到 %v", err)
	}
	if len(conn.frames()) != 0 {
		t.Fatal("被拒的指令不该发给扩展")
	}
}

func TestDispatchRejectsHostOutsideAllowlist(t *testing.T) {
	hub, _, conn := newTestHub(t, readWritePolicy(), []TabInfo{{ID: 3, URL: "https://bank.test/", Active: true}})
	_, err := hub.Dispatch(context.Background(), Command{Op: OpPageRead})
	if ErrorCode(err) != CodeHostDenied {
		t.Fatalf("白名单外站点应被拒，得到 %v", err)
	}
	_, err = hub.Dispatch(context.Background(), Command{Op: OpPageOpen, URL: "https://bank.test/transfer"})
	if ErrorCode(err) != CodeHostDenied {
		t.Fatalf("导航到白名单外站点应被拒，得到 %v", err)
	}
	if len(conn.frames()) != 0 {
		t.Fatal("被拒的指令不该发给扩展")
	}
}

func TestDispatchTabsListHidesUnauthorizedTabs(t *testing.T) {
	hub, _, conn := newTestHub(t, readWritePolicy(), []TabInfo{
		{ID: 1, URL: "https://example.com/a", Title: "允许", Active: true},
		{ID: 2, URL: "https://bank.test/account", Title: "不该出现"},
	})
	result, err := hub.Dispatch(context.Background(), Command{Op: OpTabsList})
	if err != nil {
		t.Fatalf("列标签页失败：%v", err)
	}
	var payload TabsPayload
	if err := json.Unmarshal(result.Data, &payload); err != nil {
		t.Fatalf("解析结果失败：%v", err)
	}
	if len(payload.Tabs) != 1 || payload.Tabs[0].ID != 1 {
		t.Fatalf("只应返回白名单内的标签页，得到 %+v", payload.Tabs)
	}
	if len(conn.frames()) != 0 {
		t.Fatal("标签页清单由控制面缓存回答，不该打扰扩展")
	}
}

func TestDispatchRejectsUnknownTab(t *testing.T) {
	hub, _, _ := newTestHub(t, readWritePolicy(), []TabInfo{{ID: 1, URL: "https://example.com/a", Active: true}})
	_, err := hub.Dispatch(context.Background(), Command{Op: OpPageRead, TabID: 42})
	if ErrorCode(err) != CodeTabUnknown {
		t.Fatalf("未知标签页应被拒，得到 %v", err)
	}
	// 白名单外的标签页也走同一条路径，不透露它是什么站点。
	_, err = hub.Dispatch(context.Background(), Command{Op: OpPageRead, TabID: 2})
	if ErrorCode(err) != CodeTabUnknown {
		t.Fatalf("白名单外标签页应报 tab_unknown，得到 %v", err)
	}
}

func TestDispatchHonoursTakeover(t *testing.T) {
	hub, c, conn := newTestHub(t, readWritePolicy(), []TabInfo{{ID: 1, URL: "https://example.com/a", Active: true}})
	c.HandleFrame(Frame{Type: FrameTakeover, Data: rawJSON(TakeoverPayload{Active: true, Reason: "我自己来"})})
	_, err := hub.Dispatch(context.Background(), Command{Op: OpPageRead})
	if ErrorCode(err) != CodeTakeover {
		t.Fatalf("接管期间应拒绝下发，得到 %v", err)
	}
	if len(conn.frames()) != 0 {
		t.Fatal("接管期间不该给扩展发指令")
	}
	if hub.Ready() {
		t.Fatal("接管期间 Ready 应为 false")
	}
	c.HandleFrame(Frame{Type: FrameTakeover, Data: rawJSON(TakeoverPayload{Active: false})})
	if _, err := hub.Dispatch(context.Background(), Command{Op: OpPageRead}); err != nil {
		t.Fatalf("释放接管后应恢复：%v", err)
	}
	if !hub.Ready() {
		t.Fatal("释放接管后 Ready 应为 true")
	}
}

func TestDispatchWithdrawsContentAfterRedirectOffAllowlist(t *testing.T) {
	hub, c, conn := newTestHub(t, readWritePolicy(), []TabInfo{{ID: 1, URL: "https://example.com/a", Active: true}})
	conn.mu.Lock()
	conn.respond = func(frame Frame) *Frame {
		// 扩展如实回报「读到的其实是跳转之后的页面」。
		return &Frame{Type: FrameResult, ID: frame.ID, Data: rawJSON(map[string]any{
			"url":  "https://bank.test/account",
			"text": "余额 100 万",
		})}
	}
	conn.deliver = c.HandleFrame
	conn.mu.Unlock()
	_, err := hub.Dispatch(context.Background(), Command{Op: OpPageRead})
	if ErrorCode(err) != CodeHostDenied {
		t.Fatalf("跳转到白名单外时应丢掉正文，得到 %v", err)
	}
}

func TestDispatchRateLimit(t *testing.T) {
	policy := readWritePolicy()
	policy.CommandsPerMinute = 2
	hub, _, _ := newTestHub(t, policy, []TabInfo{{ID: 1, URL: "https://example.com/a", Active: true}})
	for i := 0; i < 2; i++ {
		if _, err := hub.Dispatch(context.Background(), Command{Op: OpPageRead}); err != nil {
			t.Fatalf("第 %d 条指令不该被限流：%v", i+1, err)
		}
	}
	_, err := hub.Dispatch(context.Background(), Command{Op: OpPageRead})
	if ErrorCode(err) != CodeRateLimited {
		t.Fatalf("超过每分钟上限应被限流，得到 %v", err)
	}
}

func TestDispatchTimeoutDoesNotLeakPending(t *testing.T) {
	policy := readWritePolicy()
	policy.CommandTimeoutMS = 30
	hub, c, conn := newTestHub(t, policy, []TabInfo{{ID: 1, URL: "https://example.com/a", Active: true}})
	conn.mu.Lock()
	conn.respond = func(Frame) *Frame { return nil } // 扩展装死
	conn.mu.Unlock()
	_, err := hub.Dispatch(context.Background(), Command{Op: OpPageRead})
	if ErrorCode(err) != CodeTimeout {
		t.Fatalf("扩展不回执应超时，得到 %v", err)
	}
	c.mu.Lock()
	pending := len(c.pending)
	c.mu.Unlock()
	if pending != 0 {
		t.Fatalf("超时后不该留下在飞记录，得到 %d 条", pending)
	}
}

func TestDispatchSurfacesExtensionError(t *testing.T) {
	hub, c, conn := newTestHub(t, readWritePolicy(), []TabInfo{{ID: 1, URL: "https://example.com/a", Active: true}})
	conn.mu.Lock()
	conn.respond = func(frame Frame) *Frame {
		return &Frame{Type: FrameResult, ID: frame.ID, Error: "选择器没命中", Code: CodeBadRequest}
	}
	conn.deliver = c.HandleFrame
	conn.mu.Unlock()
	_, err := hub.Dispatch(context.Background(), Command{Op: OpPageClick, Selector: "#nope"})
	if ErrorCode(err) != CodeBadRequest || err.Error() != "选择器没命中" {
		t.Fatalf("扩展报错应原样带回，得到 %v（code=%s）", err, ErrorCode(err))
	}
}

func TestDispatchRequiresConnectionWhenAmbiguous(t *testing.T) {
	hub, _, _ := newTestHub(t, readWritePolicy(), []TabInfo{{ID: 1, URL: "https://example.com/a", Active: true}})
	second := &fakeConn{}
	if _, _, err := hub.Register(second, Hello{ProtocolVersion: ProtocolVersion, ExtensionID: "def"}, TokenInfo{ID: "t2"}); err != nil {
		t.Fatalf("第二条连接握手失败：%v", err)
	}
	_, err := hub.Dispatch(context.Background(), Command{Op: OpPageRead})
	if ErrorCode(err) != CodeBadRequest {
		t.Fatalf("多条连接时应要求点名，得到 %v", err)
	}
}

func TestDispatchMissingParams(t *testing.T) {
	hub, _, _ := newTestHub(t, readWritePolicy(), []TabInfo{{ID: 1, URL: "https://example.com/a", Active: true}})
	cases := []Command{
		{Op: OpPageOpen},
		{Op: OpPageClick},
		{Op: OpPageType, Selector: "#q"},
	}
	for _, cmd := range cases {
		if _, err := hub.Dispatch(context.Background(), cmd); ErrorCode(err) == "" {
			t.Errorf("%s 缺参数应被拦下，得到 %v", cmd.Op, err)
		}
	}
}

func TestRegisterRejectsProtocolMismatchAndDisabled(t *testing.T) {
	registry := NewRegistry(context.Background(), &memoryStore{})
	hub := NewHub(registry)
	if _, _, err := hub.Register(&fakeConn{}, Hello{ProtocolVersion: ProtocolVersion, ExtensionID: "a"}, TokenInfo{}); ErrorCode(err) != CodeDisabled {
		t.Fatalf("未启用时不该接受连接，得到 %v", err)
	}
	if _, err := registry.SetPolicy(context.Background(), Policy{Enabled: true}); err != nil {
		t.Fatalf("启用失败：%v", err)
	}
	if _, _, err := hub.Register(&fakeConn{}, Hello{ProtocolVersion: ProtocolVersion + 1, ExtensionID: "a"}, TokenInfo{}); ErrorCode(err) != CodeVersion {
		t.Fatalf("协议版本不一致应拒连，得到 %v", err)
	}
}

func TestConnectionCloseFailsPendingCommands(t *testing.T) {
	policy := readWritePolicy()
	policy.CommandTimeoutMS = 2_000
	hub, c, conn := newTestHub(t, policy, []TabInfo{{ID: 1, URL: "https://example.com/a", Active: true}})
	conn.mu.Lock()
	conn.respond = func(Frame) *Frame { return nil }
	conn.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		_, err := hub.Dispatch(context.Background(), Command{Op: OpPageRead})
		done <- err
	}()
	// 等指令真的挂到 pending 上，再断连接。
	deadline := time.Now().Add(time.Second)
	for {
		c.mu.Lock()
		pending := len(c.pending)
		c.mu.Unlock()
		if pending > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	c.Close()
	select {
	case err := <-done:
		if ErrorCode(err) != CodeNotConnected {
			t.Fatalf("连接断开应让在飞指令立刻失败，得到 %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("连接断开后指令仍在等超时")
	}
	if _, ok := hub.Connection(c.ID()); ok {
		t.Fatal("断开的连接应从 Hub 注销")
	}
}

func TestSetTakeoverNotifiesExtension(t *testing.T) {
	_, c, conn := newTestHub(t, readWritePolicy(), nil)
	c.SetTakeover(true, "用户从 WebUI 接管")
	frames := conn.frames()
	if len(frames) != 1 || frames[0].Type != FrameTakeover {
		t.Fatalf("应给扩展发一帧接管通知，得到 %+v", frames)
	}
	if active, reason := c.Takeover(); !active || reason != "用户从 WebUI 接管" {
		t.Fatalf("接管状态未生效：%v %q", active, reason)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	return body
}
