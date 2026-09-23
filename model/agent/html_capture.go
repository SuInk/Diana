// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	cdppage "github.com/chromedp/cdproto/page"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// 把模型写的 HTML（允许脚本）渲染成静帧或逐帧画面。
//
// 和 CaptureHTMLScreenshot 的区别在于这里的页面可以带脚本：canvas 动画、
// requestAnimationFrame 循环、CSS 动画都要能动起来。脚本一开，隔离就不能再
// 只靠「内容里没有外链」，改成下面三层兜底：
//  1. 页面挂在一个假的 https 源上，由 CDP 直接应答，不落 file://。https 源
//     既读不到也跳不到本机文件，写个 <img src="file:///..."> 或把顶层导航到
//     file:// 都会被浏览器拒绝；
//  2. Fetch 拦截全部 http(s) 请求，只放行页面本身和预备好的字体，其余一律
//     失败；拦不到的 WebSocket 由 CSP 的 connect-src 'none' 挡；
//  3. 代理指向一个不存在的本地端口，WebRTC 只许走代理——就算前两层漏了什么，
//     包也出不了这台机器。
//
// 时间是虚拟的：注入的时钟接管 Date、performance.now、定时器和
// requestAnimationFrame，CSS/Web 动画和 SVG SMIL 按帧定位。截一帧花多久都不
// 影响画面，同一份内容每次出的帧都一样。

const (
	htmlCaptureOrigin = "https://diana-render.invalid"
	htmlCapturePage   = htmlCaptureOrigin + "/index.html"
	// MaxHTMLCaptureFrames 是单次逐帧截图的帧数硬上限，防止一次调用把工具
	// 预算全耗在截图上。
	MaxHTMLCaptureFrames = 600
	// MaxHTMLCaptureSide 是视口任一边的上限。
	MaxHTMLCaptureSide = 4096
)

// htmlCaptureCSP 只放行内联脚本和样式、data/blob 资源、同源字体。
const htmlCaptureCSP = "default-src 'none'; script-src 'unsafe-inline' 'unsafe-eval'; style-src 'unsafe-inline'; " +
	"img-src data: blob:; media-src data: blob:; font-src 'self' data:; connect-src 'none'; frame-src 'none'; " +
	"worker-src 'none'; object-src 'none'; form-action 'none'; base-uri 'none'"

// HTMLCaptureRequest 描述一次 HTML 截图。
type HTMLCaptureRequest struct {
	HTML string
	// FontFiles 由调用方校验过，页面里以 diana-font-<序号>.ttf 相对引用。
	FontFiles []string
	// Width 是视口宽度。Height 为零时按页面实际高度截整页，不超过 MaxHeight。
	Width, Height, MaxHeight int
	// FitSelector 非空时视口改成该元素的外框大小，用于只截一张 SVG 这类内容。
	FitSelector string
	Executable  string
	Timeout     time.Duration
}

// HTMLFrameOptions 控制逐帧截图。
type HTMLFrameOptions struct {
	FPS    int
	Frames int
	// JPEG 为真时出 JPEG（快、体积小，适合转视频），否则出 PNG。
	JPEG bool
	// OnFrame 按顺序收到每一帧；返回错误会中止截图。
	OnFrame func(index int, data []byte) error
}

// HTMLCaptureSize 是最终视口尺寸。
type HTMLCaptureSize struct {
	Width, Height int
}

// CaptureHTMLStill 把页面虚拟时间推进到 at 后截一张 PNG。推进一段时间再截，
// 是为了让淡入、逐项出现这类入场动画先走完，不至于截到一片空白。
func CaptureHTMLStill(ctx context.Context, req HTMLCaptureRequest, at time.Duration) ([]byte, HTMLCaptureSize, error) {
	var shot []byte
	var size HTMLCaptureSize
	err := withHTMLCaptureSession(ctx, req, func(session *htmlCaptureSession) error {
		const step = 16 * time.Millisecond
		for t := time.Duration(0); t < at; t += step {
			if err := session.seek(t); err != nil {
				return err
			}
		}
		if err := session.seek(at); err != nil {
			return err
		}
		var err error
		if size, err = session.fitViewport(); err != nil {
			return err
		}
		shot, err = session.capture(false)
		return err
	})
	return shot, size, err
}

