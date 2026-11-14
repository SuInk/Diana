// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/internal/procgroup"
	"github.com/SuInk/diana/internal/termux"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	cdppage "github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

const (
	chromeDevToolsPrefix       = "DevTools listening on "
	browserStartupTimeout      = 10 * time.Second
	browserProbeTimeout        = 3 * time.Second
	browserCaptureTimeout      = 5 * time.Second
	browserLoadingStableWindow = 5 * time.Second
	previousPageTextLimit      = 1200
	maxPreviousPageSnapshots   = 3
	semanticNetworkGraceFactor = 3
	// loadingDocumentMinText 是 HTML 还没下载完时肯收尾的最少正文字数。代理出口慢时
	// GitHub 这类页面卡在 loading，只有标题和一行「Skip to content」，不设下限就拿空壳收尾。
	loadingDocumentMinText = 200
)

type browserDOMProbe struct {
	URL               string `json:"url"`
	ReadyState        string `json:"ready_state"`
	Title             string `json:"title"`
	Description       string `json:"description"`
	TextLength        int    `json:"text_length"`
	SemanticSignature string `json:"semantic_signature"`
	DOMChanges        int64  `json:"dom_changes"`
	// PendingTimers 是页面还排着多少个没烧完的定时器；Instrumented 说明这个数
	// 到底有没有采到（脚本注入失败、页面自己换掉了 setTimeout 都会采不到）。
	PendingTimers int  `json:"pending_timers"`
	Instrumented  bool `json:"instrumented"`
	// NextTimeoutMS 是最近的一个一次性 setTimeout 还有多少毫秒到期，-1 表示一个都
	// 没排。setInterval 不计入：周期性心跳在页面稳住后不会再凭空补出内容。
	// 页面传给 setTimeout 的 delay 可以是小数，所以这里收 float64，不然一个
	// 171.7 就让整次探针解析失败、整页渲染报错。
	NextTimeoutMS float64 `json:"next_timeout_ms"`
}

// upcomingTimeout 报告页面是否排着一个会在 within 之内触发的一次性回调。
// 没采到定时器信号时答「没有」：那条路由最短观察窗兜底。
func (p browserDOMProbe) upcomingTimeout(within time.Duration) bool {
	return p.Instrumented && p.NextTimeoutMS >= 0 && time.Duration(p.NextTimeoutMS*float64(time.Millisecond)) <= within
}

func (p browserDOMProbe) meaningful() bool {
	if isChallengeInterstitial(p.Title) {
		return false
	}
	return strings.TrimSpace(p.Title) != "" || strings.TrimSpace(p.Description) != "" || p.TextLength > 0
}

// isChallengeInterstitial 认 Cloudflare 托管验证的过渡页。它几秒内就「稳定」了，
// 不拦的话渲染拿验证页收尾；真正的页面要等它自动跑完验证、跳转过去，常要 6–20 秒。
// 标题跟浏览器语言走，中英文都认。
func isChallengeInterstitial(title string) bool {
	switch strings.TrimSpace(title) {
	case "Just a moment...", "请稍候…", "请稍候...":
		return true
	}
	return false
}

type browserActivitySnapshot struct {
	NavigationChain []string
	LastNavigation  time.Time
	LastNetwork     time.Time
	Loading         bool
	PendingRequests int
	BlockedError    error
}

type browserActivityTracker struct {
	mu              sync.Mutex
	mainFrameID     cdp.FrameID
	navigationChain []string
	lastNavigation  time.Time
	lastNetwork     time.Time
	loading         bool
	pending         map[network.RequestID]struct{}
	blockedError    error
}

func newBrowserActivityTracker(rawURL string, now time.Time) *browserActivityTracker {
	return &browserActivityTracker{
		navigationChain: []string{rawURL},
		lastNavigation:  now,
		lastNetwork:     now,
		loading:         true,
		pending:         map[network.RequestID]struct{}{},
	}
}

func (t *browserActivityTracker) observe(event any, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	switch value := event.(type) {
	case *cdppage.EventFrameStartedNavigating:
		if t.mainFrameID == "" || value.FrameID == t.mainFrameID {
			t.appendNavigationLocked(value.URL)
			t.lastNavigation = now
			t.loading = true
			t.pending = map[network.RequestID]struct{}{}
		}
	case *cdppage.EventFrameNavigated:
		if value.Frame != nil && value.Frame.ParentID == "" {
			t.mainFrameID = value.Frame.ID
			t.appendNavigationLocked(value.Frame.URL)
			t.lastNavigation = now
			t.loading = true
			t.pending = map[network.RequestID]struct{}{}
		}
	case *cdppage.EventFrameStartedLoading:
		if value.FrameID == t.mainFrameID {
			t.loading = true
		}
	case *cdppage.EventFrameStoppedLoading:
		if value.FrameID == t.mainFrameID {
			t.loading = false
		}
	case *network.EventRequestWillBeSent:
		if !importantBrowserResource(value.Type) || (t.mainFrameID != "" && value.FrameID != "" && value.FrameID != t.mainFrameID) {
			return
		}
		t.pending[value.RequestID] = struct{}{}
		t.lastNetwork = now
		if value.Type == network.ResourceTypeDocument && value.Request != nil {
			t.appendNavigationLocked(value.Request.URL)
		}
	case *network.EventLoadingFinished:
		if _, ok := t.pending[value.RequestID]; ok {
			delete(t.pending, value.RequestID)
			t.lastNetwork = now
		}
	case *network.EventLoadingFailed:
		if _, ok := t.pending[value.RequestID]; ok {
			delete(t.pending, value.RequestID)
			t.lastNetwork = now
		}
	}
}

