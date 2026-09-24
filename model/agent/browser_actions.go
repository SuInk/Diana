// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// 交互式浏览器的第二批动作：标签页、滚动、按键、前进后退、下拉框、等待和执行脚本。
//
// 这组工具只登记给主人（群成员的工具面里只有一次性无头的 browser_render，见
// RelationshipPolicy.allowedAgentToolNames），连的是带着主人登录态的浏览器，所以
// 不再设「只读」「不许执行脚本」这类档位：主人在自己浏览器里能做的，机器人替他也
// 能做。每一步照样进操作记录（browser_action），事后查得清。

const (
	defaultBrowserWaitMS = 5_000
	maxBrowserWaitMS     = 30_000
	maxBrowserKeyRepeat  = 20
)

// RepeatableTool 标记「连续两次相同调用不算重复」的工具。
//
// Runner 默认跳过和上一次一模一样的调用，防止模型原地打转；但浏览器这组的结果取决
// 于页面当时的状态：连按两次 PageDown、连点两次「下一页」、隔一会儿再读一次页面，
// 参数一样，结果不一样。
type RepeatableTool interface {
	Tool
	RepeatableCalls() bool
}

func toolNumberParam(description string) map[string]any {
	return map[string]any{"type": "number", "description": description}
}

// numberFromInput 读一个数值参数，第二个返回值说明调用方到底传没传。
func numberFromInput(input map[string]any, key string) (float64, bool) {
	if input == nil {
		return 0, false
	}
	switch value := input[key].(type) {
	case float64:
		return value, true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case json.Number:
		parsed, err := value.Float64()
		return parsed, err == nil
	}
	return 0, false
}

// ---- cdpClient 上的真实输入 ----

// navigate 跳转并把加载失败（域名解析不到、连接被拒）当成错误交回去。Page.navigate
// 在这种情况下是「调用成功、结果里带 errorText」，不检查的话模型会读到一张错误页。
//
// Page.navigate 要等到响应头回来才返回。服务器一直不回的话它就一直挂着，所以这里
// 按浏览器超时（机器人配置的 agent_browser_timeout_ms）给它一个硬期限；到期或者
// 任务被取消，就补发 Page.stopLoading 把这次加载掐掉，不让标签页在后台一直转圈。
func (c *cdpClient) navigate(ctx context.Context, pageURL string) error {
	timeout := c.timeout
	if timeout <= 0 {
		timeout = DefaultBrowserTimeoutMS * time.Millisecond
	}
	navCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var result struct {
		ErrorText string `json:"errorText"`
	}
	if err := c.call(navCtx, "Page.navigate", map[string]any{"url": pageURL}, &result); err != nil {
		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			return err
		}
		c.abortLoading()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("打开 %s 时任务已结束，已停止加载：%w", pageURL, ctxErr)
		}
		return &browserTimeoutError{message: fmt.Sprintf("打开 %s 超时：%s 内页面没有响应，已停止加载。这个网址可能打不开或响应太慢，换个地址或稍后再试", pageURL, timeout)}
	}
	if text := strings.TrimSpace(result.ErrorText); text != "" {
		return fmt.Errorf("打开 %s 失败：%s", pageURL, text)
	}
	return nil
}

// abortLoading 另开一条连接补发 Page.stopLoading。原来那条连接刚因为超时被拨了读期限，
// 已经不能再用；调用方的 ctx 也已经结束，所以用自己的短期限。
func (c *cdpClient) abortLoading() {
	if c == nil || c.wsURL == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), browserAbortTimeout)
	defer cancel()
	fresh, err := newCDPClient(ctx, c.wsURL, browserAbortTimeout)
	if err != nil {
		return
	}
	defer fresh.Close()
	_ = fresh.call(ctx, "Page.stopLoading", nil, nil)
}

