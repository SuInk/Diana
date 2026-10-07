// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/SuInk/diana/model/llm"
	"github.com/SuInk/diana/model/netguard"
)

const defaultScreenshotPath = ".agent-browser/screenshot.png"

// BuiltinBrowserBridge 是一台机器人的内置浏览器句柄，由 model/browserbox.Bot 实现。
//
// 地址每次调用现取，而不是登记工具时定死：用户在 WebUI 里按下接管之后，
// 下一条工具调用就该被拒，而不是等机器人重建工具表。
type BuiltinBrowserBridge interface {
	// Endpoint 返回可用的调试地址。内置浏览器关着时返回空串和 nil，调用方回落到
	// 外部 CDP 地址；开着但还没起来时按需拉起；接管中或起不来时返回的错误会原样
	// 交给模型。
	Endpoint(ctx context.Context) (string, error)
}

// BuiltinBrowserURLPolicy 是内置浏览器可选实现的站点边界：用户在「浏览器」页填的
// denied_hosts。以前只有 WebUI 实时画面那一侧认它，机器人用 browser_open 照样打得开。
type BuiltinBrowserURLPolicy interface {
	AllowsURL(rawURL string) bool
}

// BuiltinBrowserUserTabs 是内置浏览器可选实现的标签归属：主人在 WebUI 画面里自己开的
// 标签归主人，机器人的工具不挑、不列、不切、不关——主人在里面填表、登录，机器人一跳转
// 就全没了。
type BuiltinBrowserUserTabs interface {
	UserTab(targetID string) bool
}

// PersonalBrowserBridge can only resolve the authenticated sender's profile.
// It never connects to the shared owner browser or an external CDP endpoint.
type PersonalBrowserBridge interface {
	Endpoint(context.Context) (string, error)
	CurrentTab() string
	SetCurrentTab(string)
}

type browserToolBase struct {
	root     string
	cdpURL   string
	builtin  BuiltinBrowserBridge
	timeout  time.Duration
	maxChars int
	// session 记着这个对话正在操作哪个标签页。同一个对话的前后几轮共用一份（见
	// Config.BrowserSessionKey），不同对话各用各的：两个群同时让机器人开网页，不会
	// 一个刚打开、另一个就把同一页跳走。
	session      *browserSession
	tabs         *browserTabRegistry
	personal     PersonalBrowserBridge
	allowedHosts []string
}

// defaultScreenshotPath 是没指定 path 时截图落盘的位置，按对话分文件。
//
// 工作目录是所有机器人、所有对话共用的：以前固定写 .agent-browser/screenshot.png，
// A 群刚截完图、正要用 send_attachment 发出去，B 机器人另一个对话的截图就把它覆盖
// 了，发出去的是别人登录态下的页面。
func (b browserToolBase) defaultScreenshotPath() string {
	if b.session == nil || strings.TrimSpace(b.session.key) == "" {
		return defaultScreenshotPath
	}
	sum := sha256.Sum256([]byte(b.session.key))
	return filepath.Join(filepath.Dir(defaultScreenshotPath), "screenshot-"+hex.EncodeToString(sum[:6])+".png")
}

// browserSession 记住一个对话当前操作的标签页。
type browserSession struct {
	mu       sync.Mutex
	key      string
	targetID string
	lastUsed time.Time
	now      func() time.Time
}

func (s *browserSession) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *browserSession) active() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.targetID
}

func (s *browserSession) setActive(id string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.targetID = strings.TrimSpace(id)
	s.lastUsed = s.clock()
	s.mu.Unlock()
}

func (s *browserSession) snapshot() (string, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.targetID, s.lastUsed
}

func (s *browserSession) lastUsedAt() time.Time {
	_, lastUsed := s.snapshot()
	return lastUsed
}

// endpoint 决定这次调用连哪个浏览器。内置浏览器可用时优先用它：它是 Diana
// 自己的浏览器，登录态留在数据目录里，比一个可能根本没开的外部调试端口有用。
// checkURL 在机器人主动打开一个地址之前过一遍用户设的站点黑名单。
func (b browserToolBase) checkURL(pageURL string) error {
	if b.personal != nil {
		if !browserScreenshotHostAllowed(pageURL, b.allowedHosts) {
			return errors.New("网站不在个人浏览器操作白名单内")
		}
		return netguard.ValidatePublicURLStrict(context.Background(), pageURL)
	}
	if err := validateBrowserURL(pageURL); err != nil {
		return err
	}
	if pageURL == "about:blank" {
		return nil
	}
	if policy, ok := b.builtin.(BuiltinBrowserURLPolicy); ok && !policy.AllowsURL(pageURL) {
		return fmt.Errorf("%s 在内置浏览器的禁止名单里，不能打开", pageURL)
	}
	return nil
}