// CaptureHTMLFrames 从虚拟时间 0 开始按帧率逐帧截图。
func CaptureHTMLFrames(ctx context.Context, req HTMLCaptureRequest, opts HTMLFrameOptions) (HTMLCaptureSize, error) {
	if opts.FPS <= 0 || opts.Frames <= 0 {
		return HTMLCaptureSize{}, errors.New("html capture: fps and frames must be positive")
	}
	if opts.Frames > MaxHTMLCaptureFrames {
		return HTMLCaptureSize{}, fmt.Errorf("html capture: %d frames exceeds limit %d", opts.Frames, MaxHTMLCaptureFrames)
	}
	if opts.OnFrame == nil {
		return HTMLCaptureSize{}, errors.New("html capture: missing frame callback")
	}
	var size HTMLCaptureSize
	err := withHTMLCaptureSession(ctx, req, func(session *htmlCaptureSession) error {
		frameDuration := time.Second / time.Duration(opts.FPS)
		for index := 0; index < opts.Frames; index++ {
			if err := session.seek(time.Duration(index) * frameDuration); err != nil {
				return err
			}
			// 尺寸在第一帧定下来：视频中途不能变尺寸，页面后来长高了也只截
			// 第一帧时的范围。
			if index == 0 {
				var err error
				if size, err = session.fitViewport(); err != nil {
					return err
				}
			}
			frame, err := session.capture(opts.JPEG)
			if err != nil {
				return fmt.Errorf("html capture frame %d: %w", index, err)
			}
			if err := opts.OnFrame(index, frame); err != nil {
				return err
			}
		}
		return nil
	})
	return size, err
}

type htmlCaptureSession struct {
	ctx context.Context
	req HTMLCaptureRequest
}

func withHTMLCaptureSession(ctx context.Context, req HTMLCaptureRequest, run func(*htmlCaptureSession) error) error {
	if strings.TrimSpace(req.HTML) == "" {
		return errors.New("html capture: empty html")
	}
	if req.Width <= 0 {
		req.Width = 1000
	}
	if req.MaxHeight <= 0 {
		req.MaxHeight = 2600
	}
	req.Width = min(req.Width, MaxHTMLCaptureSide)
	req.Height = min(req.Height, MaxHTMLCaptureSide)
	req.MaxHeight = min(req.MaxHeight, MaxHTMLCaptureSide)
	if req.Timeout <= 0 {
		req.Timeout = defaultScreenshotTimeout
	}
	if len(req.FontFiles) > 16 {
		return errors.New("html capture: too many font files")
	}
	fonts := make(map[string]string, len(req.FontFiles))
	for index, file := range req.FontFiles {
		fonts[fmt.Sprintf("%s/diana-font-%d.ttf", htmlCaptureOrigin, index)] = file
	}

	executable, err := findHeadlessBrowserExecutable(req.Executable)
	if err != nil {
		return fmt.Errorf("html capture: %w", err)
	}
	dirs, err := newBrowserSandboxDirs("diana-html-capture-")
	if err != nil {
		return err
	}
	defer dirs.remove()
	ctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	process, err := launchChromeProcess(ctx, executable, dirs.root, htmlCaptureChromeArgs(dirs.profile, dirs.cache, dirs.crash))
	if err != nil {
		return err
	}
	defer process.stop()
	allocator, cancelAllocator := chromedp.NewRemoteAllocator(ctx, process.wsURL, chromedp.NoModifyURL)
	defer cancelAllocator()
	browser, cancelBrowser := chromedp.NewContext(allocator)
	defer cancelBrowser()

	chromedp.ListenTarget(browser, func(event any) {
		if paused, ok := event.(*fetch.EventRequestPaused); ok {
			answerHTMLCaptureRequest(browser, paused, req.HTML, fonts)
		}
	})
	height := req.Height
	if height <= 0 {
		height = min(900, req.MaxHeight)
	}
	err = chromedp.Run(browser,
		fetch.Enable().WithPatterns([]*fetch.RequestPattern{{URLPattern: "*", RequestStage: fetch.RequestStageRequest}}),
		emulation.SetDeviceMetricsOverride(int64(req.Width), int64(height), 1, false),
		chromedp.ActionFunc(func(actionCtx context.Context) error {
			_, err := cdppage.AddScriptToEvaluateOnNewDocument(htmlCaptureClockScript).Do(actionCtx)
			return err
		}),
		chromedp.Navigate(htmlCapturePage),
	)
	if err != nil {
		return fmt.Errorf("html capture: load page: %w: %s", err, compactBrowserError(process.diagnostics.String()))
	}
	var fontsReady bool
	if err := chromedp.Run(browser, chromedp.Evaluate(`document.fonts.ready.then(() => true)`, &fontsReady, awaitPromise)); err != nil {
		return fmt.Errorf("html capture: wait fonts: %w", err)
	}
	return run(&htmlCaptureSession{ctx: browser, req: req})
}

