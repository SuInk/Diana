// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package desktopctl

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/SuInk/diana/internal/safego"
	"strings"
	"sync"
	"time"
)

// Adapter 是同机执行面。单元测试用假实现；macOS 可包一层 helper 子进程。
type Adapter interface {
	ListWindows(ctx context.Context) ([]WindowInfo, error)
	Screenshot(ctx context.Context, windowID string) (ScreenshotPayload, error)
	Click(ctx context.Context, cmd Command) (ActionResult, error)
	TypeText(ctx context.Context, cmd Command) (ActionResult, error)
	PressKey(ctx context.Context, cmd Command) (ActionResult, error)
}

// LocalConn 把 Adapter 适配成 Conn：收到 command 帧就本地执行并回 result。
type LocalConn struct {
	Adapter Adapter

	mu       sync.Mutex
	deliver  func(Frame)
	closed   bool
	takeover bool
	active   map[string]context.CancelFunc
}

// SetDeliver 由握手后的 Connection.HandleFrame 注入。
func (l *LocalConn) SetDeliver(fn func(Frame)) {
	l.mu.Lock()
	l.deliver = fn
	l.mu.Unlock()
}

func (l *LocalConn) Send(frame Frame) error { return l.SendContext(context.Background(), frame) }
func (l *LocalConn) SendContext(ctx context.Context, frame Frame) error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return commandError(CodeNotConnected, "本地桌面连接已关闭")
	}
	if frame.Type == FrameTakeover {
		var payload TakeoverPayload
		if err := json.Unmarshal(frame.Data, &payload); err != nil {
			l.mu.Unlock()
			return err
		}
		l.takeover = payload.Active
		for _, cancel := range l.active {
			cancel()
		}
		l.mu.Unlock()
		if p, ok := l.Adapter.(*ProcessAdapter); ok {
			invalidateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			p.Invalidate(invalidateCtx)
			cancel()
		}
		return nil
	}
	if frame.Type != FrameCommand {
		l.mu.Unlock()
		return nil
	}
	if l.takeover {
		l.mu.Unlock()
		return commandError(CodeTakeover, "用户正在人工接管桌面")
	}
	ctx, cancel := context.WithCancel(ctx)
	if l.active == nil {
		l.active = map[string]context.CancelFunc{}
	}
	l.active[frame.ID] = cancel
	adapter, deliver := l.Adapter, l.deliver
	l.mu.Unlock()
	go func() {
		defer recoverLocalCommand()
		defer func() { cancel(); l.mu.Lock(); delete(l.active, frame.ID); l.mu.Unlock() }()
		l.handleCommand(ctx, frame, adapter, deliver)
	}()
	return nil
}
func (l *LocalConn) Refresh(ctx context.Context) error {
	windows, err := l.Adapter.ListWindows(ctx)
	if err != nil {
		return err
	}
	l.mu.Lock()
	deliver, closed := l.deliver, l.closed
	l.mu.Unlock()
	if closed {
		return commandError(CodeNotConnected, "本地连接已关闭")
	}
	if deliver != nil {
		deliver(Frame{Type: FrameWindows, Data: rawJSON(WindowsPayload{Windows: windows})})
	}
	return nil
}

func (l *LocalConn) handleCommand(ctx context.Context, frame Frame, adapter Adapter, deliver func(Frame)) {
	defer func() {
		if recovered := recover(); recovered != nil {
			safego.Report("desktopctl.adapter", recovered)
			if deliver != nil {
				deliver(Frame{Type: FrameResult, ID: frame.ID, Code: CodeHelper, Error: "本地桌面适配器执行失败"})
			}
		}
	}()
	if deliver == nil || adapter == nil {
		return
	}
	reply := Frame{Type: FrameResult, ID: frame.ID}
	var cmd Command
	_ = json.Unmarshal(frame.Params, &cmd)

	fail := func(err error) {
		reply.Code = ErrorCode(err)
		if reply.Code == "" {
			reply.Code = CodeHelper
		}
		reply.Error = err.Error()
		deliver(reply)
	}

	switch frame.Op {
	case OpWindowScreenshot:
		var payload ScreenshotPayload
		var err error
		if process, ok := adapter.(*ProcessAdapter); ok {
			payload, err = process.screenshot(ctx, strings.TrimSpace(cmd.WindowID), cmd.ExpectedBundleID)
		} else {
			payload, err = adapter.Screenshot(ctx, strings.TrimSpace(cmd.WindowID))
		}
		if err != nil {
			fail(err)
			return
		}
		if payload.Mime == "" {
			payload.Mime = "image/png"
		}
		reply.Data = rawJSON(payload)
	case OpWindowElements:
		extended, ok := adapter.(interface {
			Elements(context.Context, Command) (ElementsPayload, error)
		})
		if !ok {
			fail(commandError(CodeUnsupportedOp, "执行器不支持元素读取"))
			return
		}
		payload, err := extended.Elements(ctx, cmd)
		if err != nil {
			fail(err)
			return
		}
		reply.Data = rawJSON(payload)
	case OpWindowScroll:
		extended, ok := adapter.(interface {
			Scroll(context.Context, Command) (ActionResult, error)
		})
		if !ok {
			fail(commandError(CodeUnsupportedOp, "执行器不支持滚动"))
			return
		}
		payload, err := extended.Scroll(ctx, cmd)
		if err != nil {
			fail(err)
			return
		}
		reply.Data = rawJSON(payload)
	case OpWindowClick:
		payload, err := adapter.Click(ctx, cmd)
		if err != nil {
			fail(err)
			return
		}
		reply.Data = rawJSON(payload)
	case OpWindowType:
		payload, err := adapter.TypeText(ctx, cmd)
		if err != nil {
			fail(err)
			return
		}
		reply.Data = rawJSON(payload)
	case OpWindowKey:
		payload, err := adapter.PressKey(ctx, cmd)
		if err != nil {
			fail(err)
			return
		}
		reply.Data = rawJSON(payload)
	default:
		reply.Code = CodeUnsupportedOp
		reply.Error = "本地适配器不支持指令：" + frame.Op
	}
	deliver(reply)
}