// userTab 判断这个标签是不是主人自己开的。外接 CDP 没有这个概念，一律不是。
func (b browserToolBase) userTab(targetID string) bool {
	owner, ok := b.builtin.(BuiltinBrowserUserTabs)
	return ok && owner.UserTab(targetID)
}

func (b browserToolBase) endpoint(ctx context.Context) (string, error) {
	if b.personal != nil {
		endpoint, err := b.personal.Endpoint(ctx)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(endpoint) == "" {
			return "", errors.New("个人浏览器不可用")
		}
		return strings.TrimRight(endpoint, "/"), nil
	}

	if b.builtin != nil {
		url, err := b.builtin.Endpoint(ctx)
		if err != nil {
			return "", err
		}
		if url = strings.TrimSpace(url); url != "" {
			return strings.TrimRight(url, "/"), nil
		}
	}
	baseURL := strings.TrimRight(strings.TrimSpace(b.cdpURL), "/")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:9222"
	}
	return baseURL, nil
}

type BrowserOpenTool struct {
	base browserToolBase
}

func (t *BrowserOpenTool) Name() string {
	return "browser_open"
}

func (t *BrowserOpenTool) RepeatableCalls() bool { return true }

func (t *BrowserOpenTool) Description() string {
	return `在浏览器里打开网页。`
}

func (t *BrowserOpenTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"url"}, map[string]any{
		"url":     toolStringParam("页面地址"),
		"tab_id":  toolStringParam("个人浏览器标签页 ID，可通过 browser_tabs 查询"),
		"new_tab": toolBoolParam("新标签页打开，默认沿用当前页"),
	})
}

func (t *BrowserOpenTool) Run(ctx context.Context, input map[string]any) (out string, err error) {
	pageURL := stringFromInput(input, "url")
	if err := t.base.checkURL(pageURL); err != nil {
		return "", err
	}
	// 默认沿用当前标签页，和参数说明一致；以前默认开新页，常驻浏览器里的标签页
	// 越攒越多。新开的页也是先开空白页再跳转：/json/new 直接带地址的话，打不开的
	// 网址会让那个页一直转圈，而这边没有任何超时能管到它。
	newTab := boolFromInput(input, "new_tab", false)
	if t.base.personal != nil && t.base.personal.CurrentTab() == "" && stringFromInput(input, "tab_id") == "" {
		newTab = true
	}
	client, err := t.base.openClient(ctx, newTab, stringFromInput(input, "tab_id"))
	if err != nil {
		return "", err
	}
	defer t.base.releaseWith(client, &err, openRecovery(newTab), "打开 "+pageURL+" 后")
	if err := t.base.load(ctx, client, pageURL, newTab); err != nil {
		return "", err
	}
	return t.base.pageSnapshot(ctx, client, "")
}

func openRecovery(newTab bool) pageRecovery {
	if newTab {
		return recoverDiscard
	}
	return recoverBlank
}

// openClient 是要跳转的工具用的 pageClient。沿用的标签页如果已经崩了，先把它换回
// 空白页再连：反正马上要跳走，没必要为此报错让模型重试。
func (b browserToolBase) openClient(ctx context.Context, newTab bool, tabID ...string) (*cdpClient, error) {
	client, err := b.pageClient(ctx, newTab, tabID...)
	if err != nil || newTab {
		return client, err
	}
	if failure := client.pageFailure(); failure != nil && failure.crashed {
		resetCtx, cancel := context.WithTimeout(ctx, browserRecoverTimeout)
		resetTabToBlank(resetCtx, client.wsURL)
		cancel()
		client.Close()
		return b.pageClient(ctx, false, tabID...)
	}
	return client, nil
}

// load 跳转到 pageURL 并等页面落定。
//
// 导航本身按浏览器超时收手（见 navigate）；导航之后的等待和取快照再整体收进一个
// 浏览器超时：页面主线程被脚本卡死时，这之后的 Runtime.evaluate 一条也不会回，
// 以前会一直耗到 Runner 的 60 秒上限。
func (b browserToolBase) load(ctx context.Context, client *cdpClient, pageURL string, newTab bool) error {
	if err := client.navigate(ctx, pageURL); err != nil {
		if newTab && client.pageFailure() == nil {
			b.discardTab(client.baseURL, client.targetID)
		}
		return err
	}
	client.limitFor(b.timeout)
	// Page.navigate 在响应头到了就返回，页面还在加载：直接读会得到一份半截快照。
	// 等到真的跳过去为止。
	if pageURL != "about:blank" {
		_ = client.waitNavigated(ctx)
	} else {
		_ = client.waitReady(ctx)
	}
	if failure := client.pageFailure(); failure != nil {
		return failure
	}
	return nil
}