func (t *browserActivityTracker) appendNavigationLocked(rawURL string) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || rawURL == "about:blank" {
		return
	}
	if len(t.navigationChain) == 0 || t.navigationChain[len(t.navigationChain)-1] != rawURL {
		t.navigationChain = append(t.navigationChain, rawURL)
	}
}

func (t *browserActivityTracker) block(err error) {
	if err == nil {
		return
	}
	t.mu.Lock()
	if t.blockedError == nil {
		t.blockedError = err
	}
	t.mu.Unlock()
}

func (t *browserActivityTracker) snapshot() browserActivitySnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return browserActivitySnapshot{
		NavigationChain: append([]string(nil), t.navigationChain...),
		LastNavigation:  t.lastNavigation,
		LastNetwork:     t.lastNetwork,
		Loading:         t.loading,
		PendingRequests: len(t.pending),
		BlockedError:    t.blockedError,
	}
}

func importantBrowserResource(resourceType network.ResourceType) bool {
	switch resourceType {
	case network.ResourceTypeMedia, network.ResourceTypeWebSocket, network.ResourceTypeEventSource,
		network.ResourceTypePing, network.ResourceTypeImage:
		return false
	default:
		return true
	}
}

type renderReadiness struct {
	started             time.Time
	lastURL             string
	lastSignature       string
	lastReadyState      string
	signatureSince      time.Time
	mutationURL         string
	lastMutationCount   int64
	totalDOMChanges     int64
	semanticChangeCount int64
}

type renderDecision struct {
	ContentStable  bool
	Complete       bool
	Reason         string
	StableFor      time.Duration
	DOMChanges     int64
	ContentChanges int64
}

func newRenderReadiness(started time.Time) *renderReadiness {
	return &renderReadiness{started: started, signatureSince: started}
}

func (r *renderReadiness) observe(now time.Time, probe browserDOMProbe, activity browserActivitySnapshot, cfg SandboxedBrowserConfig) renderDecision {
	if probe.URL != r.mutationURL {
		r.mutationURL = probe.URL
		r.lastMutationCount = probe.DOMChanges
		r.totalDOMChanges += max(int64(0), probe.DOMChanges)
	} else if probe.DOMChanges >= r.lastMutationCount {
		r.totalDOMChanges += probe.DOMChanges - r.lastMutationCount
		r.lastMutationCount = probe.DOMChanges
	}

	changed := r.lastURL == "" || probe.URL != r.lastURL || probe.SemanticSignature != r.lastSignature || probe.ReadyState != r.lastReadyState
	if changed {
		if r.lastURL != "" {
			r.semanticChangeCount++
		}
		r.lastURL = probe.URL
		r.lastSignature = probe.SemanticSignature
		r.lastReadyState = probe.ReadyState
		r.signatureSince = now
	}

	stableFor := now.Sub(r.signatureSince)
	navigationQuiet := now.Sub(activity.LastNavigation) >= cfg.StabilityWindow
	networkQuiet := activity.PendingRequests == 0 && now.Sub(activity.LastNetwork) >= cfg.NetworkIdleWindow
	semanticOverride := stableFor >= time.Duration(semanticNetworkGraceFactor)*cfg.StabilityWindow
	loadingStableWindow := max(browserLoadingStableWindow, 2*time.Duration(semanticNetworkGraceFactor)*cfg.StabilityWindow)
	documentReady := probe.ReadyState == "complete" ||
		(probe.ReadyState == "interactive" && semanticOverride) ||
		(probe.ReadyState == "loading" && probe.TextLength >= loadingDocumentMinText && stableFor >= loadingStableWindow)
	contentStable := documentReady && probe.meaningful() && navigationQuiet && stableFor >= cfg.StabilityWindow
	settled := contentStable && ((!activity.Loading && networkQuiet) || semanticOverride)
	observed := now.Sub(r.started)
	settledEarly := settledWithoutScheduledWork(probe, activity, settled, networkQuiet, stableFor, cfg.VirtualTimeBudget-observed, cfg)
	minimumObserved := observed >= cfg.VirtualTimeBudget || nothingLeftToWaitFor(probe, activity, settled, networkQuiet) || settledEarly

	reason := "waiting_for_dom"
	switch {
	case !minimumObserved:
		reason = "observing_for_delayed_changes"
	case !contentStable:
		reason = "dom_or_navigation_still_changing"
	case !settled:
		reason = "network_still_active"
	case settledEarly && observed < cfg.VirtualTimeBudget:
		reason = "settled_before_observation_window"
	default:
		reason = "dom_and_network_stable"
	}
	return renderDecision{
		ContentStable:  contentStable,
		Complete:       minimumObserved && settled,
		Reason:         reason,
		StableFor:      stableFor,
		DOMChanges:     r.totalDOMChanges,
		ContentChanges: r.semanticChangeCount,
	}
}

type capturedBrowserPage struct {
	signature string
	page      RenderedPage
}

type chromeDiagnosticBuffer struct {
	mu     sync.Mutex
	buffer cappedBuffer
}

func newChromeDiagnosticBuffer(limit int) *chromeDiagnosticBuffer {
	return &chromeDiagnosticBuffer{buffer: cappedBuffer{limit: limit}}
}

func (b *chromeDiagnosticBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(value)
}

func (b *chromeDiagnosticBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

type sandboxedChromeProcess struct {
	wsURL       string
	diagnostics *chromeDiagnosticBuffer
	cancel      context.CancelFunc
	done        <-chan error
	// window 是这一次实际怎么开的：Termux 上凑不出屏幕时会退回无头，别处始终是有头。
	window BrowserWindow
}

func (p *sandboxedChromeProcess) stop() {
	if p == nil {
		return
	}
	p.cancel()
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
	}
}

func launchSandboxedChrome(ctx context.Context, executable, root, profileDir, cacheDir, crashDir string, cfg SandboxedBrowserConfig) (*sandboxedChromeProcess, error) {
	return launchSandboxedChromeWithEnv(ctx, executable, root, profileDir, cacheDir, crashDir, cfg, nil)
}