// mouseClick 在视口坐标上按真实鼠标事件点击。el.click() 发出去的事件 isTrusted 为
// false，不少站点的按钮只认真实点击。
func (c *cdpClient) mouseClick(ctx context.Context, x, y float64, button string, clicks int) error {
	if err := c.call(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": x, "y": y}, nil); err != nil {
		return err
	}
	for i := 1; i <= clicks; i++ {
		for _, kind := range []string{"mousePressed", "mouseReleased"} {
			if err := c.call(ctx, "Input.dispatchMouseEvent", map[string]any{
				"type": kind, "x": x, "y": y, "button": button, "clickCount": i,
			}, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

type browserKey struct {
	key     string
	code    string
	keyCode int
	text    string
}

var namedBrowserKeys = map[string]browserKey{
	"enter":      {"Enter", "Enter", 13, "\r"},
	"tab":        {"Tab", "Tab", 9, ""},
	"escape":     {"Escape", "Escape", 27, ""},
	"esc":        {"Escape", "Escape", 27, ""},
	"backspace":  {"Backspace", "Backspace", 8, ""},
	"delete":     {"Delete", "Delete", 46, ""},
	"space":      {" ", "Space", 32, " "},
	"arrowup":    {"ArrowUp", "ArrowUp", 38, ""},
	"arrowdown":  {"ArrowDown", "ArrowDown", 40, ""},
	"arrowleft":  {"ArrowLeft", "ArrowLeft", 37, ""},
	"arrowright": {"ArrowRight", "ArrowRight", 39, ""},
	"up":         {"ArrowUp", "ArrowUp", 38, ""},
	"down":       {"ArrowDown", "ArrowDown", 40, ""},
	"left":       {"ArrowLeft", "ArrowLeft", 37, ""},
	"right":      {"ArrowRight", "ArrowRight", 39, ""},
	"home":       {"Home", "Home", 36, ""},
	"end":        {"End", "End", 35, ""},
	"pageup":     {"PageUp", "PageUp", 33, ""},
	"pagedown":   {"PageDown", "PageDown", 34, ""},
}

var browserKeyModifiers = map[string]int{
	"alt": 1, "option": 1,
	"ctrl": 2, "control": 2,
	"meta": 4, "cmd": 4, "command": 4,
	"shift": 8,
}

// parseBrowserKey 把 "Enter"、"PageDown"、"Control+A"、"a" 这类写法拆成按键和修饰键位。
func parseBrowserKey(spec string) (browserKey, int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return browserKey{}, 0, errors.New("key is required")
	}
	parts := strings.Split(spec, "+")
	// "Control++" 这种写法里最后一段是空的，按的是加号本身。
	if strings.HasSuffix(spec, "++") {
		parts = append(parts[:len(parts)-2], "+")
	}
	modifiers := 0
	for _, part := range parts[:len(parts)-1] {
		bit, ok := browserKeyModifiers[strings.ToLower(strings.TrimSpace(part))]
		if !ok {
			return browserKey{}, 0, fmt.Errorf("unknown modifier %q", part)
		}
		modifiers |= bit
	}
	name := strings.TrimSpace(parts[len(parts)-1])
	if name == "" {
		name = parts[len(parts)-1]
	}
	if key, ok := namedBrowserKeys[strings.ToLower(name)]; ok {
		return key, modifiers, nil
	}
	if utf8.RuneCountInString(name) != 1 {
		return browserKey{}, 0, fmt.Errorf("unknown key %q", name)
	}
	r, _ := utf8.DecodeRuneInString(name)
	key := browserKey{key: name, text: name}
	switch {
	case r >= 'a' && r <= 'z':
		key.code = "Key" + strings.ToUpper(name)
		key.keyCode = int(r - 'a' + 'A')
	case r >= 'A' && r <= 'Z':
		key.code = "Key" + name
		key.keyCode = int(r)
	case r >= '0' && r <= '9':
		key.code = "Digit" + name
		key.keyCode = int(r)
	}
	return key, modifiers, nil
}

// pressKey 按一次键，走 Input.dispatchKeyEvent，和真人按键同一条输入管线。
func (c *cdpClient) pressKey(ctx context.Context, spec string) error {
	key, modifiers, err := parseBrowserKey(spec)
	if err != nil {
		return err
	}
	text := key.text
	// 带 Ctrl/Alt/Meta 的组合键是快捷键，不该往输入框里打出字符。
	if modifiers&(1|2|4) != 0 {
		text = ""
	}
	down := map[string]any{
		"type":                  "rawKeyDown",
		"key":                   key.key,
		"code":                  key.code,
		"windowsVirtualKeyCode": key.keyCode,
		"modifiers":             modifiers,
	}
	if text != "" {
		down["type"] = "keyDown"
		down["text"] = text
		down["unmodifiedText"] = text
	}
	if err := c.call(ctx, "Input.dispatchKeyEvent", down, nil); err != nil {
		return err
	}
	return c.call(ctx, "Input.dispatchKeyEvent", map[string]any{
		"type":                  "keyUp",
		"key":                   key.key,
		"code":                  key.code,
		"windowsVirtualKeyCode": key.keyCode,
		"modifiers":             modifiers,
	}, nil)
}

// settle 在一次可能引起跳转的输入之后等页面落定：先给一小段时间让跳转开始，再等到
// 文档不在 loading。跳转途中执行上下文会被销毁，这时的报错不算失败。
func (c *cdpClient) settle(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(250 * time.Millisecond):
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := c.evaluate(ctx, `document.readyState`)
		var state string
		if err == nil && json.Unmarshal(raw, &state) == nil && state != "loading" {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// pageState 把动作结果和页面当前地址、标题拼在一起交回模型。
func (c *cdpClient) pageState(ctx context.Context, extra map[string]any) (string, error) {
	result := map[string]any{"ok": true}
	for key, value := range extra {
		result[key] = value
	}
	if raw, err := c.evaluate(ctx, `({url: location.href, title: document.title})`); err == nil {
		var page struct {
			URL   string `json:"url"`
			Title string `json:"title"`
		}
		if json.Unmarshal(raw, &page) == nil {
			result["url"] = page.URL
			result["title"] = page.Title
		}
	}
	body, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// evaluateScript 和 evaluate 的区别是把页面里抛出的异常当错误交回去，而不是返回 null。
func (c *cdpClient) evaluateScript(ctx context.Context, expression string) (json.RawMessage, error) {
	var out struct {
		Result struct {
			Type        string          `json:"type"`
			Value       json.RawMessage `json:"value"`
			Description string          `json:"description"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := c.call(ctx, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"awaitPromise":  true,
		"returnByValue": true,
		"userGesture":   true,
	}, &out); err != nil {
		return nil, err
	}
	if details := out.ExceptionDetails; details != nil {
		message := strings.TrimSpace(details.Text)
		if details.Exception != nil && strings.TrimSpace(details.Exception.Description) != "" {
			message = strings.TrimSpace(details.Exception.Description)
		}
		return nil, fmt.Errorf("脚本抛出异常：%s", message)
	}
	if len(out.Result.Value) == 0 {
		if out.Result.Type == "undefined" || out.Result.Description == "" {
			return []byte("null"), nil
		}
		// 函数、Symbol 这类不能按值返回的结果，给个描述比给个 null 有用。
		return json.Marshal(out.Result.Description)
	}
	return compactCDPValue(out.Result.Value), nil
}

// ---- 标签页 ----

type BrowserTabsTool struct {
	base browserToolBase
}

func (t *BrowserTabsTool) Name() string { return "browser_tabs" }

func (t *BrowserTabsTool) Description() string {
	return `管理浏览器标签页：列出、切换、关闭、新开。切换之后其他浏览器工具都作用在那一页上。`
}

func (t *BrowserTabsTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"action"}, map[string]any{
		"action": toolEnumParam("list 列出；switch 切换到 tab_id；close 关闭 tab_id（省略时关当前页）；new 新开一页", "list", "switch", "close", "new"),
		"tab_id": toolStringParam("标签页 ID，来自 list 的结果"),
		"url":    toolStringParam("new 时要打开的地址，省略时是空白页"),
	})
}

func (t *BrowserTabsTool) RepeatableCalls() bool { return true }

func (t *BrowserTabsTool) Run(ctx context.Context, input map[string]any) (string, error) {
	action := strings.ToLower(stringFromInput(input, "action"))
	tabID := stringFromInput(input, "tab_id")
	callCtx, cancel := context.WithTimeout(ctx, t.base.timeout)
	defer cancel()
	switch action {
	case "new":
		pageURL := firstNonEmptyString(stringFromInput(input, "url"), "about:blank")
		if err := t.base.checkURL(pageURL); err != nil {
			return "", err
		}
		client, err := t.base.pageClient(ctx, true)
		if err != nil {
			return "", err
		}
		defer client.Close()
		if pageURL != "about:blank" {
			if err := client.navigate(ctx, pageURL); err != nil {
				t.base.discardTab(client.baseURL, client.targetID)
				return "", err
			}
			_ = client.waitNavigated(ctx)
		}
		return client.pageState(ctx, map[string]any{"tab_id": t.base.session.active()})
	case "list", "":
		return t.list(callCtx)
	case "switch", "close":
	default:
		return "", fmt.Errorf("unsupported action %q", action)
	}
	baseURL, err := t.base.endpoint(callCtx)
	if err != nil {
		return "", err
	}
	if action == "close" && tabID == "" {
		tabID = t.base.session.active()
	}
	if tabID == "" {
		return "", errors.New("tab_id is required")
	}
	targets, err := listBrowserTargets(callCtx, baseURL)
	if err != nil {
		return "", err
	}
	var found *browserTarget
	for i := range targets {
		if targets[i].ID == tabID && targets[i].Type == "page" {
			found = &targets[i]
			break
		}
	}
	if found == nil {
		return "", fmt.Errorf("没有 ID 为 %s 的标签页，先用 action=list 查一下", tabID)
	}
	if action == "close" {
		if err := browserTargetCommand(callCtx, baseURL, "close", tabID); err != nil {
			return "", err
		}
		if t.base.session.active() == tabID {
			t.base.session.setActive("")
		}
		return t.list(callCtx)
	}
	// 切到前台是给看实时画面的人看的；模型那一侧靠 session 记住操作的是哪一页。
	_ = browserTargetCommand(callCtx, baseURL, "activate", tabID)
	t.base.session.setActive(tabID)
	body, err := json.Marshal(map[string]any{"ok": true, "tab_id": tabID, "url": found.URL, "title": found.Title})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (t *BrowserTabsTool) list(ctx context.Context) (string, error) {
	baseURL, err := t.base.endpoint(ctx)
	if err != nil {
		return "", err
	}
	targets, err := listBrowserTargets(ctx, baseURL)
	if err != nil {
		return "", err
	}
	active := t.base.session.active()
	tabs := make([]map[string]any, 0, len(targets))
	for _, target := range targets {
		if target.Type != "page" {
			continue
		}
		tabs = append(tabs, map[string]any{
			"tab_id": target.ID,
			"url":    target.URL,
			"title":  target.Title,
			"active": target.ID == active,
		})
	}
	body, err := json.Marshal(map[string]any{"tabs": tabs})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// browserTargetCommand 调 DevTools 的 /json/activate 与 /json/close。
func browserTargetCommand(ctx context.Context, baseURL, command, targetID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/json/"+command+"/"+url.PathEscape(targetID), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return browserConnectError(baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("browser cdp %s failed: HTTP %d", command, resp.StatusCode)
	}
	return nil
}

// ---- 滚动 ----

type BrowserScrollTool struct {
	base browserToolBase
}

func (t *BrowserScrollTool) Name() string { return "browser_scroll" }

func (t *BrowserScrollTool) Description() string {
	return `滚动页面：按方向滚整页，或把某个元素滚进视口；给了 selector 又给了 direction 时滚的是那个可滚动容器。`
}

func (t *BrowserScrollTool) InputSchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{
		"direction": toolEnumParam("滚动方向", "down", "up", "top", "bottom"),
		"amount":    toolIntParam("up/down 滚动的像素数，默认大约一屏"),
		"selector":  toolStringParam("只给 selector：把这个元素滚进视口；同时给 direction：滚这个容器自己"),
	})
}

func (t *BrowserScrollTool) RepeatableCalls() bool { return true }

func (t *BrowserScrollTool) Run(ctx context.Context, input map[string]any) (string, error) {
	direction := strings.ToLower(stringFromInput(input, "direction"))
	selector := stringFromInput(input, "selector")
	if direction == "" && selector == "" {
		direction = "down"
	}
	switch direction {
	case "", "down", "up", "top", "bottom":
	default:
		return "", fmt.Errorf("unsupported direction %q", direction)
	}
	client, err := t.base.pageClient(ctx, false)
	if err != nil {
		return "", err
	}
	defer client.Close()
	expr := fmt.Sprintf(`(() => {
const selector = %s, direction = %s, amount = %d;
const el = selector ? document.querySelector(selector) : null;
if (selector && !el) return {ok:false, error:"selector not found"};
if (el && !direction) {
  el.scrollIntoView({block:"center", inline:"nearest"});
} else {
  const box = el || document.scrollingElement || document.documentElement;
  const step = amount > 0 ? amount : Math.round((el ? el.clientHeight : window.innerHeight) * 0.85);
  const top = direction === "top" ? 0 : direction === "bottom" ? box.scrollHeight
    : box.scrollTop + (direction === "up" ? -step : step);
  if (el) el.scrollTo({top, behavior:"instant"}); else window.scrollTo({top, behavior:"instant"});
}
const box = el && direction ? el : (document.scrollingElement || document.documentElement);
const viewport = el && direction ? el.clientHeight : window.innerHeight;
return {
  ok: true,
  scroll_y: Math.round(box.scrollTop),
  scroll_height: Math.round(box.scrollHeight),
  viewport_height: Math.round(viewport),
  at_top: box.scrollTop <= 1,
  at_bottom: box.scrollTop + viewport >= box.scrollHeight - 2
};
})()`, jsString(selector), jsString(direction), intFromInput(input, "amount", 0))
	raw, err := client.evaluate(ctx, expr)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// ---- 按键 ----

type BrowserPressKeyTool struct {
	base browserToolBase
}

func (t *BrowserPressKeyTool) Name() string { return "browser_press_key" }

func (t *BrowserPressKeyTool) Description() string {
	return `按键盘键，走真实按键事件：Enter、Tab、Escape、Backspace、方向键、PageDown、单个字符，或 Control+A 这类组合键。`
}

func (t *BrowserPressKeyTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"key"}, map[string]any{
		"key":      toolStringParam("键名，例如 Enter、Escape、ArrowDown、PageDown、a、Control+A、Shift+Tab"),
		"selector": toolStringParam("先聚焦这个元素再按；省略时按在当前焦点上"),
		"repeat":   toolIntParam("连按次数，默认 1，最多 20"),
	})
}

func (t *BrowserPressKeyTool) RepeatableCalls() bool { return true }

func (t *BrowserPressKeyTool) Run(ctx context.Context, input map[string]any) (string, error) {
	key := rawStringFromInput(input, "key")
	if _, _, err := parseBrowserKey(key); err != nil {
		return "", err
	}
	repeat := intFromInput(input, "repeat", 1)
	if repeat < 1 {
		repeat = 1
	}
	if repeat > maxBrowserKeyRepeat {
		repeat = maxBrowserKeyRepeat
	}
	client, err := t.base.pageClient(ctx, false)
	if err != nil {
		return "", err
	}
	defer client.Close()
	if selector := stringFromInput(input, "selector"); selector != "" {
		raw, err := client.evaluate(ctx, fmt.Sprintf(`(() => {
const el = document.querySelector(%s);
if (!el) return false;
el.scrollIntoView({block:"center", inline:"center"});
el.focus();
return true;
})()`, jsString(selector)))
		if err != nil {
			return "", err
		}
		var ok bool
		if json.Unmarshal(raw, &ok) != nil || !ok {
			return `{"ok":false,"error":"selector not found"}`, nil
		}
	}
	for i := 0; i < repeat; i++ {
		if err := client.pressKey(ctx, key); err != nil {
			return "", err
		}
	}
	client.settle(ctx)
	return client.pageState(ctx, map[string]any{"key": strings.TrimSpace(key), "repeat": repeat})
}

// ---- 前进、后退、刷新 ----

type BrowserNavigateTool struct {
	base browserToolBase
}

func (t *BrowserNavigateTool) Name() string { return "browser_navigate" }

func (t *BrowserNavigateTool) Description() string {
	return `在当前标签页后退、前进或刷新，返回之后的页面文本。`
}

func (t *BrowserNavigateTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"action"}, map[string]any{
		"action": toolEnumParam("back 后退、forward 前进、reload 刷新", "back", "forward", "reload"),
	})
}

func (t *BrowserNavigateTool) RepeatableCalls() bool { return true }

func (t *BrowserNavigateTool) Run(ctx context.Context, input map[string]any) (string, error) {
	action := strings.ToLower(stringFromInput(input, "action"))
	client, err := t.base.pageClient(ctx, false)
	if err != nil {
		return "", err
	}
	defer client.Close()
	switch action {
	case "reload":
		if err := client.call(ctx, "Page.reload", map[string]any{}, nil); err != nil {
			return "", err
		}
	case "back", "forward":
		var history struct {
			CurrentIndex int `json:"currentIndex"`
			Entries      []struct {
				ID int `json:"id"`
			} `json:"entries"`
		}
		if err := client.call(ctx, "Page.getNavigationHistory", map[string]any{}, &history); err != nil {
			return "", err
		}
		next := history.CurrentIndex - 1
		if action == "forward" {
			next = history.CurrentIndex + 1
		}
		if next < 0 || next >= len(history.Entries) {
			return "", fmt.Errorf("当前标签页没有可以%s的页面", map[string]string{"back": "后退", "forward": "前进"}[action])
		}
		if err := client.call(ctx, "Page.navigateToHistoryEntry", map[string]any{"entryId": history.Entries[next].ID}, nil); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("unsupported action %q", action)
	}
	client.settle(ctx)
	_ = client.waitReady(ctx)
	return t.base.pageSnapshot(ctx, client, "")
}

// ---- 下拉框 ----

type BrowserSelectTool struct {
	base browserToolBase
}

func (t *BrowserSelectTool) Name() string { return "browser_select" }

func (t *BrowserSelectTool) Description() string {
	return `在 <select> 下拉框里选一项，按选项的 value 或显示文字选。`
}

func (t *BrowserSelectTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"selector"}, map[string]any{
		"selector": toolStringParam("<select> 元素的 CSS 选择器"),
		"value":    toolStringParam("要选的 option 的 value"),
		"label":    toolStringParam("要选的 option 的显示文字，和 value 二选一"),
	})
}

func (t *BrowserSelectTool) Run(ctx context.Context, input map[string]any) (string, error) {
	selector := stringFromInput(input, "selector")
	value := rawStringFromInput(input, "value")
	label := stringFromInput(input, "label")
	if selector == "" {
		return "", errors.New("selector is required")
	}
	if value == "" && label == "" {
		return "", errors.New("value or label is required")
	}
	client, err := t.base.pageClient(ctx, false)
	if err != nil {
		return "", err
	}
	defer client.Close()
	expr := fmt.Sprintf(`(() => {
const el = document.querySelector(%s);
if (!el) return {ok:false, error:"selector not found"};
if (el.tagName !== "SELECT") return {ok:false, error:"element is not a <select>"};
const value = %s, label = %s;
const options = Array.from(el.options);
const option = options.find(o => value !== "" ? o.value === value : o.text.trim() === label)
  || (label ? options.find(o => o.text.trim().includes(label)) : null);
if (!option) return {ok:false, error:"option not found", options: options.slice(0, 50).map(o => ({value:o.value, label:o.text.trim()}))};
const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value").set;
setter.call(el, option.value);
el.dispatchEvent(new Event("input", {bubbles:true}));
el.dispatchEvent(new Event("change", {bubbles:true}));
return {ok:true, value: option.value, label: option.text.trim()};
})()`, jsString(selector), jsString(value), jsString(label))
	raw, err := client.evaluate(ctx, expr)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// ---- 等待 ----

type BrowserWaitTool struct {
	base browserToolBase
}

func (t *BrowserWaitTool) Name() string { return "browser_wait" }

func (t *BrowserWaitTool) Description() string {
	return `等页面上出现某个元素或某段文字（异步加载、跳转之后用），或者单纯等一会儿。超时不算失败，返回 found=false。`
}

func (t *BrowserWaitTool) InputSchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{
		"selector":   toolStringParam("等这个元素出现并可见"),
		"text":       toolStringParam("等页面上出现这段文字"),
		"timeout_ms": toolIntParam(fmt.Sprintf("最多等多久，默认 %d，最多 %d；selector 和 text 都不给时就是单纯等这么久", defaultBrowserWaitMS, maxBrowserWaitMS)),
	})
}

func (t *BrowserWaitTool) RepeatableCalls() bool { return true }

func (t *BrowserWaitTool) Run(ctx context.Context, input map[string]any) (string, error) {
	selector := stringFromInput(input, "selector")
	text := stringFromInput(input, "text")
	timeout := intFromInput(input, "timeout_ms", defaultBrowserWaitMS)
	if timeout <= 0 {
		timeout = defaultBrowserWaitMS
	}
	if timeout > maxBrowserWaitMS {
		timeout = maxBrowserWaitMS
	}
	started := time.Now()
	if selector == "" && text == "" {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Duration(timeout) * time.Millisecond):
		}
		return fmt.Sprintf(`{"ok":true,"waited_ms":%d}`, timeout), nil
	}
	client, err := t.base.pageClient(ctx, false)
	if err != nil {
		return "", err
	}
	defer client.Close()
	expr := fmt.Sprintf(`(() => {
const selector = %s, text = %s;
if (selector) {
  const el = document.querySelector(selector);
  if (!el) return false;
  const r = el.getBoundingClientRect();
  const style = getComputedStyle(el);
  if (r.width === 0 && r.height === 0) return false;
  if (style.visibility === "hidden" || style.display === "none") return false;
}
if (text && !(document.body && document.body.innerText.includes(text))) return false;
return true;
})()`, jsString(selector), jsString(text))
	deadline := started.Add(time.Duration(timeout) * time.Millisecond)
	for {
		// 等的过程中页面可能正在跳转，执行上下文被销毁时的报错不算失败，下一轮再看。
		if raw, err := client.evaluate(ctx, expr); err == nil {
			var found bool
			if json.Unmarshal(raw, &found) == nil && found {
				return client.pageState(ctx, map[string]any{"found": true, "waited_ms": time.Since(started).Milliseconds()})
			}
		}
		if time.Now().After(deadline) {
			return client.pageState(ctx, map[string]any{"found": false, "waited_ms": time.Since(started).Milliseconds()})
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// ---- 执行脚本 ----

type BrowserEvalTool struct {
	base browserToolBase
}

func (t *BrowserEvalTool) Name() string { return "browser_eval" }

func (t *BrowserEvalTool) Description() string {
	return `在当前页面执行一段 JavaScript 并返回结果（按 JSON 返回，Promise 会等它完成）。适合批量提取结构化数据、操作其他工具覆盖不到的页面状态。`
}

func (t *BrowserEvalTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"script"}, map[string]any{
		"script": toolStringParam("JavaScript 表达式；多条语句写成 (() => { ...; return 结果 })()，异步写成 (async () => { ... })()"),
	})
}

func (t *BrowserEvalTool) RepeatableCalls() bool { return true }

func (t *BrowserEvalTool) Run(ctx context.Context, input map[string]any) (string, error) {
	script := rawStringFromInput(input, "script")
	if strings.TrimSpace(script) == "" {
		return "", errors.New("script is required")
	}
	client, err := t.base.pageClient(ctx, false)
	if err != nil {
		return "", err
	}
	defer client.Close()
	raw, err := client.evaluateScript(ctx, script)
	if err != nil {
		return "", err
	}
	result := string(raw)
	if t.base.maxChars > 0 && utf8.RuneCountInString(result) > t.base.maxChars {
		runes := []rune(result)
		result = string(runes[:t.base.maxChars]) + fmt.Sprintf("\n…（结果共 %d 字，已截断；需要的话在脚本里只取需要的部分）", len(runes))
	}
	return result, nil
}
