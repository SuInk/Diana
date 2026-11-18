// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// 沙箱浏览器按 browser-use 的做法给页面上能点的元素编号：每次打开、点击、输入、
// 滚动、截图都交回当前视口里的可交互元素清单，形如 [3]<button>开始测试</button>，
// 模型按编号点、按编号输入，截图上也标出同样的编号。
//
// 元素逐个 frame 收集（包括跨域 iframe——沙箱关了站点隔离，所有 frame 和主页面同
// 进程），坐标换算到主视口，点击走主视口上的真实鼠标事件，所以 iframe 里的按钮和
// 主页面的按钮点法一样。

const (
	maxBrowserElementFrames = 12
	browserElementWorld     = "diana-elements"
)

// browserElement 是一个编号元素。X/Y/W/H 是主视口 CSS 像素；LocalX/LocalY 是它
// 在自己 frame 里的中心点，点之前用来核对这个位置上还是不是它。
type browserElement struct {
	Index   int
	FrameID string
	Tag     string
	Text    string
	Attrs   string
	X, Y    float64
	W, H    float64
	LocalX  float64
	LocalY  float64
}

func (e browserElement) centre() (float64, float64) { return e.X + e.W/2, e.Y + e.H/2 }

func (e browserElement) line() string {
	attrs := ""
	if e.Attrs != "" {
		attrs = " " + e.Attrs
	}
	return fmt.Sprintf("[%d]<%s%s>%s</%s>", e.Index, e.Tag, attrs, e.Text, e.Tag)
}

type browserElementState struct {
	URL      string
	Title    string
	Text     string
	Elements []browserElement
	Above    int
	Below    int
	// Unread 是这次没读到的嵌入页（还在跳转、刚被换掉），照实告诉模型，别当成没有。
	Unread []string
}

// lines 是交给模型的元素清单，前后各一句告诉它视口外还有多少。
func (s browserElementState) lines() string {
	var b strings.Builder
	if s.Above > 0 {
		fmt.Fprintf(&b, "... 上方还有约 %d 像素，browser 的 scroll 往上看 ...\n", s.Above)
	}
	for _, el := range s.Elements {
		b.WriteString(el.line())
		b.WriteByte('\n')
	}
	if len(s.Elements) == 0 {
		b.WriteString("（当前视口里没找到可点击的元素；画在 canvas 上的内容用截图看，按坐标点）\n")
	}
	if s.Below > 0 {
		fmt.Fprintf(&b, "... 下方还有约 %d 像素，browser 的 scroll 往下看 ...\n", s.Below)
	}
	for _, frame := range s.Unread {
		fmt.Fprintf(&b, "（嵌入页 %s 这次没读到，可能还在加载，用 browser 的 wait 等一下再 text 重读）\n", frame)
	}
	return strings.TrimRight(b.String(), "\n")
}