type BrowserTextTool struct {
	base browserToolBase
}

func (t *BrowserTextTool) Name() string {
	return "browser_text"
}

func (t *BrowserTextTool) Description() string {
	return `读取当前页面文本。`
}

// RepeatableCalls：页面会变，隔一步再读一次同一页不是原地打转。
func (t *BrowserTextTool) RepeatableCalls() bool { return true }

func (t *BrowserTextTool) InputSchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{
		"tab_id":   toolStringParam("个人浏览器标签页 ID，可通过 browser_tabs 查询"),
		"selector": toolStringParam("CSS 选择器，默认整页"),
	})
}

func (t *BrowserTextTool) Run(ctx context.Context, input map[string]any) (out string, err error) {
	client, err := t.base.pageClient(ctx, false, stringFromInput(input, "tab_id"))
	if err != nil {
		return "", err
	}
	defer t.base.release(client, &err)
	if err := t.base.validatePersonalPage(ctx, client); err != nil {
		return "", err
	}
	return t.base.pageSnapshot(ctx, client, stringFromInput(input, "selector"))
}

type BrowserClickTool struct {
	base browserToolBase
}

func (t *BrowserClickTool) Name() string {
	return "browser_click"
}

func (t *BrowserClickTool) Description() string {
	return `点击当前页面：给 selector 点元素，或给截图上的 x/y 坐标。走真实鼠标事件。`
}

func (t *BrowserClickTool) InputSchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{
		"tab_id":      toolStringParam("个人浏览器标签页 ID，可通过 browser_tabs 查询"),
		"selector":    toolStringParam("CSS 选择器"),
		"x":           toolNumberParam("视口 CSS 像素，同截图坐标"),
		"y":           toolNumberParam("视口 CSS 像素"),
		"button":      toolEnumParam("默认 left", "left", "right", "middle"),
		"click_count": toolIntParam("双击填 2"),
	})
}

// RepeatableCalls 见 RepeatableTool：同一个「下一页」按钮连点两次是正常操作。
func (t *BrowserClickTool) RepeatableCalls() bool { return true }

func (t *BrowserClickTool) Run(ctx context.Context, input map[string]any) (out string, err error) {
	selector := stringFromInput(input, "selector")
	x, hasX := numberFromInput(input, "x")
	y, hasY := numberFromInput(input, "y")
	if selector == "" && !(hasX && hasY) {
		return "", errors.New("selector or x/y is required")
	}
	button := strings.ToLower(stringFromInput(input, "button"))
	switch button {
	case "":
		button = "left"
	case "left", "right", "middle":
	default:
		return "", fmt.Errorf("unsupported button %q", button)
	}
	clicks := intFromInput(input, "click_count", 1)
	if clicks < 1 || clicks > 3 {
		clicks = 1
	}
	client, err := t.base.pageClient(ctx, false, stringFromInput(input, "tab_id"))
	if err != nil {
		return "", err
	}
	defer t.base.release(client, &err)
	if err := t.base.validatePersonalPage(ctx, client); err != nil {
		return "", err
	}
	if selector != "" {
		// 先滚到视口中间再取坐标。点击点被别的元素盖住（弹层、吸顶栏）时真实点击
		// 会落到盖着的那个元素上，这种情况退回 el.click()，保证点到的是要点的元素。
		expr := fmt.Sprintf(`(() => {
const el = document.querySelector(%s);
if (!el) return {ok:false, error:"selector not found"};
el.scrollIntoView({block:"center", inline:"center"});
const r = el.getBoundingClientRect();
const x = r.left + r.width / 2, y = r.top + r.height / 2;
const hit = document.elementFromPoint(x, y);
const reachable = r.width > 0 && r.height > 0 && hit && (hit === el || el.contains(hit) || hit.contains(el));
if (!reachable) { el.click(); return {ok:true, synthetic:true}; }
return {ok:true, x, y};
})()`, jsString(selector))
		raw, err := client.evaluate(ctx, t.base.personalExpression(expr))
		if err != nil {
			return "", err
		}
		var located struct {
			OK        bool    `json:"ok"`
			Error     string  `json:"error"`
			Synthetic bool    `json:"synthetic"`
			X         float64 `json:"x"`
			Y         float64 `json:"y"`
		}
		if err := json.Unmarshal(raw, &located); err != nil {
			return "", err
		}
		if !located.OK {
			return string(raw), nil
		}
		if located.Synthetic {
			if err := t.base.validatePersonalPage(ctx, client); err != nil {
				return "", err
			}
			return client.pageState(ctx, map[string]any{"clicked": selector, "synthetic": true})
		}
		x, y = located.X, located.Y
	}
	if err := client.mouseClick(ctx, x, y, button, clicks); err != nil {
		return "", err
	}
	// 点击可能触发跳转，给页面一点时间开始换文档，读到的地址和标题才是点完之后的。
	client.settle(ctx)
	result := map[string]any{"clicked_at": map[string]float64{"x": x, "y": y}}
	if selector != "" {
		result["clicked"] = selector
	}
	if err := t.base.validatePersonalPage(ctx, client); err != nil {
		return "", err
	}
	return client.pageState(ctx, result)
}