func launchSandboxedChromeWithEnv(ctx context.Context, executable, root, profileDir, cacheDir, crashDir string, cfg SandboxedBrowserConfig, extraEnv []string) (*sandboxedChromeProcess, error) {
	env, err := resolveBrowserDisplay()
	// 别处坚持只用有头；Termux 里凑不出屏幕是常态（见 internal/termux），只能退回无头，
	// 否则手机上网页渲染整个不能用。
	headless := false
	if err != nil {
		if !termux.Detected() {
			return nil, err
		}
		env, headless = nil, true
	}
	env = append(env, extraEnv...)
	buildArgs := func() []string {
		args := sandboxedChromeArgsForWindow(profileDir, cacheDir, crashDir, cfg.Window)
		if headless {
			args = append([]string{"--headless=new"}, args...)
		}
		return append(args, cfg.ExtraArgs...)
	}
	process, err := launchChromeProcessWithEnv(ctx, executable, root, buildArgs(), env)
	// 「自动」档：沙箱起不来就去掉沙箱再试一次，只试一次（见 BrowserSandboxFallback）。
	if err != nil && ctx.Err() == nil && BrowserSandboxFallback(err.Error()) {
		clearChromeSingletonLocks(profileDir)
		process, err = launchChromeProcessWithEnv(ctx, executable, root, buildArgs(), env)
	}
	if err != nil {
		return nil, WithBrowserSandboxHint(err)
	}
	process.window = cfg.Window
	return process, nil
}

// offscreenWindowPosition 把看不见的有头窗口放到所有屏幕外面。
const offscreenWindowPosition = -32000

// openBackgroundPage 在有头浏览器里开一个页面，不抢前台：chromedp 自己建页面时会把
// 窗口拉到前面，macOS 上每读一个链接就把人正在用的窗口抢走一次。hidden 时窗口
// 开在屏幕外面。
func openBackgroundPage(ctx context.Context, browserWSURL string, window BrowserWindow) (string, error) {
	client, err := newCDPClient(ctx, browserWSURL, browserStartupTimeout)
	if err != nil {
		return "", err
	}
	defer client.Close()
	params := map[string]any{
		"url":        "about:blank",
		"newWindow":  true,
		"background": window == BrowserWindowHidden,
		"width":      1280,
		"height":     960,
	}
	if window == BrowserWindowHidden {
		params["left"] = offscreenWindowPosition
		params["top"] = offscreenWindowPosition
	}
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := client.call(ctx, "Target.createTarget", params, &created); err != nil {
		return "", err
	}
	if created.TargetID == "" {
		return "", errors.New("有头浏览器没有开出页面")
	}
	return created.TargetID, nil
}

func launchChromeProcessWithEnv(ctx context.Context, executable, root string, args, extraEnv []string) (*sandboxedChromeProcess, error) {
	processCtx, cancel := context.WithCancel(ctx)
	cmd := procgroup.CommandContext(processCtx, executable, args...)
	cmd.Env = append(sandboxedBrowserEnvironment(os.Environ(), root), extraEnv...)
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.WaitDelay = 2 * time.Second
	// root 运行时换成 diana 用户启动，沙箱照开；root 是这次运行独占的临时目录。
	if err := applyBrowserAccount(cmd, executable, args, root); err != nil {
		cancel()
		return nil, err
	}
	diagnostics := newChromeDiagnosticBuffer(32 * 1024)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	wsURL := make(chan string, 1)
	go func() {
		defer recoverGoroutinePanic("render_browser_cdp.scanChromeDiagnostics")
		scanChromeDiagnostics(stderr, diagnostics, wsURL)
	}()
	done := make(chan error, 1)
	go func() {
		defer recoverGoroutinePanic("render_browser_cdp.waitChromeProcess")
		done <- cmd.Wait()
	}()

	timer := time.NewTimer(browserStartupTimeout)
	defer timer.Stop()
	select {
	case value := <-wsURL:
		return &sandboxedChromeProcess{wsURL: value, diagnostics: diagnostics, cancel: cancel, done: done}, nil
	case err := <-done:
		cancel()
		return nil, fmt.Errorf("browser exited before CDP startup: %w: %s", err, compactBrowserError(diagnostics.String()))
	case <-timer.C:
		cancel()
		return nil, fmt.Errorf("browser CDP startup timeout: %s", compactBrowserError(diagnostics.String()))
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	}
}

// probeSandboxedChrome verifies the same sandbox/CDP path used for untrusted
// webpages. Local HTML screenshots intentionally have different requirements.
func probeSandboxedChrome(ctx context.Context, executable string) error {
	dirs, err := newBrowserSandboxDirs("diana-browser-probe-")
	if err != nil {
		return err
	}
	defer dirs.remove()
	process, err := launchSandboxedChrome(ctx, executable, dirs.root, dirs.profile, dirs.cache, dirs.crash, sandboxedBrowserConfigWithDefaults(SandboxedBrowserConfig{}))
	if err != nil {
		return err
	}
	defer process.stop()
	allocator, cancelAllocator := chromedp.NewRemoteAllocator(ctx, process.wsURL, chromedp.NoModifyURL)
	defer cancelAllocator()
	var contextOptions []chromedp.ContextOption
	targetID, err := openBackgroundPage(ctx, process.wsURL, process.window)
	if err != nil {
		return err
	}
	contextOptions = append(contextOptions, chromedp.WithTargetID(target.ID(targetID)))
	browser, cancelBrowser := chromedp.NewContext(allocator, contextOptions...)
	defer cancelBrowser()
	var value int
	if err := chromedp.Run(browser, chromedp.Evaluate("1+1", &value)); err != nil {
		return err
	}
	if value != 2 {
		return errors.New("browser JavaScript probe failed")
	}
	return nil
}