// browserElementScript 在一个 frame 的隔离环境里跑：DOM 和页面共享，脚本变量不和
// 页面混。挑元素的规则照 browser-use：语义控件、交互角色、可编辑区，加上鼠标指针
// 是小手的最外层元素（不少小游戏的选项就是一个 div）；只要视口内、没被盖住的。
const browserElementScript = `(() => {
const norm = (v) => String(v || "").replace(/\s+/g, " ").trim();
const clip = (v, n) => { v = norm(v); return v.length > n ? v.slice(0, n) + "…" : v; };
const vw = window.innerWidth, vh = window.innerHeight;
const semantic = new Set(["A","BUTTON","INPUT","SELECT","TEXTAREA","SUMMARY","DETAILS","LABEL"]);
const roles = new Set(["button","link","checkbox","radio","tab","menuitem","menuitemradio","menuitemcheckbox","option","switch","combobox","textbox","searchbox","slider","treeitem"]);
const picked = new Set();
const out = [];
const visible = (el, r) => {
  if (r.width < 2 || r.height < 2) return false;
  if (r.bottom <= 0 || r.right <= 0 || r.top >= vh || r.left >= vw) return false;
  const cs = getComputedStyle(el);
  return cs.visibility !== "hidden" && cs.display !== "none" && Number(cs.opacity) !== 0;
};
for (const el of document.querySelectorAll("body *")) {
  const tag = el.tagName;
  let ok = false;
  if (semantic.has(tag)) {
    ok = !(tag === "A" && !el.hasAttribute("href")) && !(tag === "INPUT" && el.type === "hidden") && !el.disabled;
  }
  if (!ok && roles.has((el.getAttribute("role") || "").toLowerCase())) ok = el.getAttribute("aria-disabled") !== "true";
  if (!ok && el.isContentEditable && !(el.parentElement && el.parentElement.isContentEditable)) ok = true;
  if (!ok && (el.hasAttribute("onclick") || (el.hasAttribute("tabindex") && el.tabIndex >= 0))) ok = true;
  if (!ok && tag !== "HTML" && tag !== "BODY") {
    if (getComputedStyle(el).cursor === "pointer") {
      const parent = el.parentElement;
      ok = !parent || getComputedStyle(parent).cursor !== "pointer";
    }
  }
  if (!ok) continue;
  let nested = false;
  for (let p = el.parentElement; p; p = p.parentElement) { if (picked.has(p)) { nested = true; break; } }
  if (nested) continue;
  const r = el.getBoundingClientRect();
  if (!visible(el, r)) continue;
  const cx = Math.min(Math.max(r.left + r.width / 2, 1), vw - 1);
  const cy = Math.min(Math.max(r.top + r.height / 2, 1), vh - 1);
  const hit = document.elementFromPoint(cx, cy);
  if (!hit || !(hit === el || el.contains(hit) || hit.contains(el))) continue;
  picked.add(el);
  const attrs = [];
  const add = (k, v) => { v = clip(v, 60); if (v) attrs.push(k + "=" + JSON.stringify(v)); };
  if (tag === "INPUT") add("type", el.type);
  const role = el.getAttribute("role"); if (role) add("role", role);
  if (tag === "A") add("href", el.getAttribute("href"));
  if ("placeholder" in el) add("placeholder", el.placeholder);
  if ((tag === "INPUT" || tag === "TEXTAREA") && el.type !== "password") add("value", el.value);
  if (tag === "SELECT" && el.selectedOptions && el.selectedOptions[0]) add("selected", el.selectedOptions[0].text);
  if (el.checked || el.getAttribute("aria-checked") === "true" || el.getAttribute("aria-selected") === "true") attrs.push("checked");
  let text = clip(el.innerText || "", 80);
  if (!text) text = clip(el.getAttribute("aria-label") || el.getAttribute("title") || el.getAttribute("alt") || (el.querySelector && el.querySelector("img[alt]") ? el.querySelector("img[alt]").alt : ""), 80);
  out.push({tag: tag.toLowerCase(), text, attrs: attrs.join(" "), x: r.left, y: r.top, w: r.width, h: r.height, lx: cx, ly: cy});
}
const se = document.scrollingElement || document.documentElement;
return {
  url: location.href,
  title: document.title,
  text: String(document.body ? document.body.innerText : "").replace(/[ \t\u00a0]+/g, " ").replace(/\s*\n\s*/g, "\n").trim(),
  vw, vh,
  above: Math.round(se.scrollTop),
  below: Math.max(0, Math.round(se.scrollHeight - se.scrollTop - vh)),
  elements: out
};
})()`

type browserFrameNode struct {
	Frame struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	} `json:"frame"`
	ChildFrames []browserFrameNode `json:"childFrames"`
}

type browserFrameElements struct {
	URL      string  `json:"url"`
	Title    string  `json:"title"`
	Text     string  `json:"text"`
	VW       float64 `json:"vw"`
	VH       float64 `json:"vh"`
	Above    int     `json:"above"`
	Below    int     `json:"below"`
	Elements []struct {
		Tag   string  `json:"tag"`
		Text  string  `json:"text"`
		Attrs string  `json:"attrs"`
		X     float64 `json:"x"`
		Y     float64 `json:"y"`
		W     float64 `json:"w"`
		H     float64 `json:"h"`
		LX    float64 `json:"lx"`
		LY    float64 `json:"ly"`
	} `json:"elements"`
}

// frameContext 在指定 frame 里开一个隔离环境，返回它的执行上下文 ID。
func (c *cdpClient) frameContext(ctx context.Context, frameID string) (int64, error) {
	var world struct {
		ID int64 `json:"executionContextId"`
	}
	if err := c.call(ctx, "Page.createIsolatedWorld", map[string]any{"frameId": frameID, "worldName": browserElementWorld, "grantUniveralAccess": false}, &world); err != nil {
		return 0, err
	}
	return world.ID, nil
}