type BrowserTypeTool struct {
	base browserToolBase
}

func (t *BrowserTypeTool) Name() string {
	return "browser_type"
}

func (t *BrowserTypeTool) Description() string {
	return `向当前页面元素输入文本，走真实键盘输入，受控输入框也认。`
}

func (t *BrowserTypeTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"text"}, map[string]any{
		"tab_id":      toolStringParam("个人浏览器标签页 ID，可通过 browser_tabs 查询"),
		"selector":    toolStringParam("输入框 CSS 选择器，默认当前焦点"),
		"text":        toolStringParam(""),
		"clear":       toolBoolParam("先清空原内容，默认 true"),
		"press_enter": toolBoolParam("输入后回车"),
	})
}

func (t *BrowserTypeTool) RepeatableCalls() bool { return true }

func (t *BrowserTypeTool) Run(ctx context.Context, input map[string]any) (out string, err error) {
	selector := stringFromInput(input, "selector")
	text := rawStringFromInput(input, "text")
	client, err := t.base.pageClient(ctx, false, stringFromInput(input, "tab_id"))
	if err != nil {
		return "", err
	}
	defer t.base.release(client, &err)
	if err := t.base.validatePersonalPage(ctx, client); err != nil {
		return "", err
	}
	// 聚焦和清空在页面里做，文字本身用 Input.insertText 送进去：直接改 value 的话
	// React、Vue 这类受控输入框不认，页面上看着填了，提交出去还是空的。清空用原生
	// setter 而不是 el.value = ""，同样是为了让框架的值追踪器看到这次变化。
	expr := fmt.Sprintf(`(() => {
const selector = %s;
const el = selector ? document.querySelector(selector) : document.activeElement;
if (!el) return {ok:false, error: selector ? "selector not found" : "no focused element"};
el.scrollIntoView({block:"center", inline:"center"});
el.focus();
if (%t) {
  if (el.isContentEditable) {
    const range = document.createRange();
    range.selectNodeContents(el);
    const sel = window.getSelection();
    sel.removeAllRanges();
    sel.addRange(range);
    document.execCommand("delete");
  } else if ("value" in el) {
    const proto = Object.getPrototypeOf(el);
    const setter = Object.getOwnPropertyDescriptor(proto, "value")?.set;
    if (setter) setter.call(el, ""); else el.value = "";
    el.dispatchEvent(new Event("input", {bubbles:true}));
  }
} else if ("value" in el && typeof el.setSelectionRange === "function") {
  try { const n = (el.value || "").length; el.setSelectionRange(n, n); } catch (_) {}
}
return {ok:true};
})()`, jsString(selector), boolFromInput(input, "clear", true))
	raw, err := client.evaluate(ctx, t.base.personalExpression(expr))
	if err != nil {
		return "", err
	}
	var focused struct {
		OK bool `json:"ok"`
	}
	if json.Unmarshal(raw, &focused) != nil || !focused.OK {
		return string(raw), nil
	}
	if text != "" {
		if err := client.call(ctx, "Input.insertText", map[string]any{"text": text}, nil); err != nil {
			return "", err
		}
	}
	if boolFromInput(input, "press_enter", false) {
		if err := client.pressKey(ctx, "Enter"); err != nil {
			return "", err
		}
		client.settle(ctx)
	}
	result := map[string]any{"typed_chars": utf8.RuneCountInString(text)}
	if selector != "" {
		result["selector"] = selector
	}
	if err := t.base.validatePersonalPage(ctx, client); err != nil {
		return "", err
	}
	return client.pageState(ctx, result)
}

type BrowserScreenshotTool struct {
	base         browserToolBase
	restricted   bool
	allowedHosts []string
	protected    protectedFiles
	mu           sync.Mutex
	parts        []llm.ContentPart
}

func (t *BrowserScreenshotTool) RepeatableCalls() bool { return true }

func (t *BrowserScreenshotTool) Name() string { return "browser_screenshot" }

