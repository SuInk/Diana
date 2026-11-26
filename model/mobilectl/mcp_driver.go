// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package mobilectl

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/internal/procgroup"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const DefaultMobileMCPCommand = "npx -y @mobilenext/mobile-mcp@latest"

// mcpToolCaller abstracts an MCP session so tests can inject deterministic fakes.
type mcpToolCaller interface {
	Call(ctx context.Context, name string, args map[string]any) (mcpToolOutput, error)
	Close() error
}

type mcpToolOutput struct {
	Structured json.RawMessage
	Text       string
	ImageBytes []byte
}

// MCPDriver drives `@mobilenext/mobile-mcp` (Apache-2.0) over MCP stdio.
type MCPDriver struct {
	caller mcpToolCaller
}

// NewMCPDriver builds a Driver backed by `@mobilenext/mobile-mcp` (or compatible mobile MCP stdio command).
func NewMCPDriver(commandLine string, timeout time.Duration) *MCPDriver {
	if strings.TrimSpace(commandLine) == "" {
		commandLine = DefaultMobileMCPCommand
	}
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	return &MCPDriver{caller: &stdioMCPSession{commandLine: commandLine, timeout: timeout}}
}

// NewMCPDriverWithCaller builds an MCPDriver from a custom caller (used in unit tests).
func NewMCPDriverWithCaller(caller mcpToolCaller) *MCPDriver {
	return &MCPDriver{caller: caller}
}

func (d *MCPDriver) ListDevices(ctx context.Context) ([]DeviceInfo, error) {
	res, err := d.caller.Call(ctx, "mobile_list_available_devices", map[string]any{})
	if err != nil {
		return nil, err
	}
	return parseMobileMCPDevices(res)
}

func parseMobileMCPDevices(res mcpToolOutput) ([]DeviceInfo, error) {
	raw := trimMCPJSON(res.Structured, res.Text)
	var wrapper struct {
		Devices []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Platform string `json:"platform"`
			Type     string `json:"type"`
			State    string `json:"state"`
		} `json:"devices"`
	}
	if len(raw) > 0 && json.Unmarshal(raw, &wrapper) == nil && len(wrapper.Devices) > 0 {
		out := make([]DeviceInfo, 0, len(wrapper.Devices))
		for _, dev := range wrapper.Devices {
			if strings.TrimSpace(dev.ID) == "" {
				continue
			}
			plat := normalizeMobilePlatform(firstNonEmptyString(dev.Platform, dev.Type))
			out = append(out, DeviceInfo{
				ID:       strings.TrimSpace(dev.ID),
				Name:     firstNonEmptyString(dev.Name, dev.ID),
				Platform: plat,
				State:    firstNonEmptyString(dev.State, "online"),
				Backend:  BackendMobileMCP,
			})
		}
		return out, nil
	}
	var direct []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Platform string `json:"platform"`
		State    string `json:"state"`
	}
	if len(raw) > 0 && json.Unmarshal(raw, &direct) == nil && len(direct) > 0 {
		out := make([]DeviceInfo, 0, len(direct))
		for _, dev := range direct {
			if strings.TrimSpace(dev.ID) == "" {
				continue
			}
			out = append(out, DeviceInfo{
				ID:       strings.TrimSpace(dev.ID),
				Name:     firstNonEmptyString(dev.Name, dev.ID),
				Platform: normalizeMobilePlatform(dev.Platform),
				State:    firstNonEmptyString(dev.State, "online"),
				Backend:  BackendMobileMCP,
			})
		}
		return out, nil
	}
	return nil, nil
}

func normalizeMobilePlatform(raw string) string {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if strings.Contains(lower, "ios") || strings.Contains(lower, "iphone") || strings.Contains(lower, "ipad") {
		return PlatformIOS
	}
	return PlatformAndroid
}

func trimMCPJSON(structured json.RawMessage, text string) []byte {
	if len(structured) > 0 && string(structured) != "null" {
		return structured
	}
	trimmed := strings.TrimSpace(text)
	if start := strings.IndexAny(trimmed, "{["); start >= 0 {
		return []byte(trimmed[start:])
	}
	return nil
}