func scanChromeDiagnostics(reader io.Reader, diagnostics *chromeDiagnosticBuffer, wsURL chan<- string) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		_, _ = diagnostics.Write(append([]byte(line), '\n'))
		if index := strings.Index(line, chromeDevToolsPrefix); index >= 0 {
			value := strings.TrimSpace(line[index+len(chromeDevToolsPrefix):])
			select {
			case wsURL <- value:
			default:
			}
		}
	}
}

func (b *SandboxedBrowser) renderObservable(ctx context.Context, executable, root, profileDir, cacheDir, crashDir, rawURL string, screenshot ...*[]byte) (RenderedPage, error) {
	renderStarted := time.Now()
	process, err := launchSandboxedChrome(ctx, executable, root, profileDir, cacheDir, crashDir, b.cfg)
	if err != nil {
		return RenderedPage{}, err
	}
	defer process.stop()

	allocatorCtx, cancelAllocator := chromedp.NewRemoteAllocator(ctx, process.wsURL, chromedp.NoModifyURL)
	defer cancelAllocator()
	var contextOptions []chromedp.ContextOption
	targetID, err := openBackgroundPage(ctx, process.wsURL, process.window)
	if err != nil {
		return RenderedPage{}, fmt.Errorf("有头浏览器开页面失败：%w: %s", err, compactBrowserError(process.diagnostics.String()))
	}
	contextOptions = append(contextOptions, chromedp.WithTargetID(target.ID(targetID)))
	browserCtx, cancelBrowser := chromedp.NewContext(allocatorCtx, contextOptions...)
	defer cancelBrowser()
	return b.observeRender(ctx, browserCtx, process.diagnostics.String, executable, rawURL, renderStarted, screenshot...)
}