func (t *BrowserScreenshotTool) Description() string {
	return `截取已打开的登录浏览器页面并附加真实画面。url 必须是已打开标签页的完整地址，不会打开、点击或输入网页；公开网页请用 webpage_screenshot。多个页面时必须明确指定 url 或 tab_id。`
}

func (t *BrowserScreenshotTool) InputSchema() map[string]any {
	props := map[string]any{
		"url":    toolStringParam("已打开标签页的完整 URL；非主人必填，只允许授权网站"),
		"tab_id": toolStringParam("已知标签页 ID，用于区分相同 URL 的多个页面；不指定时必须唯一匹配"),
	}
	var required []string
	if t.restricted {
		required = []string{"url"}
	} else {
		props["path"] = toolStringParam("工作目录内的相对保存路径，省略时使用默认文件名")
	}
	return toolObjectSchema(required, props)
}

func (t *BrowserScreenshotTool) Run(ctx context.Context, input map[string]any) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.parts = nil
	outPath := stringFromInput(input, "path")
	if t.restricted && outPath != "" {
		return "", errors.New("非主人不能指定截图文件路径")
	}
	var path string
	if !t.restricted {
		if outPath == "" {
			outPath = t.base.defaultScreenshotPath()
		}
		var err error
		path, err = safePath(t.base.root, outPath)
		if err != nil {
			return "", err
		}
		if t.protected.blocked(path) {
			return "", errors.New("截图不能覆盖受保护的配置文件")
		}
	}
	requestedURL := stringFromInput(input, "url")
	if t.restricted && !browserScreenshotHostAllowed(requestedURL, t.allowedHosts) {
		return "", errors.New("请明确指定授权网站上已打开页面的完整 HTTP(S) URL")
	}
	ctx, cancel := context.WithTimeout(ctx, t.base.timeout)
	defer cancel()
	endpoint, err := t.base.endpoint(ctx)
	if err != nil {
		return "", err
	}
	targets, err := listBrowserTargets(ctx, endpoint)
	if err != nil {
		return "", err
	}
	var target browserTarget
	if t.base.personal == nil && requestedURL == "" && stringFromInput(input, "tab_id") == "" {
		target, err = t.base.pickTarget(ctx, endpoint, false)
	} else {
		filtered := []browserTarget{}
		for _, candidate := range targets {
			if !t.base.userTab(candidate.ID) && (t.base.personal != nil || !t.base.tabRegistry().heldByOther(candidate.ID, t.base.session)) {
				filtered = append(filtered, candidate)
			}
		}
		target, err = screenshotTarget(filtered, requestedURL, stringFromInput(input, "tab_id"))
	}
	if err != nil {
		return "", err
	}
	if err := t.base.checkURL(target.URL); err != nil {
		return "", err
	}
	client, err := newCDPClient(ctx, target.WebSocketDebuggerURL, t.base.timeout)
	if err != nil {
		return "", err
	}
	defer client.Close()
	if err := client.call(ctx, "Page.enable", nil, nil); err != nil {
		return "", err
	}
	var before screenshotFrameTree
	if err := client.call(ctx, "Page.getFrameTree", nil, &before); err != nil {
		return "", err
	}
	if before.FrameTree.Frame.URL != target.URL {
		return "", errors.New("页面已切换，请重新指定截图页面")
	}
	if t.restricted && (!before.FrameTree.allowed(t.allowedHosts) || (t.base.personal != nil && !before.FrameTree.allowed(t.base.allowedHosts))) {
		return "", errors.New("页面或嵌入页面不在截图网站授权范围内")
	}
	client.navigationObserved.Store(false)
	var result struct {
		Data string `json:"data"`
	}
	if err := client.call(ctx, "Page.captureScreenshot", map[string]any{"format": "png", "fromSurface": true}, &result); err != nil {
		return "", err
	}
	var after screenshotFrameTree
	if err := client.call(ctx, "Page.getFrameTree", nil, &after); err != nil {
		return "", err
	}
	if client.navigationObserved.Load() || before.FrameTree.Frame.URL != after.FrameTree.Frame.URL || (t.restricted && (!after.FrameTree.allowed(t.allowedHosts) || (t.base.personal != nil && !after.FrameTree.allowed(t.base.allowedHosts)))) {
		return "", errors.New("截图期间页面发生跳转或授权范围变化，请重试")
	}
	// A human may take over while the capture is in flight. Do not expose that image.
	if currentEndpoint, err := t.base.endpoint(ctx); err != nil {
		return "", err
	} else if currentEndpoint != endpoint {
		return "", errors.New("浏览器连接已变化，请重试截图")
	}
	data, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil {
		return "", err
	}
	part, err := screenshotImagePart(data)
	if err != nil {
		return "", err
	}
	body := map[string]any{"url": after.FrameTree.Frame.URL, "tab_id": target.ID, "bytes": len(data)}
	// Members receive only these captured bytes, never access to a workspace path.
	if !t.restricted {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return "", err
		}
		body["path"] = relPathForOutput(t.base.root, path)
	}
	output, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	t.parts = []llm.ContentPart{part}
	return string(output), nil
}

