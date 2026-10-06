// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package desktopctl

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
)

// Adapter 是同机执行面：列窗口与截图。单元测试用假实现；macOS 可包一层 helper 子进程。
type Adapter interface {
	ListWindows(ctx context.Context) ([]WindowInfo, error)
	Screenshot(ctx context.Context, windowID string) (ScreenshotPayload, error)
}

// LocalConn 把 Adapter 适配成 Conn：收到 command 帧就本地执行并回 result。
// 不依赖真实显示器以外的网络；CI 注入 mock Adapter 即可。
type LocalConn struct {
	Adapter Adapter

	mu      sync.Mutex
	deliver func(Frame)
	closed  bool
}

// SetDeliver 由握手后的 Connection.HandleFrame 注入。
func (l *LocalConn) SetDeliver(fn func(Frame)) {
	l.mu.Lock()
	l.deliver = fn
	l.mu.Unlock()
}

func (l *LocalConn) Send(frame Frame) error {
	l.mu.Lock()
	deliver := l.deliver
	adapter := l.Adapter
	closed := l.closed
	l.mu.Unlock()
	if closed {
		return commandError(CodeNotConnected, "本地桌面连接已关闭")
	}
	if frame.Type != FrameCommand {
		return nil
	}
	go l.handleCommand(frame, adapter, deliver)
	return nil
}

func (l *LocalConn) handleCommand(frame Frame, adapter Adapter, deliver func(Frame)) {
	if deliver == nil || adapter == nil {
		return
	}
	reply := Frame{Type: FrameResult, ID: frame.ID}
	ctx := context.Background()
	switch frame.Op {
	case OpWindowScreenshot:
		var cmd Command
		_ = json.Unmarshal(frame.Params, &cmd)
		payload, err := adapter.Screenshot(ctx, strings.TrimSpace(cmd.WindowID))
		if err != nil {
			reply.Code = ErrorCode(err)
			if reply.Code == "" {
				reply.Code = CodeHelper
			}
			reply.Error = err.Error()
		} else {
			if payload.Mime == "" {
				payload.Mime = "image/png"
			}
			reply.Data = rawJSON(payload)
		}
	default:
		reply.Code = CodeUnsupportedOp
		reply.Error = "本地适配器不支持指令：" + frame.Op
	}
	deliver(reply)
}

func (l *LocalConn) Close() error {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	return nil
}

// PublishWindows 把当前窗口清单推到连接缓存（等同 FrameWindows）。
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

// MockAdapter 供单测使用，不碰显示器。
type MockAdapter struct {
	Windows []WindowInfo
	PNG     []byte
	Err     error
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
	var app, bundle, title string
	for _, w := range m.Windows {
		if w.ID == windowID {
			app, bundle, title = w.AppName, w.BundleID, w.Title
			break
		}
	}
	return ScreenshotPayload{
		WindowID: windowID,
		AppName:  app,
		BundleID: bundle,
		Title:    title,
		Mime:     "image/png",
		Data:     base64.StdEncoding.EncodeToString(png),
	}, nil
}