func (c *cdpClient) evaluateIn(ctx context.Context, contextID int64, expression string) (json.RawMessage, error) {
	var out struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := c.call(ctx, "Runtime.evaluate", map[string]any{"expression": expression, "contextId": contextID, "returnByValue": true}, &out); err != nil {
		return nil, err
	}
	if out.ExceptionDetails != nil {
		return nil, errors.New("页面脚本出错：" + out.ExceptionDetails.Text)
	}
	return out.Result.Value, nil
}

// frameOffset 是子 frame 内容区左上角在主视口里的位置。DOM.getBoxModel 给的坐标
// 已经算上了外层 frame 的位置和各层滚动，嵌套 iframe 不用再自己累加。
func (c *cdpClient) frameOffset(ctx context.Context, frameID string) (x, y, w, h float64, err error) {
	var owner struct {
		BackendNodeID int64 `json:"backendNodeId"`
	}
	if err = c.call(ctx, "DOM.getFrameOwner", map[string]any{"frameId": frameID}, &owner); err != nil {
		return
	}
	var box struct {
		Model struct {
			Content []float64 `json:"content"`
		} `json:"model"`
	}
	if err = c.call(ctx, "DOM.getBoxModel", map[string]any{"backendNodeId": owner.BackendNodeID}, &box); err != nil {
		return
	}
	quad := box.Model.Content
	if len(quad) < 8 {
		return 0, 0, 0, 0, errors.New("iframe has no box")
	}
	x, y = math.Min(quad[0], quad[6]), math.Min(quad[1], quad[3])
	return x, y, math.Max(quad[2], quad[4]) - x, math.Max(quad[5], quad[7]) - y, nil
}

func flattenBrowserFrames(node browserFrameNode, out []browserFrameNode) []browserFrameNode {
	out = append(out, node)
	for _, child := range node.ChildFrames {
		if len(out) >= maxBrowserElementFrames {
			break
		}
		out = flattenBrowserFrames(child, out)
	}
	return out
}

// collectElements 收集各 frame 视口内的可交互元素和文字，编号后记进对话的会话里，
// 后面的点击、输入按编号找回坐标。
func (b browserToolBase) collectElements(ctx context.Context, client *cdpClient) (browserElementState, error) {
	var tree struct {
		FrameTree browserFrameNode `json:"frameTree"`
	}
	if err := client.call(ctx, "Page.getFrameTree", nil, &tree); err != nil {
		return browserElementState{}, err
	}
	_ = client.call(ctx, "DOM.enable", nil, nil)
	frames := flattenBrowserFrames(tree.FrameTree, nil)
	state := browserElementState{}
	unread := func(frame browserFrameNode) {
		if frame.Frame.URL != "" && !strings.HasPrefix(frame.Frame.URL, "about:") {
			state.Unread = append(state.Unread, frame.Frame.URL)
		}
	}
	var texts []string
	script := browserElementScript
	for i, frame := range frames {
		offsetX, offsetY := 0.0, 0.0
		if i > 0 {
			x, y, w, h, err := client.frameOffset(ctx, frame.Frame.ID)
			if err != nil {
				unread(frame)
				continue
			}
			if w < 2 || h < 2 {
				// 藏起来的 iframe（统计、预加载）不算没读到。
				continue
			}
			offsetX, offsetY = x, y
		}
		contextID, err := client.frameContext(ctx, frame.Frame.ID)
		if err != nil {
			if i == 0 {
				return browserElementState{}, err
			}
			unread(frame)
			continue
		}
		raw, err := client.evaluateIn(ctx, contextID, script)
		if err != nil {
			if i == 0 {
				return browserElementState{}, err
			}
			unread(frame)
			continue
		}
		var got browserFrameElements
		if json.Unmarshal(raw, &got) != nil {
			unread(frame)
			continue
		}
		if i == 0 {
			state.URL, state.Title, state.Above, state.Below = got.URL, got.Title, got.Above, got.Below
			if got.Text != "" {
				texts = append(texts, got.Text)
			}
		} else if got.Text != "" {
			texts = append(texts, "【嵌入页 "+got.URL+"】\n"+got.Text)
		}
		for _, el := range got.Elements {
			state.Elements = append(state.Elements, browserElement{
				Index: len(state.Elements), FrameID: frame.Frame.ID,
				Tag: el.Tag, Text: el.Text, Attrs: el.Attrs,
				X: offsetX + el.X, Y: offsetY + el.Y, W: el.W, H: el.H,
				LocalX: el.LX, LocalY: el.LY,
			})
		}
	}
	state.Text = strings.Join(texts, "\n\n")
	b.session.setElements(client.targetID, state)
	return state, nil
}