func (t *BrowserScreenshotTool) ToolResultParts(string) []llm.ContentPart {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]llm.ContentPart(nil), t.parts...)
}

func screenshotTarget(targets []browserTarget, rawURL, tabID string) (browserTarget, error) {
	var matches []browserTarget
	for _, target := range targets {
		if target.Type != "page" || target.WebSocketDebuggerURL == "" || isBlankBrowserTarget(target.URL) {
			continue
		}
		if rawURL != "" && target.URL != rawURL {
			continue
		}
		if tabID != "" && target.ID != tabID {
			continue
		}
		matches = append(matches, target)
	}
	if len(matches) != 1 {
		return browserTarget{}, errors.New("截图页面未找到或有多个匹配，请明确指定已打开页面的完整 url 和必要的 tab_id")
	}
	return matches[0], nil
}

type screenshotFrameTree struct {
	FrameTree screenshotFrame `json:"frameTree"`
}
type screenshotFrame struct {
	Frame struct {
		URL string `json:"url"`
	} `json:"frame"`
	ChildFrames []screenshotFrame `json:"childFrames"`
}

func (frame screenshotFrame) allowed(hosts []string) bool {
	if !browserScreenshotHostAllowed(frame.Frame.URL, hosts) {
		return false
	}
	for _, child := range frame.ChildFrames {
		if !child.allowed(hosts) {
			return false
		}
	}
	return true
}

func (b browserToolBase) pageSnapshot(ctx context.Context, client *cdpClient, selector string) (string, error) {
	if err := b.validatePersonalPage(ctx, client); err != nil {
		return "", err
	}
	selectorExpr := "null"
	if selector != "" {
		selectorExpr = jsString(selector)
	}
	expr := fmt.Sprintf(`(() => {
const selector = %s;
const el = selector ? document.querySelector(selector) : document.body;
const text = el ? (el.innerText || el.textContent || "") : "";
return {
  url: location.href,
  title: document.title,
  selector,
  text: text.length > %d ? text.slice(0, %d) : text,
  truncated: text.length > %d
};
})()`, selectorExpr, b.maxChars, b.maxChars, b.maxChars)
	client.navigationObserved.Store(false)
	raw, err := client.evaluate(ctx, b.personalExpression(expr))
	if err != nil {
		return "", err
	}
	if err := b.validatePersonalPage(ctx, client); err != nil {
		return "", err
	}
	if b.personal != nil && client.navigationObserved.Load() {
		return "", errors.New("读取期间页面发生跳转，请重读授权页面")
	}
	return string(raw), nil
}

func (b browserToolBase) pageClient(ctx context.Context, newTab bool, tabID ...string) (*cdpClient, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	baseURL, err := b.endpoint(ctx)
	if err != nil {
		return nil, err
	}
	var target browserTarget
	if b.personal != nil {
		id := ""
		if len(tabID) > 0 {
			id = tabID[0]
		}
		target, err = b.pickPersonalOrOwnerTarget(ctx, baseURL, "", newTab, id)
	} else {
		target, err = b.pickTarget(ctx, baseURL, newTab)
	}
	if err != nil {
		return nil, err
	}
	if target.WebSocketDebuggerURL == "" {
		return nil, errors.New("browser target has no websocket debugger URL")
	}
	client, err := newCDPClient(ctx, target.WebSocketDebuggerURL, b.timeout)
	if err != nil {
		return nil, err
	}
	client.baseURL = baseURL
	client.targetID = target.ID
	b.session.setActive(target.ID)
	b.tabRegistry().touch(target.ID)
	// Inspector 域在浏览器进程里处理，页面卡死也照样回；对已经崩掉的标签页，它一开
	// 就先推一条 Inspector.targetCrashed，这条会话随即记上「崩溃」。Page、Runtime
	// 两条要渲染进程回，只发不等：页面卡死时等它们，每个工具都要先白等一个浏览器超时。
	_ = client.call(ctx, "Inspector.enable", nil, nil)
	client.send("Page.enable")
	client.send("Runtime.enable")
	return client, nil
}

