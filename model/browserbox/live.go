// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserbox

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// 实时画面走 CDP 的 screencast：浏览器把每一帧渲染结果编成 JPEG 推过来，
// 不需要虚拟显示器，也不需要 VNC，无头模式照样有画面。输入反过来走
// Input.dispatch*，也就是说用户在 WebUI 里点的每一下，落到页面上和真人点
// 是同一条路径——不是模拟脚本，是浏览器自己的输入管线。
const (
	// liveFrameQuality 是 JPEG 质量。60 在文字清晰和带宽之间取平衡。
	liveFrameQuality = 60
	// liveAckTimeout 是回执超时。不回执浏览器就不再发下一帧。
	liveAckTimeout = 5 * time.Second
)

// Frame 是一帧画面。
type Frame struct {
	// Data 是 base64 编码的 JPEG，直接可以塞进 img 的 src。
	Data string `json:"data"`
	// Width/Height 是这一帧的像素尺寸，前端按它换算点击坐标。
	Width  int `json:"width"`
	Height int `json:"height"`
	// PageX/PageY/Scale 来自 CDP 的帧元数据，页面滚动或缩放时坐标要用它换算。
	PageX  float64 `json:"page_x"`
	PageY  float64 `json:"page_y"`
	Scale  float64 `json:"scale"`
	TabURL string  `json:"tab_url,omitempty"`
}

// MouseEvent 是一次鼠标输入。字段名对齐 CDP，前端传过来什么就是什么。
type MouseEvent struct {
	Type       string  `json:"type"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Button     string  `json:"button,omitempty"`
	Buttons    int     `json:"buttons,omitempty"`
	ClickCount int     `json:"click_count,omitempty"`
	DeltaX     float64 `json:"delta_x,omitempty"`
	DeltaY     float64 `json:"delta_y,omitempty"`
	Modifiers  int     `json:"modifiers,omitempty"`
}

// KeyEvent 是一次键盘输入。
type KeyEvent struct {
	Type                  string `json:"type"`
	Key                   string `json:"key,omitempty"`
	Code                  string `json:"code,omitempty"`
	Text                  string `json:"text,omitempty"`
	WindowsVirtualKeyCode int    `json:"windows_virtual_key_code,omitempty"`
	Modifiers             int    `json:"modifiers,omitempty"`
}

var (
	allowedMouseTypes = map[string]bool{
		"mousePressed": true, "mouseReleased": true, "mouseMoved": true, "mouseWheel": true,
	}
	allowedKeyTypes = map[string]bool{
		"keyDown": true, "keyUp": true, "rawKeyDown": true, "char": true,
	}
)

// Live 是一条实时画面会话：一个标签页的画面出去，用户的输入进来。
type Live struct {
	session *Session
	frames  chan Frame
	tabURL  string

	mu     sync.Mutex
	closed bool
}

// StartLive 连上标签页并开始推帧。
func StartLive(ctx context.Context, websocketURL, tabURL string, width, height int) (*Live, error) {
	session, err := Dial(ctx, websocketURL)
	if err != nil {
		return nil, err
	}
	live := &Live{session: session, frames: make(chan Frame, 4), tabURL: tabURL}
	if err := session.Call(ctx, "Page.enable", nil, nil); err != nil {
		session.Close()
		return nil, err
	}
	// 先把焦点给页面，否则无头模式下键盘事件会被丢掉。
	_ = session.Call(ctx, "Emulation.setFocusEmulationEnabled", map[string]any{"enabled": true}, nil)
	if err := session.Call(ctx, "Page.startScreencast", map[string]any{
		"format":        "jpeg",
		"quality":       liveFrameQuality,
		"maxWidth":      width,
		"maxHeight":     height,
		"everyNthFrame": 1,
	}, nil); err != nil {
		session.Close()
		return nil, err
	}
	go func() {
		defer recoverGoroutinePanic("screencast")
		live.pump()
	}()
	return live, nil
}

// Frames 返回画面流。
func (l *Live) Frames() <-chan Frame { return l.frames }

func (l *Live) pump() {
	defer close(l.frames)
	for event := range l.session.Events() {
		if event.Method != "Page.screencastFrame" {
			continue
		}
		var payload struct {
			Data      string `json:"data"`
			SessionID int    `json:"sessionId"`
			Metadata  struct {
				OffsetTop       float64 `json:"offsetTop"`
				PageScaleFactor float64 `json:"pageScaleFactor"`
				DeviceWidth     float64 `json:"deviceWidth"`
				DeviceHeight    float64 `json:"deviceHeight"`
				ScrollOffsetX   float64 `json:"scrollOffsetX"`
				ScrollOffsetY   float64 `json:"scrollOffsetY"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(event.Params, &payload); err != nil {
			continue
		}
		// 不回执浏览器就不发下一帧，所以先回执再投递。
		ackCtx, cancel := context.WithTimeout(context.Background(), liveAckTimeout)
		_ = l.session.Call(ackCtx, "Page.screencastFrameAck", map[string]any{"sessionId": payload.SessionID}, nil)
		cancel()
		frame := Frame{
			Data:   payload.Data,
			Width:  int(payload.Metadata.DeviceWidth),
			Height: int(payload.Metadata.DeviceHeight),
			PageX:  payload.Metadata.ScrollOffsetX,
			PageY:  payload.Metadata.ScrollOffsetY,
			Scale:  payload.Metadata.PageScaleFactor,
			TabURL: l.tabURL,
		}
		select {
		case l.frames <- frame:
		default:
			select {
			case <-l.frames:
			default:
			}
			select {
			case l.frames <- frame:
			default:
			}
		}
	}
}