// elementByIndex 取上一次交给模型的编号元素，并核对那个位置上还是不是它：页面换了、
// 弹层盖上来了，旧编号就不能再点，免得点到别的东西上。
func (b browserToolBase) elementByIndex(ctx context.Context, client *cdpClient, index int) (browserElement, error) {
	target, state := b.session.elements()
	if target != client.targetID || index < 0 || index >= len(state.Elements) {
		return browserElement{}, fmt.Errorf("没有编号 %d 的元素：编号只在最近一次返回的元素清单里有效，先看最新清单", index)
	}
	el := state.Elements[index]
	contextID, err := client.frameContext(ctx, el.FrameID)
	if err != nil {
		return browserElement{}, errors.New("元素所在的页面已经变了，编号失效，请看最新元素清单")
	}
	raw, err := client.evaluateIn(ctx, contextID, fmt.Sprintf(`(() => {
const norm = (v) => String(v || "").replace(/\s+/g, " ").trim();
const tag = %s, text = %s;
for (let el = document.elementFromPoint(%f, %f), depth = 0; el && depth < 8; el = el.parentElement, depth++) {
  if (el.tagName.toLowerCase() !== tag) continue;
  const own = norm(el.innerText || "");
  if (!text || own.startsWith(text.replace(/…$/, "")) || !own) return true;
}
return false;
})()`, jsString(el.Tag), jsString(el.Text), el.LocalX, el.LocalY))
	var same bool
	if err != nil || json.Unmarshal(raw, &same) != nil || !same {
		return browserElement{}, fmt.Errorf("编号 %d 的元素已经不在原处（页面变了或被盖住），编号失效，请看最新元素清单", index)
	}
	return el, nil
}

// focusClear 在元素所在 frame 里清空当前焦点输入框，和 browser_type 的清空做法一致。
func (c *cdpClient) focusClear(ctx context.Context, frameID string) error {
	contextID, err := c.frameContext(ctx, frameID)
	if err != nil {
		return err
	}
	_, err = c.evaluateIn(ctx, contextID, `(() => {
const el = document.activeElement;
if (!el) return false;
if (el.isContentEditable) {
  const range = document.createRange();
  range.selectNodeContents(el);
  const sel = window.getSelection();
  sel.removeAllRanges();
  sel.addRange(range);
  document.execCommand("delete");
} else if ("value" in el) {
  const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), "value")?.set;
  if (setter) setter.call(el, ""); else el.value = "";
  el.dispatchEvent(new Event("input", {bubbles: true}));
}
return true;
})()`)
	return err
}

// markElements 在主页面最上层盖一层编号框（不挡鼠标），截图之后由返回的函数撤掉。
// 框画在主文档里，坐标已经换算过，iframe 里的元素也框得准。
func (c *cdpClient) markElements(ctx context.Context, frameID string, state browserElementState) func() {
	type mark struct {
		I   int        `json:"i"`
		Box [4]float64 `json:"box"`
	}
	marks := make([]mark, 0, len(state.Elements))
	for _, el := range state.Elements {
		marks = append(marks, mark{I: el.Index, Box: [4]float64{el.X, el.Y, el.W, el.H}})
	}
	data, _ := json.Marshal(marks)
	contextID, err := c.frameContext(ctx, frameID)
	if err != nil {
		return func() {}
	}
	_, err = c.evaluateIn(ctx, contextID, fmt.Sprintf(`(() => {
const old = document.getElementById("__diana_marks"); if (old) old.remove();
const colors = ["#e6194b","#3cb44b","#4363d8","#f58231","#911eb4","#008080","#9a6324","#800000"];
const layer = document.createElement("div");
layer.id = "__diana_marks";
layer.style.cssText = "position:fixed;inset:0;pointer-events:none;z-index:2147483647";
for (const m of %s) {
  const c = colors[m.i %% colors.length];
  const box = document.createElement("div");
  box.style.cssText = "position:fixed;box-sizing:border-box;border:2px solid " + c + ";left:" + m.box[0] + "px;top:" + m.box[1] + "px;width:" + m.box[2] + "px;height:" + m.box[3] + "px";
  const label = document.createElement("div");
  label.textContent = m.i;
  label.style.cssText = "position:absolute;top:-2px;right:-2px;background:" + c + ";color:#fff;font:bold 12px/14px sans-serif;padding:0 3px";
  box.appendChild(label);
  layer.appendChild(box);
}
document.documentElement.appendChild(layer);
return true;
})()`, data))
	if err != nil {
		return func() {}
	}
	return func() {
		_, _ = c.evaluateIn(context.WithoutCancel(ctx), contextID, `(() => { const old = document.getElementById("__diana_marks"); if (old) old.remove(); return true; })()`)
	}
}