func (d *MCPDriver) LaunchApp(ctx context.Context, deviceID, appID string) error {
	_, err := d.caller.Call(ctx, "mobile_launch_app", map[string]any{
		"device":      deviceID,
		"packageName": appID,
	})
	return err
}

func (d *MCPDriver) OpenURL(ctx context.Context, deviceID, rawURL string) error {
	_, err := d.caller.Call(ctx, "mobile_open_url", map[string]any{
		"device": deviceID,
		"url":    rawURL,
	})
	return err
}

func (d *MCPDriver) Screenshot(ctx context.Context, deviceID string) ([]byte, error) {
	res, err := d.caller.Call(ctx, "mobile_take_screenshot", map[string]any{
		"device": deviceID,
	})
	if err != nil {
		return nil, err
	}
	if len(res.ImageBytes) > 0 {
		return res.ImageBytes, nil
	}
	trimmed := strings.TrimSpace(res.Text)
	trimmed = strings.TrimPrefix(trimmed, "data:image/png;base64,")
	if decoded, err := base64.StdEncoding.DecodeString(trimmed); err == nil && len(decoded) > 0 {
		return decoded, nil
	}
	return nil, errors.New("mobile-mcp 未返回有效截图数据")
}

func (d *MCPDriver) Elements(ctx context.Context, deviceID string) ([]Element, int, int, error) {
	res, err := d.caller.Call(ctx, "mobile_list_elements_on_screen", map[string]any{
		"device": deviceID,
	})
	if err != nil {
		return nil, 0, 0, err
	}
	return parseMobileMCPElements(res)
}

func parseMobileMCPElements(res mcpToolOutput) ([]Element, int, int, error) {
	raw := trimMCPJSON(res.Structured, res.Text)
	type mcpCoords struct {
		X      float64 `json:"x"`
		Y      float64 `json:"y"`
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	}
	type mcpElem struct {
		Type        string    `json:"type"`
		Label       string    `json:"label"`
		Name        string    `json:"name"`
		Text        string    `json:"text"`
		Value       string    `json:"value"`
		Coordinates mcpCoords `json:"coordinates"`
	}
	var list []mcpElem
	var wrapped struct {
		Elements []mcpElem `json:"elements"`
	}
	if len(raw) > 0 {
		if json.Unmarshal(raw, &wrapped) == nil && len(wrapped.Elements) > 0 {
			list = wrapped.Elements
		} else {
			_ = json.Unmarshal(raw, &list)
		}
	}
	if len(list) == 0 && strings.TrimSpace(res.Text) != "" {
		// Fallback for line-oriented text dumps produced by newer mobile-mcp versions.
		var fallback []Element
		for idx, line := range strings.Split(res.Text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			fallback = append(fallback, Element{Index: idx + 1, Text: line, Width: 100, Height: 40})
		}
		return fallback, 0, 0, nil
	}
	out := make([]Element, 0, len(list))
	maxW, maxH := 0, 0
	for _, item := range list {
		x := int(item.Coordinates.X + 0.5)
		y := int(item.Coordinates.Y + 0.5)
		w := int(item.Coordinates.Width + 0.5)
		h := int(item.Coordinates.Height + 0.5)
		if w <= 0 {
			w = 40
		}
		if h <= 0 {
			h = 24
		}
		if x+w > maxW {
			maxW = x + w
		}
		if y+h > maxH {
			maxH = y + h
		}
		out = append(out, Element{
			Type:   item.Type,
			Label:  firstNonEmptyString(item.Label, item.Name),
			Text:   firstNonEmptyString(item.Text, item.Value),
			X:      x,
			Y:      y,
			Width:  w,
			Height: h,
		})
	}
	return out, maxW, maxH, nil
}

func (d *MCPDriver) Tap(ctx context.Context, deviceID string, x, y int) error {
	_, err := d.caller.Call(ctx, "mobile_click_on_screen_at_coordinates", map[string]any{
		"device": deviceID,
		"x":      x,
		"y":      y,
	})
	return err
}