func (b browserToolBase) pickTarget(ctx context.Context, baseURL string, newTab bool) (browserTarget, error) {
	if newTab {
		return b.openTab(ctx, baseURL)
	}
	targets, err := listBrowserTargets(ctx, baseURL)
	if err != nil {
		return browserTarget{}, err
	}
	// 模型切过或打开过的那个标签页优先；它被关掉了才退回下面的自动挑选。
	if active := b.session.active(); active != "" {
		for _, target := range targets {
			if target.ID == active && target.Type == "page" && target.WebSocketDebuggerURL != "" && !b.userTab(target.ID) {
				return target, nil
			}
		}
	}
	// 挑一个真的载着网页的标签页。一次性浏览器里通常只有一个标签页，随便挑都对；
	// 内置浏览器是常驻的，开机那个 about:blank 会一直排在列表里，照单全收的话
	// browser_text 读到的永远是空白页——刚 browser_open 打开的那一页反而读不到。
	// 别的对话正在用的页不挑：挑到了，这边一跳转，那边读到的就是这边的页面。
	registry := b.tabRegistry()
	var fallback browserTarget
	for _, target := range targets {
		if target.Type != "page" || target.WebSocketDebuggerURL == "" {
			continue
		}
		if registry.heldByOther(target.ID, b.session) || b.userTab(target.ID) {
			continue
		}
		if isBlankBrowserTarget(target.URL) {
			if fallback.WebSocketDebuggerURL == "" {
				fallback = target
			}
			continue
		}
		return target, nil
	}
	if fallback.WebSocketDebuggerURL != "" {
		return fallback, nil
	}
	return b.openTab(ctx, baseURL)
}

// isBlankBrowserTarget 判断一个标签页是不是「还没装东西」的那种：新标签页、
// 空白页和浏览器自己的内部页都算。
func isBlankBrowserTarget(rawURL string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(rawURL))
	switch {
	case trimmed == "", trimmed == "about:blank":
		return true
	case strings.HasPrefix(trimmed, "chrome://"), strings.HasPrefix(trimmed, "edge://"),
		strings.HasPrefix(trimmed, "devtools://"), strings.HasPrefix(trimmed, "chrome-extension://"):
		return true
	}
	return false
}