// htmlCaptureChromeArgs 在沙盒底座上再加断网参数。
func htmlCaptureChromeArgs(profileDir, cacheDir, crashDir string) []string {
	args := sandboxedChromeArgs(profileDir, cacheDir, crashDir, sandboxedBrowserConfigWithDefaults(SandboxedBrowserConfig{}))
	args = append(args,
		"--force-device-scale-factor=1",
		"--disable-gpu",
		// 9 是 discard 端口，本机上不会有人听：所有经代理的连接都直接失败。
		"--proxy-server=http://127.0.0.1:9",
		"--proxy-bypass-list=<-loopback>",
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
	)
	// 这里跑的是带脚本的页面，能开进程沙盒就开。只有 root 身份起不来沙盒，
	// 和截图那条路的兼容取舍一样。
	if os.Geteuid() == 0 {
		args = append(args, "--no-sandbox")
	}
	return append(args, "about:blank")
}

func answerHTMLCaptureRequest(ctx context.Context, event *fetch.EventRequestPaused, html string, fonts map[string]string) {
	if event == nil || event.Request == nil {
		return
	}
	requestID, rawURL := event.RequestID, event.Request.URL
	go func() {
		defer recoverGoroutinePanic("html_capture.answerRequest")
		browserContext := chromedp.FromContext(ctx)
		if browserContext == nil || browserContext.Target == nil {
			return
		}
		executorCtx := cdp.WithExecutor(ctx, browserContext.Target)
		target := strings.SplitN(rawURL, "#", 2)[0]
		switch {
		case target == htmlCapturePage:
			_ = fetch.FulfillRequest(requestID, 200).
				WithResponseHeaders([]*fetch.HeaderEntry{
					{Name: "Content-Type", Value: "text/html; charset=utf-8"},
					{Name: "Content-Security-Policy", Value: htmlCaptureCSP},
					{Name: "Cache-Control", Value: "no-store"},
				}).
				WithBody(base64.StdEncoding.EncodeToString([]byte(html))).
				Do(executorCtx)
		case fonts[target] != "":
			data, err := os.ReadFile(fonts[target])
			if err != nil {
				_ = fetch.FailRequest(requestID, network.ErrorReasonFailed).Do(executorCtx)
				return
			}
			_ = fetch.FulfillRequest(requestID, 200).
				WithResponseHeaders([]*fetch.HeaderEntry{{Name: "Content-Type", Value: "font/" + strings.TrimPrefix(path.Ext(target), ".")}}).
				WithBody(base64.StdEncoding.EncodeToString(data)).
				Do(executorCtx)
		default:
			_ = fetch.FailRequest(requestID, network.ErrorReasonBlockedByClient).Do(executorCtx)
		}
	}()
}

func (s *htmlCaptureSession) seek(at time.Duration) error {
	ms := float64(at) / float64(time.Millisecond)
	var ok bool
	if err := chromedp.Run(s.ctx, chromedp.Evaluate(fmt.Sprintf(`window.__dianaSeek(%s)`, formatJSNumber(ms)), &ok)); err != nil {
		return fmt.Errorf("html capture: advance clock: %w", err)
	}
	if !ok {
		// 页面把注入的时钟弄丢了（顶层导航到别处等），后面的帧已经不可信。
		return errors.New("html capture: page clock missing, the page probably navigated away")
	}
	return nil
}

// fitViewport 按请求定下最终视口：固定尺寸、整页高度或某个元素的外框。
func (s *htmlCaptureSession) fitViewport() (HTMLCaptureSize, error) {
	size := HTMLCaptureSize{Width: s.req.Width, Height: s.req.Height}
	if selector := strings.TrimSpace(s.req.FitSelector); selector != "" {
		var box struct{ Width, Height float64 }
		script := fmt.Sprintf(`(() => { const el = document.querySelector(%q); if (!el) return {Width: 0, Height: 0}; const r = el.getBoundingClientRect(); return {Width: r.right, Height: r.bottom}; })()`, selector)
		if err := chromedp.Run(s.ctx, chromedp.Evaluate(script, &box)); err != nil {
			return size, fmt.Errorf("html capture: measure %s: %w", selector, err)
		}
		if box.Width >= 1 && box.Height >= 1 {
			size.Width = clampCaptureSide(box.Width, s.req.Width)
			size.Height = clampCaptureSide(box.Height, s.req.MaxHeight)
		}
	}
	if size.Height <= 0 {
		var height float64
		if err := chromedp.Run(s.ctx, chromedp.Evaluate(htmlCaptureContentHeightScript, &height)); err != nil {
			return size, fmt.Errorf("html capture: measure page: %w", err)
		}
		size.Height = clampCaptureSide(height, s.req.MaxHeight)
	}
	if err := chromedp.Run(s.ctx, emulation.SetDeviceMetricsOverride(int64(size.Width), int64(size.Height), 1, false)); err != nil {
		return size, err
	}
	return size, nil
}