func (d *MCPDriver) Swipe(ctx context.Context, deviceID string, x1, y1, x2, y2, durationMS int) error {
	dir := "up"
	dx := x2 - x1
	dy := y2 - y1
	if absInt(dx) > absInt(dy) {
		if dx < 0 {
			dir = "left"
		} else {
			dir = "right"
		}
	} else if dy > 0 {
		dir = "down"
	}
	_, err := d.caller.Call(ctx, "mobile_swipe_on_screen", map[string]any{
		"device":    deviceID,
		"direction": dir,
		"x":         x1,
		"y":         y1,
		"duration":  durationMS,
	})
	return err
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func (d *MCPDriver) TypeText(ctx context.Context, deviceID, text string) error {
	_, err := d.caller.Call(ctx, "mobile_type_keys", map[string]any{
		"device": deviceID,
		"text":   text,
		"submit": false,
	})
	return err
}

func (d *MCPDriver) PressKey(ctx context.Context, deviceID, key string) error {
	button := "HOME"
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "home":
		button = "HOME"
	case "back":
		button = "BACK"
	case "enter", "return":
		button = "ENTER"
	default:
		return fmt.Errorf("不支持的按键 %q（支持 home、back、enter）", key)
	}
	_, err := d.caller.Call(ctx, "mobile_press_button", map[string]any{
		"device": deviceID,
		"button": button,
	})
	return err
}

func (d *MCPDriver) Close() error {
	if d.caller == nil {
		return nil
	}
	return d.caller.Close()
}

type stdioMCPSession struct {
	commandLine string
	timeout     time.Duration

	mu      sync.Mutex
	session *mcpsdk.ClientSession
}

func (s *stdioMCPSession) active(ctx context.Context) (*mcpsdk.ClientSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil {
		return s.session, nil
	}
	parts := strings.Fields(s.commandLine)
	if len(parts) == 0 {
		return nil, errors.New("Mobile MCP 启动命令为空")
	}
	bin, err := exec.LookPath(parts[0])
	if err != nil {
		return nil, fmt.Errorf("未找到 Mobile MCP 启动程序 %q：%w", parts[0], err)
	}
	cmd := procgroup.Isolate(exec.Command(bin, parts[1:]...))
	cmd.Env = append(os.Environ(), "DO_NOT_TRACK=1")
	cmd.Stderr = io.Discard

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "diana-mobilectl", Version: "1"}, nil)
	connectCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	session, err := client.Connect(connectCtx, &mcpsdk.CommandTransport{Command: cmd, TerminateDuration: 2 * time.Second}, nil)
	if err != nil {
		return nil, fmt.Errorf("启动 Mobile MCP (%s) 失败：%w", s.commandLine, err)
	}
	s.session = session
	return session, nil
}

func (s *stdioMCPSession) drop(session *mcpsdk.ClientSession) {
	s.mu.Lock()
	if s.session == session {
		s.session = nil
	}
	s.mu.Unlock()
	_ = session.Close()
}

func (s *stdioMCPSession) Call(ctx context.Context, name string, args map[string]any) (mcpToolOutput, error) {
	session, err := s.active(ctx)
	if err != nil {
		return mcpToolOutput{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	res, err := session.CallTool(callCtx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		if ctx.Err() != nil {
			return mcpToolOutput{}, ctx.Err()
		}
		s.drop(session)
		return mcpToolOutput{}, fmt.Errorf("Mobile MCP 工具 %s 调用失败：%w", name, err)
	}
	var out mcpToolOutput
	if res.StructuredContent != nil {
		out.Structured, _ = json.Marshal(res.StructuredContent)
	}
	var texts []string
	for _, block := range res.Content {
		switch typed := block.(type) {
		case *mcpsdk.TextContent:
			texts = append(texts, typed.Text)
		case *mcpsdk.ImageContent:
			if len(out.ImageBytes) == 0 && len(typed.Data) > 0 {
				out.ImageBytes = typed.Data
			}
		}
	}
	out.Text = strings.Join(texts, "\n")
	if res.IsError {
		return out, fmt.Errorf("Mobile MCP %s 返回错误：%s", name, firstNonEmptyString(out.Text, string(out.Structured)))
	}
	return out, nil
}

func (s *stdioMCPSession) Close() error {
	s.mu.Lock()
	session := s.session
	s.session = nil
	s.mu.Unlock()
	if session != nil {
		return session.Close()
	}
	return nil
}