// observeRender 在已经连上的页面里导航并观察到可以收尾。一次性浏览器和搜索复用的
// 常驻浏览器共用这一段，只是页面从哪来不同。
func (b *SandboxedBrowser) observeRender(ctx, browserCtx context.Context, diagnostics func() string, executable, rawURL string, renderStarted time.Time, screenshot ...*[]byte) (RenderedPage, error) {
	tracker := newBrowserActivityTracker(rawURL, time.Now())
	chromedp.ListenTarget(browserCtx, func(event any) {
		tracker.observe(event, time.Now())
		if paused, ok := event.(*fetch.EventRequestPaused); ok {
			handlePausedBrowserDocument(browserCtx, tracker, paused, len(screenshot) > 0)
		}
	})

	// The first Run owns the remote browser lifecycle, so it must use the
	// long-lived browser context rather than a short setup timeout.
	if err := chromedp.Run(browserCtx); err != nil {
		return RenderedPage{}, fmt.Errorf("connect to browser CDP: %w", err)
	}
	setupTimeout := min(b.cfg.Timeout, browserStartupTimeout)
	setupCtx, cancelSetup := context.WithTimeout(browserCtx, setupTimeout)
	pattern := &fetch.RequestPattern{URLPattern: "*", RequestStage: fetch.RequestStageRequest}
	if len(screenshot) == 0 {
		pattern.ResourceType = network.ResourceTypeDocument
	}
	err := chromedp.Run(setupCtx,
		network.Enable(),
		chromedp.ActionFunc(func(actionCtx context.Context) error {
			if len(screenshot) > 0 {
				return network.SetBlockedURLs([]string{"ws://*", "wss://*", "file://*"}).Do(actionCtx)
			}
			return nil
		}),
		cdppage.Enable(),
		cdppage.SetLifecycleEventsEnabled(true),
		chromedp.ActionFunc(func(actionCtx context.Context) error {
			_, err := cdppage.AddScriptToEvaluateOnNewDocument(browserMutationObserverScript).Do(actionCtx)
			return err
		}),
		fetch.Enable().WithPatterns([]*fetch.RequestPattern{pattern}),
		chromedp.ActionFunc(func(actionCtx context.Context) error {
			return navigateRetryingNetworkChange(actionCtx, func(navCtx context.Context) (string, bool, error) {
				_, _, errorText, isDownload, err := cdppage.Navigate(rawURL).Do(navCtx)
				return errorText, isDownload, err
			})
		}),
	)
	setupTimedOut := errors.Is(setupCtx.Err(), context.DeadlineExceeded)
	cancelSetup()
	if err != nil {
		if blocked := tracker.snapshot().BlockedError; blocked != nil {
			return RenderedPage{}, blocked
		}
		// 服务器一直不回时 Page.navigate 就挂在这里。说清楚是页面没响应，别只交一句
		// context deadline exceeded：模型和插件都要靠这句话决定换地址还是重试。
		if setupTimedOut && ctx.Err() == nil {
			return RenderedPage{}, &browserTimeoutError{message: fmt.Sprintf("渲染 %s 超时：%s 内页面没有响应，已停止加载。这个网址可能打不开或响应太慢，换个地址或稍后再试", rawURL, setupTimeout)}
		}
		return RenderedPage{}, fmt.Errorf("browser CDP setup failed: %w: %s", err, compactBrowserError(diagnostics()))
	}

	observationStarted := time.Now()
	readiness := newRenderReadiness(observationStarted)
	earlyReady := newEarlyRenderReadiness(ctx)
	hardDeadline := renderStarted.Add(time.Duration(MaxAllowedBrowserTimeoutMS) * time.Millisecond)
	deadline := renderStarted.Add(b.cfg.Timeout)
	if deadline.After(hardDeadline) {
		deadline = hardDeadline
	}
	// 观察要比整个请求早一点收手，留出最后抓一张快照的时间。
	//
	// 这两个时间本来是同一刻（外层 ctx 就是按 cfg.Timeout 建的），谁先醒是抽签：
	// 抽到循环自己，就带着页面现有内容收尾；抽到 ctx，走的是「一张快照都没有就
	// 直接抛错」那条路，抛的还是光秃秃的 context deadline exceeded。慢站点上这两种
	// 结果完全随机——mimo.xiaomi.com 线上就是抛错那一种。
	deadline = withFinalCaptureReserve(ctx, deadline, b.cfg.Timeout)
	var deadlineNavigation time.Time
	ticker := time.NewTicker(b.cfg.PollInterval)
	defer ticker.Stop()
	var (
		captures              []capturedBrowserPage
		lastCapturedSignature string
		lastProbe             browserDOMProbe
		lastDecision          renderDecision
	)
	finish := func(probe browserDOMProbe, decision renderDecision, stable bool, reason string) (RenderedPage, error) {
		page, err := b.finishObservableRender(browserCtx, executable, rawURL, renderStarted, probe, decision, tracker.snapshot(), captures, stable, reason, screenshot...)
		if len(screenshot) > 0 {
			if blocked := tracker.snapshot().BlockedError; blocked != nil {
				*screenshot[0] = nil
				return RenderedPage{}, blocked
			}
		}
		return page, err
	}

	for {
		activity := tracker.snapshot()
		if activity.LastNavigation.After(deadlineNavigation) {
			deadlineNavigation = activity.LastNavigation
			candidate := activity.LastNavigation.Add(b.cfg.Timeout)
			if candidate.After(deadline) {
				deadline = candidate
			}
			if deadline.After(hardDeadline) {
				deadline = hardDeadline
			}
			// 页面跳转会把观察时间顺延，但顺延不能把收尾的预留吃掉：
			// 重定向站点上一吃掉就又变成「直接抛错」那条路。
			deadline = withFinalCaptureReserve(ctx, deadline, b.cfg.Timeout)
		}
		now := time.Now()
		if !now.Before(deadline) {
			return finish(lastProbe, lastDecision, false, "render_timeout_returning_last_non_empty_snapshot")
		}
		select {
		case <-ctx.Done():
			if len(captures) > 0 {
				return finish(lastProbe, lastDecision, false, "request_cancelled_returning_last_non_empty_snapshot")
			}
			// 连一张快照都没有：说清楚是超时且页面始终没给出可抓取的内容，
			// 别只抛一句 context deadline exceeded 让人以为是网络问题。
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return RenderedPage{}, &browserTimeoutError{message: fmt.Sprintf("渲染 %s 超时：%s 内页面始终没有可读取的内容", rawURL, time.Since(renderStarted).Round(time.Millisecond))}
			}
			return RenderedPage{}, fmt.Errorf("browser render ended after %s with no capturable content: %w", time.Since(renderStarted).Round(time.Millisecond), ctx.Err())
		case <-ticker.C:
		}

		activity = tracker.snapshot()
		if activity.BlockedError != nil {
			return RenderedPage{}, activity.BlockedError
		}
		probe, err := evaluateBrowserDOMProbe(browserCtx)
		if err != nil {
			if transientBrowserEvaluationError(err) {
				continue
			}
			switch probeFailureAction(err, browserCtx.Err()) {
			case probeRetry:
				continue
			case probeFinish:
				return b.finishObservableRender(browserCtx, executable, rawURL, renderStarted, lastProbe, lastDecision, tracker.snapshot(), captures, false, "probe_deadline_returning_last_non_empty_snapshot")
			}
			return RenderedPage{}, fmt.Errorf("inspect rendered DOM: %w", err)
		}
		lastProbe = probe
		lastDecision = readiness.observe(time.Now(), probe, activity, b.cfg)
		if lastDecision.ContentStable && probe.SemanticSignature != "" && probe.SemanticSignature != lastCapturedSignature {
			if page, captureErr := b.captureObservablePage(browserCtx, rawURL); captureErr == nil {
				captures = appendOrReplaceCapturedPage(captures, capturedBrowserPage{signature: probe.SemanticSignature, page: page})
				lastCapturedSignature = probe.SemanticSignature
			}
		}
		if lastDecision.Complete {
			return finish(probe, lastDecision, true, lastDecision.Reason)
		}
		if earlyReady.observe(time.Now(), probe, activity, b.cfg.StabilityWindow, func() (RenderedPage, error) { return b.captureObservablePage(browserCtx, rawURL) }) {
			return finish(probe, lastDecision, true, "ready_key_stable")
		}
	}
}

func handlePausedBrowserDocument(ctx context.Context, tracker *browserActivityTracker, event *fetch.EventRequestPaused, publicResources ...bool) {
	if event == nil || event.Request == nil {
		return
	}
	requestID := event.RequestID
	rawURL := event.Request.URL
	go func() {
		defer recoverGoroutinePanic("render_browser_cdp.handlePausedBrowserDocument")
		browserContext := chromedp.FromContext(ctx)
		if browserContext == nil || browserContext.Target == nil {
			return
		}
		executorCtx := cdp.WithExecutor(ctx, browserContext.Target)
		// Embedded data/blob assets have no network endpoint; all network resources
		// in a public screenshot still pass the same public-address check as navigation.
		var validateErr error
		if len(publicResources) > 0 && publicResources[0] {
			validateErr = validateScreenshotResource(ctx, rawURL, event.ResourceType)
		} else {
			validateErr = validateSandboxedBrowserURL(ctx, rawURL)
		}
		if err := validateErr; err != nil {
			tracker.block(fmt.Errorf("browser redirect blocked for %q: %w", rawURL, err))
			_ = fetch.FailRequest(requestID, network.ErrorReasonBlockedByClient).Do(executorCtx)
			return
		}
		if err := fetch.ContinueRequest(requestID).Do(executorCtx); err != nil && ctx.Err() == nil {
			tracker.block(fmt.Errorf("continue browser navigation %q: %w", rawURL, err))
		}
	}()
}

