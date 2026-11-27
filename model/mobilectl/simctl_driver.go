// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package mobilectl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/internal/procgroup"
)

// SimctlDriver drives macOS Xcode iOS Simulators via `xcrun simctl` (with optional WDA delegation for touch gestures).
type SimctlDriver struct {
	xcrunPath string
	vmName    string
	wda       *WDADriver
	timeout   time.Duration

	mu       sync.Mutex
	bootOnce bool
}

// NewSimctlDriver creates an iOS Simulator driver backed by `xcrun simctl`.
func NewSimctlDriver(xcrunPath, vmName string, wda *WDADriver, timeout time.Duration) *SimctlDriver {
	xcrunPath = strings.TrimSpace(xcrunPath)
	if xcrunPath == "" {
		xcrunPath = "xcrun"
	}
	if timeout <= 0 {
		timeout = defaultCommandLimit
	}
	return &SimctlDriver{
		xcrunPath: xcrunPath,
		vmName:    strings.TrimSpace(vmName),
		wda:       wda,
		timeout:   timeout,
	}
}

func (d *SimctlDriver) run(ctx context.Context, args ...string) ([]byte, error) {
	bin, err := exec.LookPath(d.xcrunPath)
	if err != nil {
		return nil, fmt.Errorf("未找到 xcrun 命令（%s），iOS 模拟器仅在安装了 Xcode 的 macOS 上可用", d.xcrunPath)
	}
	callCtx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	fullArgs := append([]string{"simctl"}, args...)
	cmd := procgroup.CommandContext(callCtx, bin, fullArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errText := strings.TrimSpace(stderr.String())
		if errText == "" {
			errText = strings.TrimSpace(stdout.String())
		}
		if errText != "" {
			return nil, fmt.Errorf("xcrun simctl %s 失败：%s", strings.Join(args, " "), errText)
		}
		return nil, fmt.Errorf("xcrun simctl %s 失败：%w", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

type simctlDeviceEntry struct {
	UDID        string `json:"udid"`
	Name        string `json:"name"`
	State       string `json:"state"`
	IsAvailable *bool  `json:"isAvailable,omitempty"`
}

func parseSimctlDevicesJSON(raw []byte) (booted []DeviceInfo, shutdownMatch string, err error) {
	var payload struct {
		Devices map[string][]simctlDeviceEntry `json:"devices"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, "", fmt.Errorf("解析 simctl 设备列表失败：%w", err)
	}
	for runtimeKey, list := range payload.Devices {
		lowerRuntime := strings.ToLower(runtimeKey)
		if strings.Contains(lowerRuntime, "watchos") || strings.Contains(lowerRuntime, "tvos") {
			continue
		}
		for _, item := range list {
			if item.IsAvailable != nil && !*item.IsAvailable {
				continue
			}
			if strings.EqualFold(item.State, "Booted") && strings.TrimSpace(item.UDID) != "" {
				booted = append(booted, DeviceInfo{
					ID:       strings.TrimSpace(item.UDID),
					Name:     firstNonEmptyString(strings.TrimSpace(item.Name), item.UDID),
					Platform: PlatformIOS,
					State:    "online",
					Backend:  BackendSimctl,
					Virtual:  true,
				})
			}
		}
	}
	return booted, shutdownMatch, nil
}

func findMatchingShutdownSimulator(raw []byte, target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	var payload struct {
		Devices map[string][]simctlDeviceEntry `json:"devices"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	for _, list := range payload.Devices {
		for _, item := range list {
			if item.IsAvailable != nil && !*item.IsAvailable {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(item.UDID), target) || strings.EqualFold(strings.TrimSpace(item.Name), target) {
				return strings.TrimSpace(item.UDID)
			}
		}
	}
	return ""
}

// ListDevices lists booted iOS Simulators, auto-booting `vmName` if configured and none are booted yet.
func (d *SimctlDriver) ListDevices(ctx context.Context) ([]DeviceInfo, error) {
	out, err := d.run(ctx, "list", "devices", "--json")
	if err != nil {
		return nil, err
	}
	booted, _, err := parseSimctlDevicesJSON(out)
	if err != nil {
		return nil, err
	}
	if len(booted) > 0 || d.vmName == "" {
		return booted, nil
	}
	d.mu.Lock()
	alreadyBooted := d.bootOnce
	d.bootOnce = true
	d.mu.Unlock()
	if alreadyBooted {
		return nil, nil
	}
	udid := findMatchingShutdownSimulator(out, d.vmName)
	if udid == "" {
		udid = d.vmName
	}
	if _, err := d.run(ctx, "boot", udid); err != nil && !strings.Contains(strings.ToLower(err.Error()), "current state: booted") {
		return nil, err
	}
	_, _ = d.run(ctx, "bootstatus", udid, "-b")
	outAfter, err := d.run(ctx, "list", "devices", "--json")
	if err != nil {
		return nil, err
	}
	booted, _, err = parseSimctlDevicesJSON(outAfter)
	return booted, err
}

func (d *SimctlDriver) targetID(deviceID string) string {
	if strings.TrimSpace(deviceID) != "" {
		return strings.TrimSpace(deviceID)
	}
	return "booted"
}

// LaunchApp launches a bundle identifier inside the iOS Simulator.
func (d *SimctlDriver) LaunchApp(ctx context.Context, deviceID, appID string) error {
	bundle := strings.TrimSpace(appID)
	if bundle == "" || !isSafePackageToken(bundle) {
		return fmt.Errorf("无效的 iOS Bundle ID：%q", appID)
	}
	_, err := d.run(ctx, "launch", d.targetID(deviceID), bundle)
	return err
}

// OpenURL opens an HTTP URL or custom scheme inside the iOS Simulator.
func (d *SimctlDriver) OpenURL(ctx context.Context, deviceID, rawURL string) error {
	clean := strings.TrimSpace(rawURL)
	if clean == "" {
		return errors.New("链接不能为空")
	}
	_, err := d.run(ctx, "openurl", d.targetID(deviceID), clean)
	return err
}

// Screenshot captures a PNG screenshot from the simulator framebuffer via `simctl io screenshot`.
// 新版 simctl 把 `-` 当成文件名写进当前目录而不是写到标准输出，所以先落到临时文件再读回。
func (d *SimctlDriver) Screenshot(ctx context.Context, deviceID string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "diana-simctl-shot-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "screen.png")
	if _, err := d.run(ctx, "io", d.targetID(deviceID), "screenshot", "--type=png", path); err != nil {
		return nil, err
	}
	out, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 simctl 截图失败：%w", err)
	}
	magic := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	idx := bytes.Index(out, magic)
	if idx < 0 {
		return nil, errors.New("simctl screenshot 未返回有效 PNG 图像")
	}
	return out[idx:], nil
}

// Elements delegates to WDA when configured; pure simctl has no accessibility dump CLI, so returning empty triggers vision fallback.
func (d *SimctlDriver) Elements(ctx context.Context, deviceID string) ([]Element, int, int, error) {
	if d.wda != nil {
		return d.wda.Elements(ctx, deviceID)
	}
	return nil, 0, 0, nil
}

// Tap delegates to WDA when attached to the simulator.
func (d *SimctlDriver) Tap(ctx context.Context, deviceID string, x, y int) error {
	if d.wda != nil {
		return d.wda.Tap(ctx, deviceID, x, y)
	}
	return errors.New("纯 simctl 模式不支持坐标点击，请同时配置 WebDriverAgent 地址或改用 Mobile MCP 驱动")
}

// Swipe delegates to WDA when attached to the simulator.
func (d *SimctlDriver) Swipe(ctx context.Context, deviceID string, x1, y1, x2, y2, durationMS int) error {
	if d.wda != nil {
		return d.wda.Swipe(ctx, deviceID, x1, y1, x2, y2, durationMS)
	}
	return errors.New("纯 simctl 模式不支持滑动手势，请同时配置 WebDriverAgent 地址或改用 Mobile MCP 驱动")
}

// TypeText delegates to WDA when attached to the simulator.
func (d *SimctlDriver) TypeText(ctx context.Context, deviceID, text string) error {
	if d.wda != nil {
		return d.wda.TypeText(ctx, deviceID, text)
	}
	return errors.New("纯 simctl 模式不支持键盘输入，请同时配置 WebDriverAgent 地址或改用 Mobile MCP 驱动")
}

// PressKey delegates to WDA if available; for "home" without WDA it launches SpringBoard / is a safe no-op.
func (d *SimctlDriver) PressKey(ctx context.Context, deviceID, key string) error {
	if d.wda != nil {
		return d.wda.PressKey(ctx, deviceID, key)
	}
	if strings.EqualFold(strings.TrimSpace(key), "home") {
		_, _ = d.run(ctx, "launch", d.targetID(deviceID), "com.apple.springboard")
		return nil
	}
	return errors.New("纯 simctl 模式仅支持 home 操作，其余按键需配合 WebDriverAgent 或 Mobile MCP")
}

func (d *SimctlDriver) Close() error {
	if d.wda != nil {
		return d.wda.Close()
	}
	return nil
}
