// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package mobilectl

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	PlatformAndroid = "android"
	PlatformIOS     = "ios"

	BackendAuto      = "auto"
	BackendADB       = "adb"
	BackendWDA       = "wda"
	BackendSimctl    = "simctl"
	BackendMobileMCP = "mobile_mcp"

	observationTTL      = 3 * time.Minute
	defaultCommandLimit = 30 * time.Second
)

// DeviceInfo describes one reachable Android or iOS device, emulator, simulator, or container cloud-phone.
type DeviceInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	State    string `json:"state"`
	Backend  string `json:"backend,omitempty"`
	Virtual  bool   `json:"virtual,omitempty"`
	Locked   bool   `json:"locked,omitempty"`
}

// Element describes one visible UI node extracted from the accessibility tree.
type Element struct {
	Index  int    `json:"index"`
	Type   string `json:"type,omitempty"`
	Label  string `json:"label,omitempty"`
	Text   string `json:"text,omitempty"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// Center returns the tap coordinates at the center of the element bounds.
func (e Element) Center() (int, int) {
	return e.X + e.Width/2, e.Y + e.Height/2
}

// DisplayText returns a compact human/LLM readable text representation of an element.
func (e Element) DisplayText() string {
	label := strings.TrimSpace(e.Label)
	text := strings.TrimSpace(e.Text)
	switch {
	case label != "" && text != "" && label != text:
		return label + " · " + text
	case text != "":
		return text
	default:
		return label
	}
}

// ElementsSnapshot pairs extracted screen elements with an observation token.
type ElementsSnapshot struct {
	DeviceID    string    `json:"device_id"`
	Platform    string    `json:"platform,omitempty"`
	Observation int64     `json:"observation"`
	Width       int       `json:"width,omitempty"`
	Height      int       `json:"height,omitempty"`
	Elements    []Element `json:"elements"`
}

// Config configures how Controller connects to Android and/or iOS real devices or VMs/simulators.
type Config struct {
	Backend      string
	DeviceID     string
	ADBPath      string
	ADBAddress   string
	UnlockPIN    string
	StayAwakeUSB bool
	EmulatorPath string
	XcrunPath    string
	VMName       string
	VMHeadless   bool
	WDAURL       string
	MCPCommand   string
	HTTPClient   *http.Client
	Timeout      time.Duration
}

func (c Config) normalized() Config {
	out := c
	switch strings.ToLower(strings.TrimSpace(out.Backend)) {
	case BackendADB, BackendWDA, BackendSimctl, BackendMobileMCP:
		out.Backend = strings.ToLower(strings.TrimSpace(out.Backend))
	default:
		out.Backend = BackendAuto
	}
	out.DeviceID = strings.TrimSpace(out.DeviceID)
	out.ADBPath = strings.TrimSpace(out.ADBPath)
	out.ADBAddress = strings.TrimSpace(out.ADBAddress)
	out.UnlockPIN = strings.TrimSpace(out.UnlockPIN)
	out.EmulatorPath = strings.TrimSpace(out.EmulatorPath)
	out.XcrunPath = strings.TrimSpace(out.XcrunPath)
	out.VMName = strings.TrimSpace(out.VMName)
	out.WDAURL = strings.TrimRight(strings.TrimSpace(out.WDAURL), "/")
	out.MCPCommand = strings.TrimSpace(out.MCPCommand)
	if out.Timeout <= 0 {
		out.Timeout = defaultCommandLimit
	}
	return out
}

// Driver abstracts low-level mobile control across ADB, WebDriverAgent, and Mobile-MCP.
type Driver interface {
	ListDevices(ctx context.Context) ([]DeviceInfo, error)
	LaunchApp(ctx context.Context, deviceID, appID string) error
	OpenURL(ctx context.Context, deviceID, rawURL string) error
	Screenshot(ctx context.Context, deviceID string) ([]byte, error)
	Elements(ctx context.Context, deviceID string) ([]Element, int, int, error)
	Tap(ctx context.Context, deviceID string, x, y int) error
	Swipe(ctx context.Context, deviceID string, x1, y1, x2, y2 int, durationMS int) error
	TypeText(ctx context.Context, deviceID, text string) error
	PressKey(ctx context.Context, deviceID, key string) error
	Close() error
}

type cachedObservation struct {
	deviceID string
	at       time.Time
	width    int
	height   int
	elements map[int]Element
}

// Controller serializes physical phone operations and tracks element observations.
type Controller struct {
	cfg    Config
	driver Driver

	opMu sync.Mutex

	obsMu      sync.Mutex
	nextObs    int64
	latestObs  map[string]cachedObservation
	screenSize map[string][2]int
}

// NewController constructs a Controller for the configured backend.
func NewController(cfg Config) *Controller {
	cfg = cfg.normalized()
	var drv Driver
	switch cfg.Backend {
	case BackendADB:
		adb := NewADBDriverWithVM(cfg.ADBPath, cfg.ADBAddress, cfg.EmulatorPath, cfg.VMName, cfg.VMHeadless, cfg.Timeout)
		adb.SetUnlockOptions(cfg.UnlockPIN, cfg.StayAwakeUSB)
		drv = adb
	case BackendWDA:
		drv = NewWDADriver(cfg.WDAURL, cfg.HTTPClient, cfg.Timeout)
	case BackendSimctl:
		var wda *WDADriver
		if cfg.WDAURL != "" {
			wda = NewWDADriver(cfg.WDAURL, cfg.HTTPClient, cfg.Timeout)
		}
		drv = NewSimctlDriver(cfg.XcrunPath, cfg.VMName, wda, cfg.Timeout)
	case BackendMobileMCP:
		drv = NewMCPDriver(cfg.MCPCommand, cfg.Timeout)
	default:
		drv = newAutoDriver(cfg)
	}
	return NewControllerWithDriver(cfg, drv)
}

// NewControllerWithDriver wraps a custom Driver (used by unit tests and custom adapters).
func NewControllerWithDriver(cfg Config, drv Driver) *Controller {
	cfg = cfg.normalized()
	return &Controller{
		cfg:        cfg,
		driver:     drv,
		latestObs:  map[string]cachedObservation{},
		screenSize: map[string][2]int{},
	}
}

// WithExclusiveSession locks physical screen interaction for a multi-step flow (e.g. app launch + swipe patrol)
// so background patrols and interactive owner commands do not interleave taps on the same phone.
func (c *Controller) WithExclusiveSession(ctx context.Context, fn func(ctx context.Context) error) error {
	done := make(chan struct{})
	go func() {
		c.opMu.Lock()
		close(done)
	}()
	select {
	case <-done:
		defer c.opMu.Unlock()
		return fn(ctx)
	case <-ctx.Done():
		go func() {
			<-done
			c.opMu.Unlock()
		}()
		return ctx.Err()
	}
}

// Close releases underlying driver sessions.
func (c *Controller) Close() error {
	if c == nil || c.driver == nil {
		return nil
	}
	return c.driver.Close()
}

// ListDevices lists all online mobile devices.
func (c *Controller) ListDevices(ctx context.Context) ([]DeviceInfo, error) {
	if c == nil || c.driver == nil {
		return nil, errors.New("手机控制驱动未初始化")
	}
	return c.driver.ListDevices(ctx)
}

// ResolveDevice picks the requested device ID, configured default device, or first online device.
func (c *Controller) ResolveDevice(ctx context.Context, requestedID string) (DeviceInfo, error) {
	devices, err := c.ListDevices(ctx)
	if err != nil {
		return DeviceInfo{}, err
	}
	if len(devices) == 0 {
		return DeviceInfo{}, errors.New("未检测到在线手机，请检查安卓 ADB 连接、iPhone WebDriverAgent 地址或 Mobile MCP 状态")
	}
	want := strings.TrimSpace(requestedID)
	if want == "" {
		want = c.cfg.DeviceID
	}
	if want == "" {
		return devices[0], nil
	}
	for _, dev := range devices {
		if strings.EqualFold(strings.TrimSpace(dev.ID), want) {
			return dev, nil
		}
	}
	return DeviceInfo{}, fmt.Errorf("未找到指定手机设备 %q（当前在线 %d 台）", want, len(devices))
}

// LaunchApp opens an app by package name (Android) or bundle identifier (iOS).
func (c *Controller) LaunchApp(ctx context.Context, deviceID, appID string) (DeviceInfo, error) {
	dev, err := c.ResolveDevice(ctx, deviceID)
	if err != nil {
		return DeviceInfo{}, err
	}
	if err := c.driver.LaunchApp(ctx, dev.ID, strings.TrimSpace(appID)); err != nil {
		return dev, err
	}
	return dev, nil
}

// OpenURL opens a URL or deep link on the target device.
func (c *Controller) OpenURL(ctx context.Context, deviceID, rawURL string) (DeviceInfo, error) {
	dev, err := c.ResolveDevice(ctx, deviceID)
	if err != nil {
		return DeviceInfo{}, err
	}
	if err := c.driver.OpenURL(ctx, dev.ID, strings.TrimSpace(rawURL)); err != nil {
		return dev, err
	}
	return dev, nil
}

// Screenshot captures a PNG screenshot and records screen dimensions when available.
func (c *Controller) Screenshot(ctx context.Context, deviceID string) (DeviceInfo, []byte, error) {
	dev, err := c.ResolveDevice(ctx, deviceID)
	if err != nil {
		return DeviceInfo{}, nil, err
	}
	pngBytes, err := c.driver.Screenshot(ctx, dev.ID)
	if err != nil {
		return dev, nil, err
	}
	if w, h, ok := pngDimensions(pngBytes); ok {
		c.rememberSize(dev.ID, w, h)
	}
	return dev, pngBytes, nil
}

// Elements reads the current screen accessibility tree and assigns an observation ID.
func (c *Controller) Elements(ctx context.Context, deviceID string) (ElementsSnapshot, error) {
	dev, err := c.ResolveDevice(ctx, deviceID)
	if err != nil {
		return ElementsSnapshot{}, err
	}
	rawElements, width, height, err := c.driver.Elements(ctx, dev.ID)
	if err != nil {
		return ElementsSnapshot{}, err
	}
	numbered := make([]Element, 0, len(rawElements))
	byIndex := make(map[int]Element, len(rawElements))
	maxRight, maxBottom := width, height
	for idx, item := range rawElements {
		item.Index = idx + 1
		numbered = append(numbered, item)
		byIndex[item.Index] = item
		if r := item.X + item.Width; r > maxRight {
			maxRight = r
		}
		if b := item.Y + item.Height; b > maxBottom {
			maxBottom = b
		}
	}
	if width <= 0 {
		width = maxRight
	}
	if height <= 0 {
		height = maxBottom
	}
	if width > 0 && height > 0 {
		c.rememberSize(dev.ID, width, height)
	}
	c.obsMu.Lock()
	c.nextObs++
	obs := c.nextObs
	c.latestObs[dev.ID] = cachedObservation{
		deviceID: dev.ID,
		at:       time.Now(),
		width:    width,
		height:   height,
		elements: byIndex,
	}
	c.obsMu.Unlock()
	return ElementsSnapshot{
		DeviceID:    dev.ID,
		Platform:    dev.Platform,
		Observation: obs,
		Width:       width,
		Height:      height,
		Elements:    numbered,
	}, nil
}

// Tap taps explicit coordinates on the device screen.
func (c *Controller) Tap(ctx context.Context, deviceID string, x, y int) (DeviceInfo, error) {
	dev, err := c.ResolveDevice(ctx, deviceID)
	if err != nil {
		return DeviceInfo{}, err
	}
	return dev, c.driver.Tap(ctx, dev.ID, x, y)
}

// TapElement taps the center of an element from a recent Elements observation.
func (c *Controller) TapElement(ctx context.Context, deviceID string, observation int64, elementIndex int) (DeviceInfo, Element, error) {
	dev, err := c.ResolveDevice(ctx, deviceID)
	if err != nil {
		return DeviceInfo{}, Element{}, err
	}
	c.obsMu.Lock()
	snap, ok := c.latestObs[dev.ID]
	c.obsMu.Unlock()
	if !ok || time.Since(snap.at) > observationTTL {
		return dev, Element{}, errors.New("界面元素快照已过期，请先重新调用 elements 获取最新界面")
	}
	if observation > 0 && c.currentObsForDevice(dev.ID) != observation {
		return dev, Element{}, errors.New("observation 已过期，请使用最新一次 elements 返回的编号")
	}
	elem, exists := snap.elements[elementIndex]
	if !exists {
		return dev, Element{}, fmt.Errorf("未找到编号为 %d 的界面元素", elementIndex)
	}
	x, y := elem.Center()
	if err := c.driver.Tap(ctx, dev.ID, x, y); err != nil {
		return dev, elem, err
	}
	return dev, elem, nil
}

func (c *Controller) currentObsForDevice(deviceID string) int64 {
	c.obsMu.Lock()
	defer c.obsMu.Unlock()
	if _, ok := c.latestObs[deviceID]; !ok {
		return 0
	}
	return c.nextObs
}

// Swipe performs a custom coordinate swipe gesture.
func (c *Controller) Swipe(ctx context.Context, deviceID string, x1, y1, x2, y2, durationMS int) (DeviceInfo, error) {
	dev, err := c.ResolveDevice(ctx, deviceID)
	if err != nil {
		return DeviceInfo{}, err
	}
	if durationMS <= 0 {
		durationMS = 380
	}
	return dev, c.driver.Swipe(ctx, dev.ID, x1, y1, x2, y2, durationMS)
}

// SwipeDirection scrolls the feed in the requested direction ("up" swipes finger upward to see newer/lower content).
func (c *Controller) SwipeDirection(ctx context.Context, deviceID, direction string) (DeviceInfo, error) {
	dev, err := c.ResolveDevice(ctx, deviceID)
	if err != nil {
		return DeviceInfo{}, err
	}
	w, h := c.sizeFor(dev)
	cx := w / 2
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case "up", "next", "":
		return dev, c.driver.Swipe(ctx, dev.ID, cx, int(float64(h)*0.76), cx, int(float64(h)*0.24), 420)
	case "down", "prev":
		return dev, c.driver.Swipe(ctx, dev.ID, cx, int(float64(h)*0.26), cx, int(float64(h)*0.76), 420)
	case "left":
		cy := h / 2
		return dev, c.driver.Swipe(ctx, dev.ID, int(float64(w)*0.82), cy, int(float64(w)*0.18), cy, 350)
	case "right":
		cy := h / 2
		return dev, c.driver.Swipe(ctx, dev.ID, int(float64(w)*0.18), cy, int(float64(w)*0.82), cy, 350)
	default:
		return dev, fmt.Errorf("不支持的滑动方向 %q（可选 up、down、left、right）", direction)
	}
}

// TypeText enters text into the currently focused mobile input field.
func (c *Controller) TypeText(ctx context.Context, deviceID, text string) (DeviceInfo, error) {
	dev, err := c.ResolveDevice(ctx, deviceID)
	if err != nil {
		return DeviceInfo{}, err
	}
	return dev, c.driver.TypeText(ctx, dev.ID, text)
}

// PressKey triggers a system button ("home", "back", "enter").
func (c *Controller) PressKey(ctx context.Context, deviceID, key string) (DeviceInfo, error) {
	dev, err := c.ResolveDevice(ctx, deviceID)
	if err != nil {
		return DeviceInfo{}, err
	}
	return dev, c.driver.PressKey(ctx, dev.ID, key)
}

func (c *Controller) rememberSize(deviceID string, w, h int) {
	if w <= 0 || h <= 0 {
		return
	}
	c.obsMu.Lock()
	c.screenSize[deviceID] = [2]int{w, h}
	c.obsMu.Unlock()
}

func (c *Controller) sizeFor(dev DeviceInfo) (int, int) {
	c.obsMu.Lock()
	size, ok := c.screenSize[dev.ID]
	c.obsMu.Unlock()
	if ok && size[0] > 0 && size[1] > 0 {
		return size[0], size[1]
	}
	if dev.Platform == PlatformIOS {
		return 393, 852
	}
	return 1080, 2400
}

func pngDimensions(data []byte) (int, int, bool) {
	// PNG header (8 bytes) + IHDR length (4) + "IHDR" (4) + width (4) + height (4) = 24 bytes
	if len(data) < 24 || !bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		return 0, 0, false
	}
	w := int(binary.BigEndian.Uint32(data[16:20]))
	h := int(binary.BigEndian.Uint32(data[20:24]))
	if w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

// autoDriver combines Android ADB, iOS WebDriverAgent, and macOS iOS Simulator transparently.
type autoDriver struct {
	adb    *ADBDriver
	wda    *WDADriver
	simctl *SimctlDriver

	mu       sync.Mutex
	byDevice map[string]string // deviceID -> BackendADB | BackendWDA | BackendSimctl
}

func newAutoDriver(cfg Config) *autoDriver {
	var wda *WDADriver
	if cfg.WDAURL != "" {
		wda = NewWDADriver(cfg.WDAURL, cfg.HTTPClient, cfg.Timeout)
	}
	var simctl *SimctlDriver
	if cfg.XcrunPath != "" || wda == nil {
		simctl = NewSimctlDriver(cfg.XcrunPath, cfg.VMName, wda, cfg.Timeout)
	}
	adb := NewADBDriverWithVM(cfg.ADBPath, cfg.ADBAddress, cfg.EmulatorPath, cfg.VMName, cfg.VMHeadless, cfg.Timeout)
	adb.SetUnlockOptions(cfg.UnlockPIN, cfg.StayAwakeUSB)
	return &autoDriver{
		adb:      adb,
		wda:      wda,
		simctl:   simctl,
		byDevice: map[string]string{},
	}
}

func (d *autoDriver) ListDevices(ctx context.Context) ([]DeviceInfo, error) {
	var all []DeviceInfo
	var errs []string
	if d.adb != nil {
		if list, err := d.adb.ListDevices(ctx); err == nil {
			d.mu.Lock()
			for _, item := range list {
				d.byDevice[item.ID] = BackendADB
			}
			d.mu.Unlock()
			all = append(all, list...)
		} else {
			errs = append(errs, err.Error())
		}
	}
	if d.wda != nil {
		if list, err := d.wda.ListDevices(ctx); err == nil {
			d.mu.Lock()
			for _, item := range list {
				d.byDevice[item.ID] = BackendWDA
			}
			d.mu.Unlock()
			all = append(all, list...)
		} else {
			errs = append(errs, err.Error())
		}
	}
	if d.simctl != nil && len(all) == 0 {
		if list, err := d.simctl.ListDevices(ctx); err == nil && len(list) > 0 {
			d.mu.Lock()
			for _, item := range list {
				d.byDevice[item.ID] = BackendSimctl
			}
			d.mu.Unlock()
			all = append(all, list...)
		}
	}
	if len(all) == 0 && len(errs) > 0 {
		return nil, errors.New(strings.Join(errs, "；"))
	}
	return all, nil
}

func (d *autoDriver) target(ctx context.Context, deviceID string) (Driver, error) {
	d.mu.Lock()
	backend := d.byDevice[deviceID]
	d.mu.Unlock()
	if backend == "" {
		_, _ = d.ListDevices(ctx)
		d.mu.Lock()
		backend = d.byDevice[deviceID]
		d.mu.Unlock()
	}
	switch backend {
	case BackendWDA:
		if d.wda != nil {
			return d.wda, nil
		}
	case BackendSimctl:
		if d.simctl != nil {
			return d.simctl, nil
		}
	}
	if d.adb != nil {
		return d.adb, nil
	}
	if d.wda != nil {
		return d.wda, nil
	}
	if d.simctl != nil {
		return d.simctl, nil
	}
	return nil, errors.New("没有可用的手机或虚拟机控制驱动")
}

func (d *autoDriver) LaunchApp(ctx context.Context, deviceID, appID string) error {
	drv, err := d.target(ctx, deviceID)
	if err != nil {
		return err
	}
	return drv.LaunchApp(ctx, deviceID, appID)
}

func (d *autoDriver) OpenURL(ctx context.Context, deviceID, rawURL string) error {
	drv, err := d.target(ctx, deviceID)
	if err != nil {
		return err
	}
	return drv.OpenURL(ctx, deviceID, rawURL)
}

func (d *autoDriver) Screenshot(ctx context.Context, deviceID string) ([]byte, error) {
	drv, err := d.target(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	return drv.Screenshot(ctx, deviceID)
}

func (d *autoDriver) Elements(ctx context.Context, deviceID string) ([]Element, int, int, error) {
	drv, err := d.target(ctx, deviceID)
	if err != nil {
		return nil, 0, 0, err
	}
	return drv.Elements(ctx, deviceID)
}

func (d *autoDriver) Tap(ctx context.Context, deviceID string, x, y int) error {
	drv, err := d.target(ctx, deviceID)
	if err != nil {
		return err
	}
	return drv.Tap(ctx, deviceID, x, y)
}

func (d *autoDriver) Swipe(ctx context.Context, deviceID string, x1, y1, x2, y2, durationMS int) error {
	drv, err := d.target(ctx, deviceID)
	if err != nil {
		return err
	}
	return drv.Swipe(ctx, deviceID, x1, y1, x2, y2, durationMS)
}

func (d *autoDriver) TypeText(ctx context.Context, deviceID, text string) error {
	drv, err := d.target(ctx, deviceID)
	if err != nil {
		return err
	}
	return drv.TypeText(ctx, deviceID, text)
}

func (d *autoDriver) PressKey(ctx context.Context, deviceID, key string) error {
	drv, err := d.target(ctx, deviceID)
	if err != nil {
		return err
	}
	return drv.PressKey(ctx, deviceID, key)
}

func (d *autoDriver) Close() error {
	if d.simctl != nil {
		_ = d.simctl.Close()
	}
	if d.wda != nil {
		_ = d.wda.Close()
	}
	if d.adb != nil {
		_ = d.adb.Close()
	}
	return nil
}
