// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	cdppage "github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

const (
	defaultJSONCaptureBytes     = 4 << 20
	defaultJSONCaptureUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
)

// JSONCaptureRequest 描述一次「打开页面、截获页面自己发出的接口响应」的抓取。
//
// 抖音这类站点已经不给纯 HTTP 客户端返回任何数据：接口要签名，页面 HTML 里
// 只有一段混淆 JS。真正可靠的原生做法就是让浏览器自己去请求，然后把它拿到的
// JSON 截下来——这也是 RSSHub 对抖音的做法。
type JSONCaptureRequest struct {
	// PageURL 是要打开的页面地址。
	PageURL string
	// URLContains 是目标接口 URL 里必须出现的片段，例如 /aweme/v1/web/aweme/post/。
	URLContains string
	// Cookie 是可选的整段 Cookie 头，用于需要登录态的站点。
	Cookie string
	// CookieDomain 是 Cookie 生效域名，例如 .douyin.com。留空时按 PageURL 的域名。
	CookieDomain string
	// UserAgent 留空时使用桌面 Chrome 的默认 UA。
	UserAgent string
	Timeout   time.Duration
	MaxBytes  int
}

// CaptureNetworkJSON 打开一个页面并返回它自己请求到的第一份匹配响应体。
func CaptureNetworkJSON(ctx context.Context, cfg SandboxedBrowserConfig, req JSONCaptureRequest) ([]byte, error) {
	req.PageURL = strings.TrimSpace(req.PageURL)
	req.URLContains = strings.TrimSpace(req.URLContains)
	if req.PageURL == "" || req.URLContains == "" {
		return nil, errors.New("headless capture requires both a page URL and a response URL fragment")
	}
	if err := validateSandboxedBrowserURL(ctx, req.PageURL); err != nil {
		return nil, err
	}
	cfg = sandboxedBrowserConfigWithDefaults(cfg)
	if req.Timeout <= 0 {
		req.Timeout = cfg.Timeout
	}
	if req.MaxBytes <= 0 {
		req.MaxBytes = defaultJSONCaptureBytes
	}
	if strings.TrimSpace(req.UserAgent) == "" {
		req.UserAgent = defaultJSONCaptureUserAgent
	}
	executable, err := findHeadlessBrowserExecutable(cfg.Executable)
	if err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp("", "diana-headless-capture-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(root)
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, err
	}
	profileDir, cacheDir, crashDir := filepath.Join(root, "profile"), filepath.Join(root, "cache"), filepath.Join(root, "crash")
	for _, dir := range []string{profileDir, cacheDir, crashDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	return captureNetworkJSON(ctx, cfg, req, executable, root, profileDir, cacheDir, crashDir)
}

func captureNetworkJSON(ctx context.Context, cfg SandboxedBrowserConfig, req JSONCaptureRequest, executable, root, profileDir, cacheDir, crashDir string) ([]byte, error) {
	runCtx, cancelRun := context.WithTimeout(ctx, req.Timeout)
	defer cancelRun()
	process, err := launchSandboxedChrome(runCtx, executable, root, profileDir, cacheDir, crashDir, cfg)
	if err != nil {
		return nil, err
	}
	defer process.stop()

	allocatorCtx, cancelAllocator := chromedp.NewRemoteAllocator(runCtx, process.wsURL, chromedp.NoModifyURL)
	defer cancelAllocator()
	browserCtx, cancelBrowser := chromedp.NewContext(allocatorCtx)
	defer cancelBrowser()

	captured := make(chan []byte, 1)
	var wanted sync.Map
	chromedp.ListenTarget(browserCtx, func(event any) {
		switch value := event.(type) {
		case *network.EventResponseReceived:
			if strings.Contains(value.Response.URL, req.URLContains) && value.Response.Status >= 200 && value.Response.Status < 300 {
				wanted.Store(value.RequestID, struct{}{})
			}
		case *network.EventLoadingFinished:
			if _, ok := wanted.Load(value.RequestID); !ok {
				return
			}
			go collectCapturedBody(browserCtx, value.RequestID, req.MaxBytes, captured)
		}
	})

	// 第一次 Run 负责建立与远端浏览器的连接，必须用长生命周期的上下文。
	if err := chromedp.Run(browserCtx); err != nil {
		return nil, fmt.Errorf("connect to headless browser CDP: %w", err)
	}
	actions := []chromedp.Action{
		network.Enable(),
		cdppage.Enable(),
		emulation.SetUserAgentOverride(req.UserAgent).WithAcceptLanguage("zh-CN,zh;q=0.9"),
	}
	if cookies := captureCookieParams(req); len(cookies) > 0 {
		actions = append(actions, network.SetCookies(cookies))
	}
	actions = append(actions, chromedp.ActionFunc(func(actionCtx context.Context) error {
		_, _, errorText, isDownload, err := cdppage.Navigate(req.PageURL).Do(actionCtx)
		if err != nil {
			return err
		}
		if isDownload {
			return errors.New("headless capture navigation became a download")
		}
		if errorText != "" {
			return errors.New(errorText)
		}
		return nil
	}))
	if err := chromedp.Run(browserCtx, actions...); err != nil {
		return nil, fmt.Errorf("headless capture setup failed: %w: %s", err, compactBrowserError(process.diagnostics.String()))
	}

	select {
	case body := <-captured:
		return body, nil
	case <-runCtx.Done():
		return nil, fmt.Errorf("在 %s 内没有截获到 %s 的接口响应，站点可能改了接口或触发了风控", req.Timeout, req.URLContains)
	}
}

func collectCapturedBody(ctx context.Context, requestID network.RequestID, maxBytes int, out chan<- []byte) {
	target := chromedp.FromContext(ctx)
	if target == nil || target.Target == nil {
		return
	}
	body, err := network.GetResponseBody(requestID).Do(cdp.WithExecutor(ctx, target.Target))
	if err != nil || len(body) == 0 {
		return
	}
	if len(body) > maxBytes {
		body = body[:maxBytes]
	}
	select {
	case out <- body:
	default:
	}
}

func captureCookieParams(req JSONCaptureRequest) []*network.CookieParam {
	raw := strings.TrimSpace(req.Cookie)
	if raw == "" {
		return nil
	}
	domain := strings.TrimSpace(req.CookieDomain)
	if domain == "" {
		if parsed, err := url.Parse(req.PageURL); err == nil {
			domain = parsed.Hostname()
		}
	}
	if domain == "" {
		return nil
	}
	var params []*network.CookieParam
	for _, pair := range strings.Split(raw, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || name == "" {
			continue
		}
		params = append(params, &network.CookieParam{Name: name, Value: value, Domain: domain, Path: "/"})
	}
	return params
}
