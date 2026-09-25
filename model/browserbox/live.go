// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserbox

import (
	"context"
	"encoding/base64"
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
//
// 帧率跟着看的人走：浏览器每出一帧都要等回执才出下一帧，这里把回执推迟到帧真正
// 发给前端的那一刻（见 Ack）。以前收到就回执，浏览器按 60 帧往外推，远程连接的
// 带宽跟不上，帧就在缓冲里排队，画面落后好几秒，点一下要等几秒才看到反应。
const (
	// liveFrameQuality 是 JPEG 质量。60 在文字清晰和带宽之间取平衡。
	liveFrameQuality = 60
	// liveAckTimeout 是回执超时。不回执浏览器就不再发下一帧。
	liveAckTimeout = 5 * time.Second
)

// liveStartTimeout 是连上画面最多等多久。Page.enable 要渲染进程回，页面主线程被脚本
// 卡死或渲染进程崩了时它永远不回；以前这里跟着请求的 ctx 一直等，WebUI 就一直停在
// 「正在连接画面……」。是变量只为了测试能调短。
var liveStartTimeout = 10 * time.Second

// ErrLivePageUnresponsive 表示标签页在 liveStartTimeout 内没回话，画面连不上。
var ErrLivePageUnresponsive = errors.New("这个标签页 10 秒内没有响应（页面脚本卡住或渲染进程崩溃），画面连不上。机器人下次用浏览器时会把卡住的页救回来；一直这样可以先停止再启动内置浏览器")

// ErrLivePageCrashed 表示画面对应的标签页渲染进程崩了。崩掉的页已经被换回空白页，
// 重新连上就有画面。
var ErrLivePageCrashed = errors.New("这个标签页崩溃了（渲染进程退出，常见于页面太重、内存或 /dev/shm 不够），已换回空白页，画面马上重新连上")

// Frame 是一帧画面。JPEG 本身不进 JSON：前端收的是二进制帧，见 webui 的实时画面。
type Frame struct {
	JPEG []byte `json:"-"`
	// Width/Height 是这一帧的像素尺寸，前端按它换算点击坐标。
	Width  int `json:"width"`
	Height int `json:"height"`
	// PageX/PageY/Scale 来自 CDP 的帧元数据，页面滚动或缩放时坐标要用它换算。
	PageX float64 `json:"page_x"`
	PageY float64 `json:"page_y"`
	Scale float64 `json:"scale"`
	// Timestamp 是浏览器画出这一帧的时刻（Unix 秒），前端和测试拿它算画面滞后多少。
	Timestamp float64 `json:"timestamp,omitempty"`

	// ackID 是浏览器给这一帧的回执编号，Ack 用。
	ackID int
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

// PageInfo 是画面里这个标签页眼下的地址、标题和是否在加载。前端的地址栏和标题跟着
// 它走：以前只在连上画面那一刻读一次，机器人或用户点进别的页面之后，地址栏和标题
// 还停在最早那一页。
type PageInfo struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Loading bool   `json:"loading"`
}

// liveTitleTimeout 是读页面标题最多等多久。页面主线程卡住时 Runtime.evaluate 不回，
// 读标题在单独的 goroutine 里，不会拖住出帧，只是标题不更新。
const liveTitleTimeout = 3 * time.Second

// Live 是一条实时画面会话：一个标签页的画面出去，用户的输入进来。
type Live struct {
	session *Session
	// frames 只放最新的一帧：取走之前又来了新的，旧的直接作废。
	frames chan Frame
	// pages 同样只放最新的一份页面信息。
	pages chan PageInfo

	mu     sync.Mutex
	closed bool
	// err 是画面流为什么断了；正常关闭时为 nil。
	err error
	// page 是当前的页面信息，mainFrame 是主框架的 ID：子框架（广告、嵌入页）的
	// 跳转和加载不算这一页的。
	page      PageInfo
	mainFrame string
}

// StartLive 连上标签页并开始推帧。
func StartLive(ctx context.Context, websocketURL, tabURL string, width, height int) (*Live, error) {
	session, err := Dial(ctx, websocketURL)
	if err != nil {
		return nil, err
	}
	live := &Live{session: session, frames: make(chan Frame, 1), pages: make(chan PageInfo, 1), page: PageInfo{URL: tabURL}}
	startCtx, cancel := context.WithTimeout(ctx, liveStartTimeout)
	defer cancel()
	// Inspector 域由浏览器进程处理，页面卡着也回；对已经崩掉的标签页，它一开就先推一条
	// Inspector.targetCrashed，由 pump 认出来。
	_ = session.Call(startCtx, "Inspector.enable", nil, nil)
	if err := session.Call(startCtx, "Page.enable", nil, nil); err != nil {
		session.Close()
		return nil, liveStartError(ctx, startCtx, err)
	}
	var tree struct {
		FrameTree struct {
			Frame struct {
				ID  string `json:"id"`
				URL string `json:"url"`
			} `json:"frame"`
		} `json:"frameTree"`
	}
	if session.Call(startCtx, "Page.getFrameTree", nil, &tree) == nil {
		live.mainFrame = tree.FrameTree.Frame.ID
		if tree.FrameTree.Frame.URL != "" {
			live.page.URL = tree.FrameTree.Frame.URL
		}
	}
	// 先把焦点给页面，否则无头模式下键盘事件会被丢掉。
	_ = session.Call(startCtx, "Emulation.setFocusEmulationEnabled", map[string]any{"enabled": true}, nil)
	if err := session.Call(startCtx, "Page.startScreencast", map[string]any{
		"format":        "jpeg",
		"quality":       liveFrameQuality,
		"maxWidth":      width,
		"maxHeight":     height,
		"everyNthFrame": 1,
	}, nil); err != nil {
		session.Close()
		return nil, liveStartError(ctx, startCtx, err)
	}
	go func() {
		defer recoverGoroutinePanic("screencast")
		live.pump()
	}()
	live.refreshTitle()
	return live, nil
}

// Pages 返回页面信息的更新，里面永远只有最新的一份。
func (l *Live) Pages() <-chan PageInfo { return l.pages }

// Page 返回当前的页面信息。
func (l *Live) Page() PageInfo {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.page
}

// updatePage 改一下页面信息，有变化就投递出去。
func (l *Live) updatePage(change func(page *PageInfo)) {
	l.mu.Lock()
	before := l.page
	change(&l.page)
	after := l.page
	l.mu.Unlock()
	if after == before {
		return
	}
	select {
	case l.pages <- after:
	default:
		select {
		case <-l.pages:
		default:
		}
		select {
		case l.pages <- after:
		default:
		}
	}
}

// refreshTitle 在后台读一次页面标题。
func (l *Live) refreshTitle() {
	go func() {
		defer recoverGoroutinePanic("liveTitle")
		ctx, cancel := context.WithTimeout(context.Background(), liveTitleTimeout)
		defer cancel()
		var result struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		if l.session.Call(ctx, "Runtime.evaluate", map[string]any{"expression": "document.title", "returnByValue": true}, &result) != nil {
			return
		}
		l.updatePage(func(page *PageInfo) { page.Title = result.Result.Value })
	}()
}

// handlePageEvent 跟着主框架的跳转和加载更新页面信息。
func (l *Live) handlePageEvent(event Event) {
	var payload struct {
		FrameID string `json:"frameId"`
		URL     string `json:"url"`
		Frame   struct {
			ID       string `json:"id"`
			ParentID string `json:"parentId"`
			URL      string `json:"url"`
		} `json:"frame"`
	}
	if json.Unmarshal(event.Params, &payload) != nil {
		return
	}
	isMain := func(id string) bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		return id != "" && id == l.mainFrame
	}
	switch event.Method {
	case "Page.frameNavigated":
		if payload.Frame.ParentID != "" {
			return
		}
		l.mu.Lock()
		l.mainFrame = payload.Frame.ID
		l.mu.Unlock()
		// 标题要等页面加载完才有，这里先清空，免得新地址配着上一页的标题。
		l.updatePage(func(page *PageInfo) {
			page.URL = payload.Frame.URL
			page.Title = ""
		})
	case "Page.navigatedWithinDocument":
		if isMain(payload.FrameID) {
			l.updatePage(func(page *PageInfo) { page.URL = payload.URL })
			l.refreshTitle()
		}
	case "Page.frameStartedLoading":
		if isMain(payload.FrameID) {
			l.updatePage(func(page *PageInfo) { page.Loading = true })
		}
	case "Page.frameStoppedLoading":
		if isMain(payload.FrameID) {
			l.updatePage(func(page *PageInfo) { page.Loading = false })
			l.refreshTitle()
		}
	}
}