func (l *LocalConn) Close() error {
	l.mu.Lock()
	l.closed = true
	for _, cancel := range l.active {
		cancel()
	}
	l.mu.Unlock()
	if p, ok := l.Adapter.(*ProcessAdapter); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		p.Invalidate(ctx)
	}
	return nil
}

// PublishWindows 把当前窗口清单推到连接缓存。
func PublishWindows(c *Connection, windows []WindowInfo) {
	if c == nil {
		return
	}
	c.HandleFrame(Frame{Type: FrameWindows, Data: rawJSON(WindowsPayload{Windows: windows})})
}

// AttachLocal 在策略已启用时登记一条本地连接，并刷新窗口清单。
func (h *Hub) AttachLocal(ctx context.Context, adapter Adapter, hello Hello, token TokenInfo) (*Connection, *LocalConn, error) {
	if adapter == nil {
		return nil, nil, commandError(CodeBadRequest, "本地适配器不能为空")
	}
	if hello.HelperID == "" {
		hello.HelperID = "local"
	}
	if hello.ProtocolVersion == 0 {
		hello.ProtocolVersion = ProtocolVersion
	}
	local := &LocalConn{Adapter: adapter}
	c, _, err := h.Register(local, hello, token)
	if err != nil {
		return nil, nil, err
	}
	local.SetDeliver(c.HandleFrame)
	windows, err := adapter.ListWindows(ctx)
	if err != nil {
		c.Close()
		return nil, nil, err
	}
	PublishWindows(c, windows)
	return c, local, nil
}

// MockAdapter 供单测使用，不碰显示器与辅助功能权限。
type MockAdapter struct {
	Windows []WindowInfo
	PNG     []byte
	Err     error
	// WriteErr 只作用于点击/输入/按键，便于测 permission_denied。
	WriteErr error
	Clicks   []Command
	Types    []Command
	Keys     []Command
}

func (m *MockAdapter) ListWindows(context.Context) ([]WindowInfo, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	return append([]WindowInfo(nil), m.Windows...), nil
}

func (m *MockAdapter) Screenshot(_ context.Context, windowID string) (ScreenshotPayload, error) {
	if m.Err != nil {
		return ScreenshotPayload{}, m.Err
	}
	png := m.PNG
	if len(png) == 0 {
		png = []byte("mock-png")
	}
	app, bundle, title := m.lookup(windowID)
	return ScreenshotPayload{
		WindowID: windowID,
		AppName:  app,
		BundleID: bundle,
		Title:    title,
		Mime:     "image/png",
		Data:     base64.StdEncoding.EncodeToString(png),
	}, nil
}

func (m *MockAdapter) Click(_ context.Context, cmd Command) (ActionResult, error) {
	if m.WriteErr != nil {
		return ActionResult{}, m.WriteErr
	}
	if m.Err != nil {
		return ActionResult{}, m.Err
	}
	m.Clicks = append(m.Clicks, cmd)
	app, bundle, _ := m.lookup(cmd.WindowID)
	return ActionResult{WindowID: cmd.WindowID, AppName: app, BundleID: bundle, Op: OpWindowClick, OK: true}, nil
}

func (m *MockAdapter) TypeText(_ context.Context, cmd Command) (ActionResult, error) {
	if m.WriteErr != nil {
		return ActionResult{}, m.WriteErr
	}
	if m.Err != nil {
		return ActionResult{}, m.Err
	}
	m.Types = append(m.Types, cmd)
	app, bundle, _ := m.lookup(cmd.WindowID)
	return ActionResult{WindowID: cmd.WindowID, AppName: app, BundleID: bundle, Op: OpWindowType, OK: true}, nil
}

func (m *MockAdapter) PressKey(_ context.Context, cmd Command) (ActionResult, error) {
	if m.WriteErr != nil {
		return ActionResult{}, m.WriteErr
	}
	if m.Err != nil {
		return ActionResult{}, m.Err
	}
	m.Keys = append(m.Keys, cmd)
	app, bundle, _ := m.lookup(cmd.WindowID)
	return ActionResult{WindowID: cmd.WindowID, AppName: app, BundleID: bundle, Op: OpWindowKey, OK: true}, nil
}

func (m *MockAdapter) lookup(windowID string) (app, bundle, title string) {
	for _, w := range m.Windows {
		if w.ID == windowID {
			return w.AppName, w.BundleID, w.Title
		}
	}
	return "", "", ""
}

// PermissionDenied 构造可解释的权限错误（macOS Accessibility / Screen Recording）。
func PermissionDenied(what string) error {
	what = strings.TrimSpace(what)
	if what == "" {
		what = "系统权限"
	}
	return commandError(CodePermissionDenied,
		"%s未授权。请在系统设置 → 隐私与安全性中允许本执行器，然后完全退出并重启执行器", what)
}

func recoverLocalCommand() { safego.Report("desktopctl.command", recover()) }
