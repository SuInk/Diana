// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package mobilectl

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// WDADriver controls physical iPhones or iOS Simulators running Appium WebDriverAgent (WDA).
type WDADriver struct {
	baseURL string
	client  *http.Client
	timeout time.Duration

	mu        sync.Mutex
	sessionID string
	deviceID  string
}

// NewWDADriver creates a WebDriverAgent HTTP client driver.
func NewWDADriver(baseURL string, client *http.Client, timeout time.Duration) *WDADriver {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8100"
	}
	if timeout <= 0 {
		timeout = defaultCommandLimit
	}
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &WDADriver{
		baseURL: baseURL,
		client:  client,
		timeout: timeout,
	}
}

func (d *WDADriver) doJSON(ctx context.Context, method, path string, body any, out any) error {
	callCtx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(callCtx, method, d.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("连接 iPhone WebDriverAgent (%s) 失败：%w", d.baseURL, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 24<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("WebDriverAgent %s %s 返回 HTTP %d：%s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("解析 WebDriverAgent 响应失败：%w", err)
		}
	}
	return nil
}

// ListDevices queries WDA `/status` to report the attached iPhone / iOS Simulator.
func (d *WDADriver) ListDevices(ctx context.Context) ([]DeviceInfo, error) {
	var resp struct {
		SessionID string `json:"sessionId"`
		Value     struct {
			Ready bool `json:"ready"`
			OS    struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"os"`
			IOS struct {
				IP string `json:"ip"`
			} `json:"ios"`
			Device string `json:"device"`
		} `json:"value"`
	}
	if err := d.doJSON(ctx, http.MethodGet, "/status", nil, &resp); err != nil {
		return nil, err
	}
	id := "wda-ios"
	if ip := strings.TrimSpace(resp.Value.IOS.IP); ip != "" {
		id = "ios-" + ip
	}
	name := strings.TrimSpace(resp.Value.OS.Name + " " + resp.Value.OS.Version)
	if name == "" {
		name = firstNonEmptyString(strings.TrimSpace(resp.Value.Device), "iPhone (WDA)")
	}
	d.mu.Lock()
	if strings.TrimSpace(resp.SessionID) != "" {
		d.sessionID = strings.TrimSpace(resp.SessionID)
	}
	d.deviceID = id
	d.mu.Unlock()
	return []DeviceInfo{{
		ID:       id,
		Name:     name,
		Platform: PlatformIOS,
		State:    "online",
		Backend:  BackendWDA,
	}}, nil
}

func (d *WDADriver) ensureSession(ctx context.Context) (string, error) {
	d.mu.Lock()
	sid := d.sessionID
	d.mu.Unlock()
	if sid != "" {
		return sid, nil
	}
	_, _ = d.ListDevices(ctx)
	d.mu.Lock()
	sid = d.sessionID
	d.mu.Unlock()
	if sid != "" {
		return sid, nil
	}
	var created struct {
		SessionID string `json:"sessionId"`
		Value     struct {
			SessionID string `json:"sessionId"`
		} `json:"value"`
	}
	if err := d.doJSON(ctx, http.MethodPost, "/session", map[string]any{
		"capabilities": map[string]any{"alwaysMatch": map[string]any{}},
	}, &created); err != nil {
		return "", err
	}
	sid = firstNonEmptyString(strings.TrimSpace(created.SessionID), strings.TrimSpace(created.Value.SessionID))
	if sid == "" {
		return "", errors.New("WebDriverAgent 未返回有效 sessionId")
	}
	d.mu.Lock()
	d.sessionID = sid
	d.mu.Unlock()
	return sid, nil
}

func (d *WDADriver) dropSession() {
	d.mu.Lock()
	d.sessionID = ""
	d.mu.Unlock()
}

// LaunchApp launches an iOS app by bundle identifier (e.g. `com.xingin.discover`).
func (d *WDADriver) LaunchApp(ctx context.Context, _ string, appID string) error {
	bundleID := strings.TrimSpace(appID)
	if bundleID == "" {
		return errors.New("iOS 应用 Bundle ID 不能为空")
	}
	sid, err := d.ensureSession(ctx)
	if err != nil {
		return err
	}
	err = d.doJSON(ctx, http.MethodPost, "/session/"+sid+"/wda/apps/launch", map[string]any{"bundleId": bundleID}, nil)
	if err != nil {
		d.dropSession()
	}
	return err
}

// OpenURL opens a URL or Universal Link on iOS via WDA.
func (d *WDADriver) OpenURL(ctx context.Context, _ string, rawURL string) error {
	clean := strings.TrimSpace(rawURL)
	if clean == "" {
		return errors.New("链接不能为空")
	}
	sid, err := d.ensureSession(ctx)
	if err != nil {
		return err
	}
	return d.doJSON(ctx, http.MethodPost, "/session/"+sid+"/url", map[string]any{"url": clean}, nil)
}

// Screenshot captures a base64 PNG screenshot from WDA `/screenshot`.
func (d *WDADriver) Screenshot(ctx context.Context, _ string) ([]byte, error) {
	var resp struct {
		Value string `json:"value"`
	}
	if err := d.doJSON(ctx, http.MethodGet, "/screenshot", nil, &resp); err != nil {
		return nil, err
	}
	b64 := strings.TrimSpace(resp.Value)
	if b64 == "" {
		return nil, errors.New("WebDriverAgent 截图返回为空")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("解码 WebDriverAgent 截图失败：%w", err)
	}
	return raw, nil
}

// Elements fetches WDA `/source?format=json` and flattens visible XCUIElement nodes.
func (d *WDADriver) Elements(ctx context.Context, _ string) ([]Element, int, int, error) {
	var resp struct {
		Value json.RawMessage `json:"value"`
	}
	if err := d.doJSON(ctx, http.MethodGet, "/source?format=json", nil, &resp); err != nil {
		return nil, 0, 0, err
	}
	return ParseWDASourceJSON(resp.Value)
}

type wdaSourceNode struct {
	Type      string          `json:"type"`
	Label     string          `json:"label"`
	Name      string          `json:"name"`
	Value     any             `json:"value"`
	IsVisible any             `json:"isVisible"`
	IsEnabled any             `json:"isEnabled"`
	Rect      wdaNodeRect     `json:"rect"`
	Children  []wdaSourceNode `json:"children"`
}

type wdaNodeRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// ParseWDASourceJSON flattens a WebDriverAgent JSON accessibility tree into visible elements.
func ParseWDASourceJSON(raw []byte) ([]Element, int, int, error) {
	// Some WDA versions wrap JSON source as a quoted JSON string inside "value".
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var inner string
		if err := json.Unmarshal(trimmed, &inner); err == nil {
			trimmed = []byte(inner)
		}
	}
	var root wdaSourceNode
	if err := json.Unmarshal(trimmed, &root); err != nil {
		return nil, 0, 0, fmt.Errorf("解析 iOS WDA 控件树失败：%w", err)
	}
	var out []Element
	rootW := int(root.Rect.Width + 0.5)
	rootH := int(root.Rect.Height + 0.5)
	var walk func(node wdaSourceNode)
	walk = func(node wdaSourceNode) {
		if !wdaTruthy(node.IsVisible, true) {
			return
		}
		w := int(node.Rect.Width + 0.5)
		h := int(node.Rect.Height + 0.5)
		x := int(node.Rect.X + 0.5)
		y := int(node.Rect.Y + 0.5)
		if x+w > rootW {
			rootW = x + w
		}
		if y+h > rootH {
			rootH = y + h
		}
		valStr := wdaValueString(node.Value)
		label := firstNonEmptyString(strings.TrimSpace(node.Label), strings.TrimSpace(node.Name))
		shortType := strings.TrimPrefix(strings.TrimSpace(node.Type), "XCUIElementType")
		isInteractive := shortType == "Button" || shortType == "Cell" || shortType == "Link" || shortType == "TextField" || shortType == "SearchField"
		if w > 0 && h > 0 && (label != "" || valStr != "" || (isInteractive && len(node.Children) == 0)) {
			// Skip huge container windows so leaf text/cards stay clean.
			if shortType != "Application" && shortType != "Window" && (label != "" || valStr != "") {
				out = append(out, Element{
					Type:   shortType,
					Label:  label,
					Text:   valStr,
					X:      x,
					Y:      y,
					Width:  w,
					Height: h,
				})
			}
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	walk(root)
	return out, rootW, rootH, nil
}

func wdaTruthy(val any, defaultVal bool) bool {
	switch typed := val.(type) {
	case bool:
		return typed
	case string:
		if typed == "0" || strings.EqualFold(typed, "false") {
			return false
		}
		if typed == "1" || strings.EqualFold(typed, "true") {
			return true
		}
	case float64:
		return typed != 0
	}
	return defaultVal
}

func wdaValueString(val any) string {
	switch typed := val.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return formatWDAFloat(typed)
	default:
		return ""
	}
}

func formatWDAFloat(v float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
}

// Tap taps point coordinates on iOS via WDA W3C actions or `/wda/tap`.
func (d *WDADriver) Tap(ctx context.Context, _ string, x, y int) error {
	sid, err := d.ensureSession(ctx)
	if err != nil {
		return err
	}
	if err := d.doJSON(ctx, http.MethodPost, "/session/"+sid+"/wda/tap", map[string]any{"x": x, "y": y}, nil); err == nil {
		return nil
	}
	return d.doJSON(ctx, http.MethodPost, "/session/"+sid+"/wda/tap/0", map[string]any{"x": x, "y": y}, nil)
}

// Swipe drags from (x1,y1) to (x2,y2) via WDA `dragfromtoforduration`.
func (d *WDADriver) Swipe(ctx context.Context, _ string, x1, y1, x2, y2, durationMS int) error {
	sid, err := d.ensureSession(ctx)
	if err != nil {
		return err
	}
	sec := float64(durationMS) / 1000.0
	if sec < 0.1 {
		sec = 0.35
	}
	return d.doJSON(ctx, http.MethodPost, "/session/"+sid+"/wda/dragfromtoforduration", map[string]any{
		"fromX":    x1,
		"fromY":    y1,
		"toX":      x2,
		"toY":      y2,
		"duration": sec,
	}, nil)
}

// TypeText sends characters to the active iOS keyboard focus.
func (d *WDADriver) TypeText(ctx context.Context, _ string, text string) error {
	if text == "" {
		return nil
	}
	sid, err := d.ensureSession(ctx)
	if err != nil {
		return err
	}
	chars := make([]string, 0, len([]rune(text)))
	for _, r := range text {
		chars = append(chars, string(r))
	}
	return d.doJSON(ctx, http.MethodPost, "/session/"+sid+"/wda/keys", map[string]any{"value": chars}, nil)
}

// PressKey triggers an iOS hardware/system button ("home", "volumeUp", "volumeDown") orEnter key.
func (d *WDADriver) PressKey(ctx context.Context, deviceID, key string) error {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "home":
		if sid, err := d.ensureSession(ctx); err == nil {
			if err := d.doJSON(ctx, http.MethodPost, "/session/"+sid+"/wda/pressButton", map[string]any{"name": "home"}, nil); err == nil {
				return nil
			}
		}
		return d.doJSON(ctx, http.MethodPost, "/wda/homescreen", map[string]any{}, nil)
	case "enter", "return":
		return d.TypeText(ctx, deviceID, "\n")
	case "back":
		// iOS standard edge-back swipe gesture from left edge.
		return d.Swipe(ctx, deviceID, 8, 420, 280, 420, 260)
	default:
		return fmt.Errorf("不支持的 iOS 按键 %q（支持 home、back、enter）", key)
	}
}

// Close clears cached session state.
func (d *WDADriver) Close() error {
	d.dropSession()
	return nil
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
