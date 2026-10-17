// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// 搜索结果页每次都新起一个 Chrome，光启动就要 1–2 秒。搜索复用一个常驻进程，
// 每次渲染在里面新建一个隐身浏览器上下文：Cookie、缓存和存储互不相通，用完即销毁，
// 和一次性 Profile 的隔离效果一样。闲置一段时间后整个进程退出，不常驻占内存。
const warmSearchBrowserIdle = 3 * time.Minute

type warmBrowser struct {
	mu      sync.Mutex
	idle    time.Duration
	session *warmBrowserSession
	users   int
	timer   *time.Timer
}

type warmBrowserSession struct {
	executable      string
	window          BrowserWindow
	requestedWindow BrowserWindow
	process         *sandboxedChromeProcess
	dirs            browserSandboxDirs
	root            context.Context
	cancel          func()
}

var warmSearchBrowser = &warmBrowser{idle: warmSearchBrowserIdle}

func (s *warmBrowserSession) alive() bool {
	if s == nil || s.root.Err() != nil {
		return false
	}
	select {
	case <-s.process.done:
		return false
	default:
		return true
	}
}

func (s *warmBrowserSession) close() {
	s.cancel()
	s.process.stop()
	s.dirs.remove()
}

// acquire 返回可用的常驻浏览器；release 必须在渲染结束后调用。
func (w *warmBrowser) acquire(ctx context.Context, executable string, cfg SandboxedBrowserConfig) (*warmBrowserSession, func(), error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	if w.session != nil && (w.session.executable != executable || w.session.requestedWindow != cfg.Window) && w.users > 0 {
		return nil, nil, errors.New("常驻搜索浏览器配置与当前请求不同")
	}
	if w.session != nil && (!w.session.alive() || w.session.executable != executable || w.session.requestedWindow != cfg.Window) && w.users == 0 {
		w.session.close()
		w.session = nil
	}
	if w.session == nil || !w.session.alive() {
		if w.users > 0 {
			return nil, nil, errors.New("常驻搜索浏览器不可用")
		}
		session, err := startWarmBrowser(ctx, executable, cfg)
		if err != nil {
			return nil, nil, err
		}
		w.session = session
	}
	w.users++
	session := w.session
	var once sync.Once
	return session, func() { once.Do(func() { w.release(session) }) }, nil
}

func (w *warmBrowser) release(session *warmBrowserSession) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.users--
	if w.session != session {
		return
	}
	if !session.alive() && w.users == 0 {
		session.close()
		w.session = nil
		return
	}
	if w.users == 0 {
		w.timer = time.AfterFunc(w.idle, func() {
			w.mu.Lock()
			defer w.mu.Unlock()
			if w.users == 0 && w.session == session {
				session.close()
				w.session = nil
			}
		})
	}
}

func startWarmBrowser(ctx context.Context, executable string, cfg SandboxedBrowserConfig) (*warmBrowserSession, error) {
	dirs, err := newBrowserSandboxDirs("diana-search-browser-")
	if err != nil {
		return nil, err
	}
	// 进程活得比这一次请求长，不能挂在请求的 ctx 上。
	process, err := launchSandboxedChrome(context.Background(), executable, dirs.root, dirs.profile, dirs.cache, dirs.crash, cfg)
	if err != nil {
		dirs.remove()
		return nil, err
	}
	allocatorCtx, cancelAllocator := chromedp.NewRemoteAllocator(context.Background(), process.wsURL, chromedp.NoModifyURL)
	root, cancelRoot := chromedp.NewContext(allocatorCtx)
	session := &warmBrowserSession{executable: executable, window: process.window, requestedWindow: cfg.Window, process: process, dirs: dirs, root: root, cancel: func() { cancelRoot(); cancelAllocator() }}
	connected := make(chan error, 1)
	go func() {
		connectErr := errors.New("connecting search browser panicked")
		defer func() { connected <- connectErr }()
		defer recoverGoroutinePanic("warm_search_browser")
		connectErr = chromedp.Run(root)
	}()
	select {
	case err = <-connected:
	case <-ctx.Done():
		err = ctx.Err()
	case <-time.After(browserStartupTimeout):
		err = errors.New("连接常驻搜索浏览器超时")
	}
	if err != nil {
		session.close()
		return nil, fmt.Errorf("start warm search browser: %w: %s", err, compactBrowserError(process.diagnostics.String()))
	}
	return session, nil
}

// openIsolatedPage 在常驻浏览器里新建一个隐身上下文和页面，返回连到这个页面的
// chromedp 上下文；cleanup 关页面并销毁整个隐身上下文。
func (s *warmBrowserSession) openIsolatedPage(ctx context.Context) (context.Context, func(), error) {
	client, err := newCDPClient(ctx, s.process.wsURL, browserStartupTimeout)
	if err != nil {
		return nil, nil, err
	}
	var created struct {
		BrowserContextID string `json:"browserContextId"`
	}
	if err := client.call(ctx, "Target.createBrowserContext", map[string]any{"disposeOnDetach": false}, &created); err != nil {
		client.Close()
		return nil, nil, err
	}
	dispose := func() {
		disposeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = client.call(disposeCtx, "Target.disposeBrowserContext", map[string]any{"browserContextId": created.BrowserContextID}, nil)
		client.Close()
	}
	params := map[string]any{"url": "about:blank", "browserContextId": created.BrowserContextID}
	if s.window != BrowserWindowHeadless {
		// 有头时同 openBackgroundPage：不抢前台，隐藏模式开在屏幕外。
		params["newWindow"], params["background"], params["width"], params["height"] = true, s.window == BrowserWindowHidden, 1280, 960
		if s.window == BrowserWindowHidden {
			params["left"], params["top"] = offscreenWindowPosition, offscreenWindowPosition
		}
	}
	var page struct {
		TargetID string `json:"targetId"`
	}
	if err := client.call(ctx, "Target.createTarget", params, &page); err != nil || page.TargetID == "" {
		dispose()
		return nil, nil, errors.Join(errors.New("常驻搜索浏览器开页面失败"), err)
	}
	browserCtx, cancelBrowser := chromedp.NewContext(s.root, chromedp.WithTargetID(target.ID(page.TargetID)))
	stop := context.AfterFunc(ctx, cancelBrowser)
	return browserCtx, func() {
		stop()
		cancelBrowser()
		dispose()
	}, nil
}

// renderOnWarmBrowser 用常驻浏览器渲染一次；常驻浏览器起不来时返回 ok=false，
// 由调用方退回一次性浏览器。
func (b *SandboxedHeadlessBrowser) renderOnWarmBrowser(ctx context.Context, executable, rawURL string) (RenderedPage, bool, error) {
	renderStarted := time.Now()
	session, release, err := warmSearchBrowser.acquire(ctx, executable, b.cfg)
	if err != nil {
		return RenderedPage{}, false, nil
	}
	defer release()
	browserCtx, cleanup, err := session.openIsolatedPage(ctx)
	if err != nil {
		return RenderedPage{}, false, nil
	}
	defer cleanup()
	page, err := b.observeRender(ctx, browserCtx, session.process.diagnostics.String, executable, rawURL, renderStarted)
	return page, true, err
}