func validateScreenshotResource(ctx context.Context, rawURL string, resourceType network.ResourceType) error {
	if resourceType != network.ResourceTypeDocument && (strings.HasPrefix(rawURL, "data:") || strings.HasPrefix(rawURL, "blob:")) {
		return nil
	}
	return validateSandboxedBrowserURL(ctx, rawURL)
}

func evaluateBrowserDOMProbe(ctx context.Context) (browserDOMProbe, error) {
	probeCtx, cancel := context.WithTimeout(ctx, browserProbeTimeout)
	defer cancel()
	var probe browserDOMProbe
	err := chromedp.Run(probeCtx, chromedp.Evaluate(browserDOMProbeScript, &probe))
	return probe, err
}

func (b *SandboxedBrowser) captureObservablePage(ctx context.Context, requestedURL string) (RenderedPage, error) {
	captureCtx, cancel := context.WithTimeout(ctx, browserCaptureTimeout)
	defer cancel()
	var snapshot struct {
		URL        string `json:"url"`
		ReadyState string `json:"ready_state"`
		HTML       string `json:"html"`
		Truncated  bool   `json:"truncated"`
		DOMChanges int64  `json:"dom_changes"`
	}
	expression := fmt.Sprintf(browserDOMCaptureScript, b.cfg.MaxHTMLBytes, b.cfg.MaxHTMLBytes)
	if err := chromedp.Run(captureCtx, chromedp.Evaluate(expression, &snapshot)); err != nil {
		return RenderedPage{}, err
	}
	if strings.TrimSpace(snapshot.HTML) == "" {
		return RenderedPage{}, errors.New("browser returned an empty DOM snapshot")
	}
	page, err := parseRenderedPage([]byte(snapshot.HTML), requestedURL, b.cfg.MaxTextChars, snapshot.Truncated)
	if err != nil {
		return RenderedPage{}, err
	}
	if snapshot.URL != "" {
		page.URL = snapshot.URL
	}
	page.ReadyState = snapshot.ReadyState
	page.DOMChanges = snapshot.DOMChanges
	return page, nil
}

func (b *SandboxedBrowser) finishObservableRender(ctx context.Context, executable, requestedURL string, started time.Time, probe browserDOMProbe, decision renderDecision, activity browserActivitySnapshot, captures []capturedBrowserPage, stable bool, reason string, screenshot ...*[]byte) (RenderedPage, error) {
	if activity.BlockedError != nil {
		return RenderedPage{}, activity.BlockedError
	}
	page, err := b.captureObservablePage(ctx, requestedURL)
	if err != nil {
		if len(captures) == 0 {
			return RenderedPage{}, fmt.Errorf("browser did not produce a readable DOM: %w", err)
		}
		page = captures[len(captures)-1].page
	}
	page.RequestedURL = requestedURL
	page.Sandboxed = true
	page.BrowserEngine = filepath.Base(executable)
	page.ReadyState = firstNonEmptyString(probe.ReadyState, page.ReadyState)
	page.Stable = stable
	page.StabilityReason = reason
	page.WaitedMS = time.Since(started).Milliseconds()
	page.DOMChanges = max(page.DOMChanges, decision.DOMChanges)
	page.ContentChanges = decision.ContentChanges
	page.PendingRequests = activity.PendingRequests
	page.NavigationChain = dedupeConsecutiveStrings(activity.NavigationChain)
	page.PreviousPages = previousPageSnapshots(captures, page.URL)
	if len(screenshot) > 0 {
		captureCtx, cancel := context.WithTimeout(ctx, browserCaptureTimeout)
		defer cancel()
		var finalURL string
		if err := chromedp.Run(captureCtx,
			chromedp.ActionFunc(func(actionCtx context.Context) error {
				data, err := cdppage.CaptureScreenshot().WithFormat(cdppage.CaptureScreenshotFormatPng).WithFromSurface(true).Do(actionCtx)
				*screenshot[0] = data
				return err
			}),
			chromedp.Location(&finalURL),
		); err != nil {
			return RenderedPage{}, err
		}
		if finalURL != page.URL {
			return RenderedPage{}, errors.New("截图期间页面发生跳转，请重试")
		}
		if _, err := screenshotImagePart(*screenshot[0]); err != nil {
			return RenderedPage{}, err
		}
	}
	return page, nil
}

func appendOrReplaceCapturedPage(captures []capturedBrowserPage, capture capturedBrowserPage) []capturedBrowserPage {
	if len(captures) > 0 && captures[len(captures)-1].page.URL == capture.page.URL {
		captures[len(captures)-1] = capture
		return captures
	}
	return append(captures, capture)
}

func previousPageSnapshots(captures []capturedBrowserPage, finalURL string) []RenderedPageSnapshot {
	start := max(0, len(captures)-maxPreviousPageSnapshots)
	out := make([]RenderedPageSnapshot, 0, len(captures)-start)
	for _, capture := range captures[start:] {
		page := capture.page
		if page.URL == "" || page.URL == finalURL {
			continue
		}
		out = append(out, RenderedPageSnapshot{
			URL:         page.URL,
			Title:       page.Title,
			Description: page.Description,
			Text:        truncateText(page.Text, previousPageTextLimit),
		})
	}
	return out
}

func dedupeConsecutiveStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || value == "about:blank" || (len(out) > 0 && out[len(out)-1] == value) {
			continue
		}
		out = append(out, value)
	}
	return out
}