type browserTarget struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	Title                string `json:"title"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

func listBrowserTargets(ctx context.Context, baseURL string) ([]browserTarget, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/json/list", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, browserConnectError(baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("browser cdp list failed: HTTP %d", resp.StatusCode)
	}
	var targets []browserTarget
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return nil, err
	}
	return targets, nil
}

func newBrowserTarget(ctx context.Context, baseURL, pageURL string) (browserTarget, error) {
	endpoint := baseURL + "/json/new?" + url.QueryEscape(pageURL)
	for _, method := range []string{http.MethodPut, http.MethodGet} {
		req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
		if err != nil {
			return browserTarget{}, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return browserTarget{}, browserConnectError(baseURL, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusMethodNotAllowed && method == http.MethodPut {
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return browserTarget{}, fmt.Errorf("browser cdp new target failed: HTTP %d", resp.StatusCode)
		}
		var target browserTarget
		if err := json.NewDecoder(resp.Body).Decode(&target); err != nil {
			return browserTarget{}, err
		}
		return target, nil
	}
	return browserTarget{}, errors.New("browser cdp new target failed")
}

func browserConnectError(baseURL string, err error) error {
	return fmt.Errorf("browser cdp is unavailable at %s: %w; start Chrome with --remote-debugging-port=9222 or configure DIANA_AGENT_BROWSER_CDP_URL", baseURL, err)
}

// compactCDPValue 把 Chrome 交回的 JSON 重新编码一遍。Chrome 把非 ASCII 字符一律写成
// \uXXXX，一页中文读回来每个字变成六个字符，模型那边既难读又多花几倍 token。
func compactCDPValue(raw json.RawMessage) json.RawMessage {
	// UseNumber：页面里的大整数 ID 走一趟 float64 会丢精度。
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return raw
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return raw
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func (c *cdpClient) waitReady(ctx context.Context) error {
	// 页面自己的定时器兜底：load 一直不来也按时回话。整体期限（见 limitFor）快到时
	// 缩短它，留出页面回话的时间——否则一个好好的页面会因为还在等 load 被当成卡死。
	wait := 3 * time.Second
	if remaining := c.remaining() - minCDPCallWait; remaining < wait {
		wait = max(remaining, 0)
	}
	_, err := c.evaluate(ctx, fmt.Sprintf(`new Promise(resolve => {
if (document.readyState === "complete") { resolve(true); return; }
const done = () => resolve(true);
window.addEventListener("load", done, {once:true});
setTimeout(done, %d);
})`, wait.Milliseconds()))
	return err
}

// waitNavigated 等页面真的离开 about:blank 并加载完。轮询而不是监听事件：
// 这条客户端只做请求响应，加事件订阅要改的不止一处，而这里的等待窗口很短。
//
// 响应头回来了、正文却一直传不完的页面，等满窗口之后停止加载：已经到手的内容照样
// 能读，标签页也不会在后台一直转圈。
func (c *cdpClient) waitNavigated(ctx context.Context) error {
	deadline := time.Now().Add(8 * time.Second)
	if limit := time.Now().Add(c.remaining() - minCDPCallWait); limit.Before(deadline) {
		deadline = limit
	}
	for time.Now().Before(deadline) {
		value, err := c.evaluate(ctx, `(() => location.href !== "about:blank" && document.readyState !== "loading")()`)
		if err != nil {
			return err
		}
		var ready bool
		if json.Unmarshal(value, &ready) == nil && ready {
			// 文档已经换过去了，再等一次 load 让首屏内容落定。
			return c.waitReady(ctx)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	return c.call(ctx, "Page.stopLoading", nil, nil)
}

func validateBrowserURL(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("url is required")
	}
	if value == "about:blank" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("only http and https browser URLs are allowed")
	}
	if parsed.Host == "" {
		return errors.New("browser URL host is required")
	}
	return nil
}

func jsString(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (b browserToolBase) validatePersonalPage(ctx context.Context, client *cdpClient) error {
	if b.personal == nil {
		return nil
	}
	var frames screenshotFrameTree
	if err := client.call(ctx, "Page.getFrameTree", nil, &frames); err != nil {
		return err
	}
	if !frames.FrameTree.allowed(b.allowedHosts) {
		return errors.New("页面或嵌入页面不在个人浏览器操作白名单内")
	}
	return nil
}

func (b browserToolBase) pickPersonalOrOwnerTarget(ctx context.Context, endpoint, pageURL string, newTab bool, tabID string) (browserTarget, error) {
	if b.personal == nil {
		return b.pickTarget(ctx, endpoint, newTab)
	}
	if newTab || (pageURL != "" && b.personal.CurrentTab() == "" && tabID == "") {
		target, err := newBrowserTarget(ctx, endpoint, "about:blank")
		if err == nil {
			b.personal.SetCurrentTab(target.ID)
		}
		return target, err
	}
	if tabID == "" {
		tabID = b.personal.CurrentTab()
	}
	targets, err := listBrowserTargets(ctx, endpoint)
	if err != nil {
		return browserTarget{}, err
	}
	for _, target := range targets {
		if target.Type == "page" && target.ID == tabID && target.WebSocketDebuggerURL != "" {
			if pageURL == "" && !browserScreenshotHostAllowed(target.URL, b.allowedHosts) {
				return browserTarget{}, errors.New("请先打开白名单网站，或指定自己的有效标签页")
			}
			b.personal.SetCurrentTab(target.ID)
			return target, nil
		}
	}
	return browserTarget{}, errors.New("个人浏览器标签页不存在，请用 browser_open 打开网站或 browser_tabs 查找登录弹窗")
}

type PersonalBrowserTabsTool struct{ base browserToolBase }

func (t *PersonalBrowserTabsTool) Name() string { return "browser_tabs" }
func (t *PersonalBrowserTabsTool) Description() string {
	return "列出本人浏览器中白名单网站的标签页，用于查找登录跳转或弹窗；返回的 tab_id 可用于打开、读取、点击、输入及截图。"
}
func (t *PersonalBrowserTabsTool) InputSchema() map[string]any { return toolObjectSchema(nil, nil) }
func (t *PersonalBrowserTabsTool) Run(ctx context.Context, input map[string]any) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, t.base.timeout)
	defer cancel()
	endpoint, err := t.base.endpoint(ctx)
	if err != nil {
		return "", err
	}
	targets, err := listBrowserTargets(ctx, endpoint)
	if err != nil {
		return "", err
	}
	result := []map[string]string{}
	for _, target := range targets {
		if target.Type != "page" || !browserScreenshotHostAllowed(target.URL, t.base.allowedHosts) {
			continue
		}
		result = append(result, map[string]string{"tab_id": target.ID, "url": target.URL, "title": target.Title})
	}
	data, err := json.Marshal(result)
	return string(data), err
}

// Check location inside the same JavaScript evaluation, so a navigation between
// the frame-tree check and evaluation cannot expose a different page's DOM.
func (b browserToolBase) personalExpression(expr string) string {
	if b.personal == nil {
		return expr
	}
	hosts, _ := json.Marshal(b.allowedHosts)
	return fmt.Sprintf(`(() => {
 const allowed = %s;
 if (!['http:','https:'].includes(location.protocol) || !allowed.includes(location.host.toLowerCase())) throw new Error("页面不在个人浏览器白名单内");
 return (%s);
 })()`, hosts, expr)
}