// htmlCaptureContentHeightScript 量内容实际占到多高。不能用 scrollHeight：
// 它至少是视口高度，内容比视口矮时会截出一大片空白，页面背景还会平铺出接缝。
const htmlCaptureContentHeightScript = `(() => {
  const body = document.body;
  if (!body) return document.documentElement.scrollHeight;
  let bottom = body.getBoundingClientRect().bottom + (parseFloat(getComputedStyle(body).marginBottom) || 0);
  for (const el of body.querySelectorAll('*')) {
    const style = getComputedStyle(el);
    if (style.position === 'fixed' || style.display === 'none') continue;
    bottom = Math.max(bottom, el.getBoundingClientRect().bottom);
  }
  return bottom + window.scrollY;
})()`

func (s *htmlCaptureSession) capture(jpeg bool) ([]byte, error) {
	params := cdppage.CaptureScreenshot().WithFormat(cdppage.CaptureScreenshotFormatPng)
	if jpeg {
		params = cdppage.CaptureScreenshot().WithFormat(cdppage.CaptureScreenshotFormatJpeg).WithQuality(90)
	}
	var data []byte
	err := chromedp.Run(s.ctx, chromedp.ActionFunc(func(actionCtx context.Context) error {
		var err error
		data, err = params.Do(actionCtx)
		return err
	}))
	if err != nil {
		return nil, err
	}
	if len(data) > maxScreenshotBytes {
		return nil, errors.New("html capture: frame exceeds size limit")
	}
	return data, nil
}

func clampCaptureSide(value float64, limit int) int {
	side := int(math.Ceil(value))
	if limit > 0 {
		side = min(side, limit)
	}
	return max(side, 1)
}

func formatJSNumber(value float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", value), "0"), ".")
}

func awaitPromise(p *cdpruntime.EvaluateParams) *cdpruntime.EvaluateParams {
	return p.WithAwaitPromise(true)
}

// htmlCaptureClockScript 在页面任何脚本之前注入，接管页面能看到的时间。
//
// 定时器和 requestAnimationFrame 只在 __dianaSeek 时按虚拟时间顺序触发；CSS、
// Web Animations 在第一次被看见时记下出生时间，之后每帧暂停并定位到
// 「当前虚拟时间 - 出生时间」；SVG SMIL 直接定位整棵 SVG 的时间轴。
const htmlCaptureClockScript = `(() => {
  if (window.__dianaSeek) return;
  const epoch = 1767225600000;
  let now = 0;
  const RealDate = Date;
  class VirtualDate extends RealDate {
    constructor(...args) { if (args.length === 0) { super(epoch + now); } else { super(...args); } }
    static now() { return epoch + now; }
  }
  window.Date = VirtualDate;
  try { Object.defineProperty(performance, 'now', { value: () => now, configurable: true }); } catch (e) {}
  const report = (error) => { try { console.error(error); } catch (e) {} };
  const call = (fn, args) => {
    try { if (typeof fn === 'function') fn(...args); else (0, eval)(String(fn)); } catch (error) { report(error); }
  };
  let nextID = 1;
  const timers = new Map();
  const frames = new Map();
  window.setTimeout = (fn, ms, ...args) => { const id = nextID++; timers.set(id, { at: now + Math.max(0, Number(ms) || 0), fn, args, every: 0 }); return id; };
  window.setInterval = (fn, ms, ...args) => { const id = nextID++; const every = Math.max(1, Number(ms) || 0); timers.set(id, { at: now + every, fn, args, every }); return id; };
  window.clearTimeout = window.clearInterval = (id) => { timers.delete(id); };
  window.requestAnimationFrame = (fn) => { const id = nextID++; frames.set(id, fn); return id; };
  window.cancelAnimationFrame = (id) => { frames.delete(id); };
  const births = new WeakMap();
  window.__dianaSeek = (target) => {
    target = Math.max(now, Number(target) || 0);
    for (let guard = 0; guard < 100000; guard++) {
      let dueID = 0, due = null;
      for (const [id, timer] of timers) {
        if (timer.at <= target && (!due || timer.at < due.at)) { dueID = id; due = timer; }
      }
      if (!due) break;
      now = Math.max(now, due.at);
      if (due.every) { due.at += due.every; } else { timers.delete(dueID); }
      call(due.fn, due.args);
    }
    now = target;
    const pending = Array.from(frames.values());
    frames.clear();
    for (const fn of pending) call(fn, [now]);
    if (document.getAnimations) {
      for (const animation of document.getAnimations()) {
        if (!births.has(animation)) births.set(animation, now);
        try { animation.pause(); animation.currentTime = now - births.get(animation); } catch (error) { report(error); }
      }
    }
    for (const svg of document.querySelectorAll('svg')) {
      if (svg.ownerSVGElement || typeof svg.setCurrentTime !== 'function') continue;
      try { svg.pauseAnimations(); svg.setCurrentTime(now / 1000); } catch (error) { report(error); }
    }
    return true;
  };
})();`