// browserFinalCaptureReserve 是留给「最后抓一张快照」的时间。抓取本身有
// browserCaptureTimeout 的预算，这里按它留，同时不超过总时长的五分之一——
// 短超时的调用不该把大半预算花在收尾上。
func withFinalCaptureReserve(ctx context.Context, deadline time.Time, timeout time.Duration) time.Time {
	ctxDeadline, ok := ctx.Deadline()
	if !ok {
		return deadline
	}
	reserve := min(browserCaptureTimeout, timeout/5)
	if reserve <= 0 {
		return deadline
	}
	reserved := ctxDeadline.Add(-reserve)
	if reserved.Before(deadline) {
		return reserved
	}
	return deadline
}

// nothingLeftToWaitFor 判断这个页面是不是确凿地没有后手了，有就不必等满最短
// 观察窗。
//
// 最短观察窗（VirtualTimeBudget，默认 8 秒）存在的理由只有一个：页面可能过几秒
// 才跳转、才补内容，早收手就会拿到半成品。代价是连一张静态页也要等满 8 秒，
// 而工具总预算才 60 秒。
//
// 但「过几秒才动」这件事是可以直接看出来的：延迟跳转、延迟渲染都要先排个
// setTimeout/setInterval。注入脚本把没烧完的回调数记下来，这里连同「文档已
// complete、网络静了、DOM 稳了、没有在途请求」一起看——全都成立时，页面确实
// 没有任何已排期的后续动作，再等下去也只是空耗。
//
// 采不到这个信号（脚本没注进去、页面自己换掉了 setTimeout）就退回按时间等，
// 宁可慢也不要拿半成品。
func nothingLeftToWaitFor(probe browserDOMProbe, activity browserActivitySnapshot, settled, networkQuiet bool) bool {
	return settled &&
		networkQuiet &&
		probe.Instrumented &&
		probe.PendingTimers == 0 &&
		probe.ReadyState == "complete" &&
		!activity.Loading &&
		activity.PendingRequests == 0
}

// settledWithoutScheduledWork 是上面那条的放宽版：页面已经 complete、网络静了、
// DOM 连续 cfg.SettledGrace 没动，而且没有会在剩下的观察窗里触发的一次性回调。
//
// 只看「一个定时器都没有」在线上几乎永远等不到：现代页面总挂着几个 setInterval
// 心跳、埋点轮询，或者一个几十秒后才到期的 setTimeout。生产 11-05～07 的 715 次
// 渲染，等待时间中位 10.1 秒、p90 12.0 秒，正文中位却只有 600 字——基本都是等满
// 8 秒最短观察窗。周期性回调在页面稳住之后再跳几拍也不会凭空补出内容；到期在
// 观察窗之外的回调，照旧等下去也等不到它。真正要接住的只有「几秒后才跳转、才
// 补内容」的那一类，它排的是观察窗内到期的一次性 setTimeout，这里只为它留着。
//
// 而且只有正文还很短的页面才让这种回调拦住收尾。实测 GitHub 仓库页、MCP 文档页
// 这类正文几千字的内容页也常年排着三五秒后到期的一次性回调（轮询链、埋点上报），
// 正文早就齐了；真正的延迟跳转页只有「Loading…please wait」一两行字。正文够长
// 就是内容页，不再为它的后台回调多等。
//
// 没采到定时器信号时（脚本没注进去、页面换掉了 setTimeout）照样按宽限期收尾：
// 宽限期本身已经比稳定窗长两倍多，延迟动作绝大多数在这之前就露头了。
func settledWithoutScheduledWork(probe browserDOMProbe, activity browserActivitySnapshot, settled, networkQuiet bool, stableFor, remaining time.Duration, cfg SandboxedBrowserConfig) bool {
	if cfg.SettledGrace <= 0 {
		return false
	}
	return settled &&
		networkQuiet &&
		probe.ReadyState == "complete" &&
		!activity.Loading &&
		activity.PendingRequests == 0 &&
		stableFor >= cfg.SettledGrace &&
		!(probe.TextLength < interstitialTextLimit && probe.upcomingTimeout(remaining))
}

// interstitialTextLimit 是「还可能是过渡页」的正文字数上限：延迟跳转、「正在加载」
// 的占位页只有一两行字，正文超过这个数的页面按内容页处理。
const interstitialTextLimit = 1500

// probeAction 是一次 DOM 探针失败之后该怎么办。
type probeAction int

const (
	// probeFail：探针说的是页面本身有问题，这次渲染到此为止。
	probeFail probeAction = iota
	// probeRetry：下一拍再探一次。
	probeRetry
	// probeFinish：渲染的总时间也到了，用手上已有的快照收尾。
	probeFinish
)

// probeFailureAction 判断一次 DOM 探针失败该重试、收尾还是判死。
//
// 探针自己只有 browserProbeTimeout 那点预算，而它要的是页面主线程空出来。某一刻
// 主线程被长任务占着探不动，只说明这一刻忙，不说明这次渲染失败——判死是渲染总
// 截止时间的事，手上已经抓到的快照更不该因此丢掉。
//
// 线上就是这么丢的：一次 3 秒探针超时，整页直接报「沙盒无头浏览器渲染失败」，
// 而同一个会话下一秒渲染别的站点完全正常。
func probeFailureAction(err error, browserCtxErr error) probeAction {
	switch {
	case err == nil:
		return probeRetry
	case transientBrowserEvaluationError(err):
		return probeRetry
	case !errors.Is(err, context.DeadlineExceeded):
		return probeFail
	case browserCtxErr == nil:
		// 只是这一拍探不动，整次渲染的时间还有。
		return probeRetry
	default:
		// 连浏览器上下文都到期了，别再探，拿手上的收尾。
		return probeFinish
	}
}

func transientBrowserEvaluationError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "context was destroyed") ||
		strings.Contains(text, "cannot find context") ||
		strings.Contains(text, "execution context") ||
		strings.Contains(text, "target closed")
}