// sandboxState 是沙箱里每个动作之后交回模型的页面状态：地址、标题、各 frame 的文字
// （按页分）和当前视口的元素清单。
func (b browserToolBase) sandboxState(ctx context.Context, client *cdpClient, offset int, extra map[string]any) (string, error) {
	if err := b.validatePersonalPage(ctx, client); err != nil {
		return "", err
	}
	client.waitFramesReady(ctx)
	state, err := b.collectElements(ctx, client)
	if err != nil {
		return "", err
	}
	text := []rune(state.Text)
	return pageByLines(text, offset, ToolOutputBudget(ctx), func(chunk string, end int) any {
		out := map[string]any{"url": state.URL, "title": state.Title, "text": chunk, "total_chars": len(text), "elements": state.lines()}
		for key, value := range extra {
			out[key] = value
		}
		if offset > 0 {
			out["offset"] = min(offset, len(text))
		}
		if end < len(text) {
			out["next_offset"] = end
			out["read_notice"] = fmt.Sprintf("页面文字共 %d 字，这次给到第 %d 字。要后面的内容调 browser 的 text，参数 {\"offset\":%d}。", len(text), end, end)
		}
		return out
	})
}

func (s *browserSession) setElements(targetID string, state browserElementState) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.elementTarget, s.elementState = targetID, state
	s.mu.Unlock()
}

func (s *browserSession) elements() (string, browserElementState) {
	if s == nil {
		return "", browserElementState{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.elementTarget, s.elementState
}

// waitFramesReady 等页面里每个 frame 都不在加载中，最多 4 秒。settle 只看主文档；B 站
// Toy 这类页面点一下换的是 iframe 里的页，主文档早就 complete 了，不等就会读到一个
// 正在跳转、什么都拿不到的 iframe。
func (c *cdpClient) waitFramesReady(ctx context.Context) {
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		var tree struct {
			FrameTree browserFrameNode `json:"frameTree"`
		}
		if c.call(ctx, "Page.getFrameTree", nil, &tree) != nil {
			return
		}
		ready := true
		for _, frame := range flattenBrowserFrames(tree.FrameTree, nil) {
			contextID, err := c.frameContext(ctx, frame.Frame.ID)
			if err != nil {
				ready = false
				break
			}
			raw, err := c.evaluateIn(ctx, contextID, `document.readyState`)
			var state string
			if err != nil || json.Unmarshal(raw, &state) != nil || state == "loading" {
				ready = false
				break
			}
		}
		if ready {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// evaluateInFrames 在每个 frame 里跑一个返回布尔值的表达式，任何一个为真就返回 true。
func (c *cdpClient) evaluateInFrames(ctx context.Context, expression string) (json.RawMessage, error) {
	var tree struct {
		FrameTree browserFrameNode `json:"frameTree"`
	}
	if err := c.call(ctx, "Page.getFrameTree", nil, &tree); err != nil {
		return nil, err
	}
	for _, frame := range flattenBrowserFrames(tree.FrameTree, nil) {
		contextID, err := c.frameContext(ctx, frame.Frame.ID)
		if err != nil {
			continue
		}
		raw, err := c.evaluateIn(ctx, contextID, expression)
		var found bool
		if err == nil && json.Unmarshal(raw, &found) == nil && found {
			return raw, nil
		}
	}
	return json.RawMessage("false"), nil
}