// Mouse 下发一次鼠标输入。
func (l *Live) Mouse(ctx context.Context, event MouseEvent) error {
	if !allowedMouseTypes[event.Type] {
		return errors.New("不认识的鼠标事件：" + event.Type)
	}
	params := map[string]any{
		"type":       event.Type,
		"x":          event.X,
		"y":          event.Y,
		"modifiers":  event.Modifiers,
		"clickCount": event.ClickCount,
	}
	if event.Button != "" {
		params["button"] = event.Button
		params["buttons"] = event.Buttons
	}
	if event.Type == "mouseWheel" {
		params["deltaX"] = event.DeltaX
		params["deltaY"] = event.DeltaY
	}
	return l.session.Call(ctx, "Input.dispatchMouseEvent", params, nil)
}

// Key 下发一次键盘输入。
func (l *Live) Key(ctx context.Context, event KeyEvent) error {
	if !allowedKeyTypes[event.Type] {
		return errors.New("不认识的键盘事件：" + event.Type)
	}
	params := map[string]any{
		"type":      event.Type,
		"modifiers": event.Modifiers,
	}
	if event.Key != "" {
		params["key"] = event.Key
	}
	if event.Code != "" {
		params["code"] = event.Code
	}
	if event.Text != "" {
		params["text"] = event.Text
	}
	if event.WindowsVirtualKeyCode != 0 {
		params["windowsVirtualKeyCode"] = event.WindowsVirtualKeyCode
		params["nativeVirtualKeyCode"] = event.WindowsVirtualKeyCode
	}
	return l.session.Call(ctx, "Input.dispatchKeyEvent", params, nil)
}

// Text 直接插入一段文字。中文输入法走的是这条路，不是逐键敲。
func (l *Live) Text(ctx context.Context, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return l.session.Call(ctx, "Input.insertText", map[string]any{"text": text}, nil)
}

// Navigate 在这个标签页里跳转。站点是否允许由调用方先判断。
func (l *Live) Navigate(ctx context.Context, pageURL string) error {
	l.tabURL = pageURL
	return l.session.Call(ctx, "Page.navigate", map[string]any{"url": pageURL}, nil)
}

// Reload 重新加载当前页面。
func (l *Live) Reload(ctx context.Context) error {
	return l.session.Call(ctx, "Page.reload", nil, nil)
}

// Back 后退一步。历史为空时静默返回。
func (l *Live) Back(ctx context.Context) error {
	var history struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID int `json:"id"`
		} `json:"entries"`
	}
	if err := l.session.Call(ctx, "Page.getNavigationHistory", nil, &history); err != nil {
		return err
	}
	if history.CurrentIndex <= 0 || len(history.Entries) == 0 {
		return nil
	}
	return l.session.Call(ctx, "Page.navigateToHistoryEntry",
		map[string]any{"entryId": history.Entries[history.CurrentIndex-1].ID}, nil)
}

// Close 结束这条会话。
func (l *Live) Close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	l.mu.Unlock()
	stopCtx, cancel := context.WithTimeout(context.Background(), liveAckTimeout)
	_ = l.session.Call(stopCtx, "Page.stopScreencast", nil, nil)
	cancel()
	l.session.Close()
}