// liveStartError 把「连画面时标签页没回话」换成能看懂的原因；调用方自己取消的保持原样。
func liveStartError(parent, start context.Context, err error) error {
	if parent.Err() == nil && errors.Is(start.Err(), context.DeadlineExceeded) {
		return ErrLivePageUnresponsive
	}
	return err
}

// Frames 返回画面流，里面永远只有最新的一帧。取走的每一帧都要 Ack，否则浏览器
// 不再出下一帧。
func (l *Live) Frames() <-chan Frame { return l.frames }

// Ack 告诉浏览器这一帧已经发出去了，可以出下一帧。
func (l *Live) Ack(frame Frame) {
	ackCtx, cancel := context.WithTimeout(context.Background(), liveAckTimeout)
	_ = l.session.Call(ackCtx, "Page.screencastFrameAck", map[string]any{"sessionId": frame.ackID}, nil)
	cancel()
}

// Err 是画面流为什么断了：标签页崩溃时是 ErrLivePageCrashed，正常结束时为 nil。
// 在 Frames 关闭之后读。
func (l *Live) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

func (l *Live) pump() {
	defer close(l.frames)
	for event := range l.session.Events() {
		if event.Method == "Inspector.targetCrashed" {
			// 崩掉的页不会再出帧，别让前端对着最后一帧干等。导航由浏览器进程处理，
			// 会换一个新的渲染进程，下一次连画面就是一张能用的空白页。
			resetCtx, cancel := context.WithTimeout(context.Background(), liveAckTimeout)
			_ = l.session.Call(resetCtx, "Page.navigate", map[string]any{"url": "about:blank"}, nil)
			cancel()
			l.mu.Lock()
			l.err = ErrLivePageCrashed
			l.mu.Unlock()
			return
		}
		if event.Method != "Page.screencastFrame" {
			l.handlePageEvent(event)
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
				Timestamp       float64 `json:"timestamp"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(event.Params, &payload); err != nil {
			continue
		}
		jpegBytes, err := base64.StdEncoding.DecodeString(payload.Data)
		if err != nil {
			l.Ack(Frame{ackID: payload.SessionID})
			continue
		}
		frame := Frame{
			JPEG:      jpegBytes,
			ackID:     payload.SessionID,
			Width:     int(payload.Metadata.DeviceWidth),
			Height:    int(payload.Metadata.DeviceHeight),
			PageX:     payload.Metadata.ScrollOffsetX,
			PageY:     payload.Metadata.ScrollOffsetY,
			Scale:     payload.Metadata.PageScaleFactor,
			Timestamp: payload.Metadata.Timestamp,
		}
		// 还没被取走的那一帧已经过时了：换成这一帧，并替它回执，浏览器才会接着出帧。
		select {
		case l.frames <- frame:
		default:
			select {
			case stale := <-l.frames:
				l.Ack(stale)
			default:
			}
			select {
			case l.frames <- frame:
			default:
				l.Ack(frame)
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