const browserMutationObserverScript = `(function () {
  const state = { mutations: 0, lastMutationAt: Date.now(), pendingTimers: 0, instrumented: false, nextTimeoutAt: 0 };
  Object.defineProperty(globalThis, "__dianaRenderState", {
    configurable: false,
    enumerable: false,
    value: state
  });
  // 记还有多少「已排期但还没烧完」的回调。页面靠 setTimeout 延迟跳转、延迟补内容
  // 是最常见的一类慢，光看 DOM 和网络都是静的，看这个才知道它还有后手。
  // setInterval 只要没 clear 就一直算挂着。
  //
  // 一次性的 setTimeout 另记到期时刻：探针据此报出「最近的一个还有多久到期」。
  // 延迟跳转、延迟补内容排的都是一次性回调；setInterval 是周期性的心跳、轮询，
  // 页面稳住之后它再跳几拍也不会凭空多出内容，不算后手。
  try {
    const timeout = globalThis.setTimeout;
    const interval = globalThis.setInterval;
    const clearT = globalThis.clearTimeout;
    const clearI = globalThis.clearInterval;
    const live = new Set();
    const dueAt = new Map();
    const refreshNext = () => {
      let next = 0;
      for (const at of dueAt.values()) {
        if (!next || at < next) next = at;
      }
      state.nextTimeoutAt = next;
    };
    globalThis.setTimeout = function (fn, delay, ...rest) {
      if (typeof fn !== "function") return timeout.apply(this, arguments);
      let id;
      const wrapped = function () {
        live.delete(id);
        dueAt.delete(id);
        state.pendingTimers = live.size;
        refreshNext();
        return fn.apply(this, arguments);
      };
      id = timeout.call(this, wrapped, delay, ...rest);
      live.add(id);
      dueAt.set(id, Date.now() + Math.max(0, Number(delay) || 0));
      state.pendingTimers = live.size;
      refreshNext();
      return id;
    };
    globalThis.setInterval = function (fn, delay, ...rest) {
      const id = interval.apply(this, arguments);
      live.add(id);
      state.pendingTimers = live.size;
      return id;
    };
    globalThis.clearTimeout = function (id) {
      live.delete(id);
      dueAt.delete(id);
      state.pendingTimers = live.size;
      refreshNext();
      return clearT.apply(this, arguments);
    };
    globalThis.clearInterval = function (id) {
      live.delete(id);
      dueAt.delete(id);
      state.pendingTimers = live.size;
      refreshNext();
      return clearI.apply(this, arguments);
    };
    state.instrumented = true;
  } catch (_) {
    // 包不上就当没有这个信号，退回按最短观察窗等。
  }
  const install = () => {
    if (!document.documentElement) return;
    const observer = new MutationObserver((records) => {
      state.mutations += records.length;
      state.lastMutationAt = Date.now();
    });
    observer.observe(document.documentElement, {
      subtree: true,
      childList: true,
      characterData: true,
      attributes: true
    });
  };
  if (document.documentElement) install();
  else document.addEventListener("DOMContentLoaded", install, { once: true });
})();`

const browserDOMProbeScript = `(() => {
  const normalize = (value) => String(value || "").replace(/\s+/g, " ").trim();

  let state = globalThis.__dianaRenderState;
  if (!state) {
    state = { mutations: 0, lastMutationAt: Date.now() };
    try {
      Object.defineProperty(globalThis, "__dianaRenderState", {
        configurable: false,
        enumerable: false,
        value: state
      });
    } catch (_) {
      globalThis.__dianaRenderState = state;
    }
    if (document.documentElement) {
      const observer = new MutationObserver((records) => {
        state.mutations += records.length;
        state.lastMutationAt = Date.now();
      });
      observer.observe(document.documentElement, {
        subtree: true,
        childList: true,
        characterData: true,
        attributes: true
      });
    }
  }

  const descriptionNode = document.querySelector('meta[name="description"],meta[property="og:description"]');
  const description = normalize(descriptionNode && descriptionNode.content);
  const text = normalize(document.body ? document.body.innerText : "");
  // Stability follows the content that can actually fit in the LLM context.
  // Infinite feeds may keep appending recommendations without changing the
  // primary page information near the start of the visible text.
  const semanticText = text.slice(0, 12000);
  const semantic = [location.href, document.title, description, semanticText].join("\n");
  let hash = 2166136261;
  for (let index = 0; index < semantic.length; index++) {
    hash ^= semantic.charCodeAt(index);
    hash = Math.imul(hash, 16777619);
  }
  return {
    url: location.href,
    ready_state: document.readyState,
    title: normalize(document.title),
    description,
    text_length: text.length,
    semantic_signature: (hash >>> 0).toString(16) + ":" + semantic.length,
    dom_changes: Number(state.mutations || 0),
    pending_timers: Number(state.pendingTimers || 0),
    next_timeout_ms: state.nextTimeoutAt ? Math.max(0, Math.round(Number(state.nextTimeoutAt) - Date.now())) : -1,
    instrumented: Boolean(state.instrumented)
  };
})()`

const browserDOMCaptureScript = `(() => {
  const html = document.documentElement ? document.documentElement.outerHTML : "";
  const state = globalThis.__dianaRenderState || {};
  return {
    url: location.href,
    ready_state: document.readyState,
    html: html.slice(0, %d),
    truncated: html.length > %d,
    dom_changes: Number(state.mutations || 0)
  };
})()`

// clearChromeSingletonLocks 删掉上一次没起来的进程留在 profile 里的单实例锁，
// 重试时 Chrome 才不会以为已有实例在跑。
func clearChromeSingletonLocks(profileDir string) {
	for _, name := range []string{"SingletonLock", "SingletonSocket", "SingletonCookie"} {
		_ = os.Remove(filepath.Join(profileDir, name))
	}
}
