// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package mobilectl

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/internal/procgroup"
)

var adbBoundsPattern = regexp.MustCompile(`^\[(\d+),(\d+)\]\[(\d+),(\d+)\]$`)

// ADBDriver controls Android phones, emulators (AVD/MuMu/BlueStacks), and container VMs (Redroid/Waydroid) via ADB.
type ADBDriver struct {
	adbPath      string
	adbAddress   string
	unlockPIN    string
	stayAwakeUSB bool
	emulatorPath string
	vmName       string
	vmHeadless   bool
	timeout      time.Duration

	mu             sync.Mutex
	connected      bool
	bootedVM       bool
	stayAwakeSet   map[string]bool
	failedPINByDev map[string]string
}

// NewADBDriver creates an Android ADB driver.
func NewADBDriver(adbPath, adbAddress string, timeout time.Duration) *ADBDriver {
	return NewADBDriverWithVM(adbPath, adbAddress, "", "", false, timeout)
}

// NewADBDriverWithVM creates an Android ADB driver with optional AVD emulator auto-start support.
func NewADBDriverWithVM(adbPath, adbAddress, emulatorPath, vmName string, vmHeadless bool, timeout time.Duration) *ADBDriver {
	adbPath = strings.TrimSpace(adbPath)
	if adbPath == "" {
		adbPath = "adb"
	}
	if timeout <= 0 {
		timeout = defaultCommandLimit
	}
	return &ADBDriver{
		adbPath:        adbPath,
		adbAddress:     strings.TrimSpace(adbAddress),
		emulatorPath:   strings.TrimSpace(emulatorPath),
		vmName:         strings.TrimSpace(vmName),
		vmHeadless:     vmHeadless,
		timeout:        timeout,
		stayAwakeSet:   map[string]bool{},
		failedPINByDev: map[string]string{},
	}
}

// SetUnlockOptions configures optional PIN/password auto-unlock and USB stay-awake behavior.
func (d *ADBDriver) SetUnlockOptions(pin string, stayAwakeUSB bool) {
	d.mu.Lock()
	d.unlockPIN = strings.TrimSpace(pin)
	d.stayAwakeUSB = stayAwakeUSB
	d.mu.Unlock()
}

func resolveADBBinary(configured string) (string, error) {
	if envPath := strings.TrimSpace(os.Getenv("DIANA_ADB_PATH")); envPath != "" && (configured == "" || configured == "adb") {
		if bin, err := exec.LookPath(envPath); err == nil {
			return bin, nil
		}
	}
	if bin, err := exec.LookPath(configured); err == nil {
		return bin, nil
	}
	if configured == "" || configured == "adb" {
		var candidates []string
		for _, envKey := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
			if sdk := strings.TrimSpace(os.Getenv(envKey)); sdk != "" {
				candidates = append(candidates, filepath.Join(sdk, "platform-tools", "adb"))
			}
		}
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			candidates = append(candidates,
				filepath.Join(home, "Library", "Android", "sdk", "platform-tools", "adb"),
				filepath.Join(home, "Android", "Sdk", "platform-tools", "adb"),
			)
		}
		candidates = append(candidates, "/opt/homebrew/bin/adb", "/usr/local/bin/adb")
		for _, c := range candidates {
			if bin, err := exec.LookPath(c); err == nil {
				return bin, nil
			}
		}
	}
	return "", fmt.Errorf("未找到 adb 命令（%s），请安装 Android Platform Tools", configured)
}

func (d *ADBDriver) run(ctx context.Context, deviceID string, args ...string) ([]byte, error) {
	bin, err := resolveADBBinary(d.adbPath)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	var fullArgs []string
	if strings.TrimSpace(deviceID) != "" {
		fullArgs = append(fullArgs, "-s", strings.TrimSpace(deviceID))
	}
	fullArgs = append(fullArgs, args...)

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
			return nil, fmt.Errorf("adb %s 失败：%s", strings.Join(args, " "), errText)
		}
		return nil, fmt.Errorf("adb %s 失败：%w", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

func (d *ADBDriver) ensureWirelessConnected(ctx context.Context) {
	if d.adbAddress == "" {
		return
	}
	d.mu.Lock()
	already := d.connected
	d.mu.Unlock()
	if already {
		return
	}
	if out, err := d.run(ctx, "", "connect", d.adbAddress); err == nil {
		lower := strings.ToLower(string(out))
		if strings.Contains(lower, "connected to") || strings.Contains(lower, "already connected") {
			d.mu.Lock()
			d.connected = true
			d.mu.Unlock()
		}
	}
}

// ListDevices parses `adb devices -l` for ready Android devices or emulators, auto-starting an AVD VM if configured.
func (d *ADBDriver) ListDevices(ctx context.Context) ([]DeviceInfo, error) {
	d.ensureWirelessConnected(ctx)
	out, err := d.run(ctx, "", "devices", "-l")
	if err != nil {
		return nil, err
	}
	devices, unauthorized := parseADBDevices(string(out))
	if len(devices) > 0 {
		for i := range devices {
			d.enrichDeviceMetadata(ctx, &devices[i])
		}
		return devices, nil
	}
	if len(unauthorized) > 0 {
		return nil, fmt.Errorf("检测到安卓手机（%s）尚未授权 USB 调试，请解锁手机并在弹窗上点击「允许 USB 调试」", strings.Join(unauthorized, "、"))
	}
	if d.vmName == "" {
		return devices, nil
	}
	if err := d.tryBootAndroidAVD(ctx); err != nil {
		return nil, err
	}
	outAfter, err := d.run(ctx, "", "devices", "-l")
	if err != nil {
		return nil, err
	}
	devices, _ = parseADBDevices(string(outAfter))
	for i := range devices {
		d.enrichDeviceMetadata(ctx, &devices[i])
	}
	return devices, nil
}

func (d *ADBDriver) enrichDeviceMetadata(ctx context.Context, dev *DeviceInfo) {
	probeCtx, cancel := context.WithTimeout(ctx, 14*time.Second)
	defer cancel()
	if !dev.Virtual {
		if out, err := d.run(probeCtx, dev.ID, "shell", "getprop ro.vendor.oplus.market.name; getprop ro.product.marketname; getprop ro.config.marketing_name"); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				market := strings.TrimSpace(line)
				if market != "" && !strings.EqualFold(market, dev.Name) {
					dev.Name = fmt.Sprintf("%s (%s)", market, dev.Name)
					break
				}
			}
		}
	}
	dev.Locked = d.wakeAndCheckLocked(probeCtx, dev.ID)
}

func (d *ADBDriver) wakeAndCheckLocked(ctx context.Context, deviceID string) bool {
	d.mu.Lock()
	pin := d.unlockPIN
	stayAwake := d.stayAwakeUSB
	alreadyStayAwake := d.stayAwakeSet[deviceID]
	blockedPIN := d.failedPINByDev[deviceID]
	d.mu.Unlock()

	if stayAwake && !alreadyStayAwake && deviceID != "" {
		if _, err := d.run(ctx, deviceID, "shell", "svc", "power", "stayon", "usb"); err == nil {
			d.mu.Lock()
			d.stayAwakeSet[deviceID] = true
			d.mu.Unlock()
		}
	}

	out, err := d.run(ctx, deviceID, "shell", "dumpsys power | grep mWakefulness=; dumpsys window policy | grep showing=")
	if err != nil {
		return false
	}
	text := string(out)
	needWake := strings.Contains(text, "mWakefulness=Asleep") || strings.Contains(text, "mWakefulness=Dozing")
	if needWake {
		_, _ = d.run(ctx, deviceID, "shell", "input", "keyevent", "224")
		time.Sleep(350 * time.Millisecond)
	}
	if needWake || strings.Contains(text, "showing=true") {
		_, _ = d.run(ctx, deviceID, "shell", "wm dismiss-keyguard; input swipe 540 1900 540 650 260")
		time.Sleep(600 * time.Millisecond)
		if out2, err2 := d.run(ctx, deviceID, "shell", "dumpsys window policy | grep showing="); err2 == nil {
			text = string(out2)
		}
	}
	if !strings.Contains(text, "showing=true") {
		if blockedPIN != "" {
			d.mu.Lock()
			delete(d.failedPINByDev, deviceID)
			d.mu.Unlock()
		}
		return false
	}
	// Still locked with PIN/password: try configured PIN at most ONCE per unique value to avoid phone lockout!
	if pin == "" || pin == blockedPIN || !isSafeASCIIUnlockPIN(pin) {
		return true
	}
	submitted := d.enterLockscreenPIN(ctx, deviceID, pin)
	if !submitted || ctx.Err() != nil {
		return true
	}
	for range 5 {
		time.Sleep(400 * time.Millisecond)
		if out3, err3 := d.run(ctx, deviceID, "shell", "dumpsys window policy | grep showing="); err3 == nil {
			if !strings.Contains(string(out3), "showing=true") {
				return false
			}
		}
	}
	if ctx.Err() != nil {
		return true
	}
	// Circuit breaker: record failed PIN so background loops never trigger repeated wrong-PIN lockouts.
	d.mu.Lock()
	d.failedPINByDev[deviceID] = pin
	d.mu.Unlock()
	return true
}

// enterLockscreenPIN handles both OEM custom secure numeric keypads (ColorOS/HyperOS/OneUI, including
// Face-Unlock's intermediate "输入密码" button) by tapping real digit node coordinates, and falls back to `input text`.
func (d *ADBDriver) enterLockscreenPIN(ctx context.Context, deviceID, pin string) bool {
	elems, w, h, err := d.Elements(ctx, deviceID)
	if err == nil && !hasPasswordButtonOrKeypad(elems) {
		cx, y1, y2 := 720, 2400, 850
		if w > 0 && h > 0 {
			cx, y1, y2 = w/2, h*78/100, h*28/100
		}
		_, _ = d.run(ctx, deviceID, "shell", fmt.Sprintf("input swipe %d %d %d %d 260", cx, y1, cx, y2))
		time.Sleep(600 * time.Millisecond)
		if nextElems, _, _, err2 := d.Elements(ctx, deviceID); err2 == nil {
			elems = nextElems
		}
	}
	if err == nil {
		for _, el := range elems {
			label := strings.TrimSpace(firstNonEmptyADB(el.Text, el.Label))
			if label == "输入密码" || label == "使用密码" || strings.EqualFold(label, "Enter password") {
				cx, cy := el.Center()
				if _, tapErr := d.run(ctx, deviceID, "shell", "input", "tap", strconv.Itoa(cx), strconv.Itoa(cy)); tapErr != nil {
					return false
				}
				time.Sleep(550 * time.Millisecond)
				if nextElems, _, _, err2 := d.Elements(ctx, deviceID); err2 == nil {
					elems = nextElems
				}
				break
			}
		}
		if tapScript, ok := buildKeypadTapScript(elems, pin); ok {
			_, runErr := d.run(ctx, deviceID, "shell", tapScript)
			return runErr == nil
		}
	}
	if ctx.Err() != nil {
		return false
	}
	if _, err1 := d.run(ctx, deviceID, "shell", "input", "text", shellSingleQuote(pin)); err1 != nil {
		return false
	}
	_, err2 := d.run(ctx, deviceID, "shell", "input", "keyevent", "66")
	return err2 == nil
}

func hasPasswordButtonOrKeypad(elems []Element) bool {
	digits := 0
	for _, el := range elems {
		label := strings.TrimSpace(firstNonEmptyADB(el.Text, el.Label))
		if label == "输入密码" || label == "使用密码" || strings.EqualFold(label, "Enter password") {
			return true
		}
		if len(label) == 1 && label[0] >= '0' && label[0] <= '9' {
			digits++
		}
	}
	return digits >= 3
}

func buildKeypadTapScript(elems []Element, pin string) (string, bool) {
	digitCoords := map[rune][2]int{}
	var confirmCoord *[2]int
	var deleteCoord *[2]int
	for _, el := range elems {
		for _, candidate := range []string{strings.TrimSpace(el.Label), strings.TrimSpace(el.Text)} {
			if len(candidate) == 1 && candidate[0] >= '0' && candidate[0] <= '9' {
				cx, cy := el.Center()
				digitCoords[rune(candidate[0])] = [2]int{cx, cy}
			}
			if candidate == "确认" || candidate == "完成" || strings.EqualFold(candidate, "OK") {
				cx, cy := el.Center()
				pt := [2]int{cx, cy}
				confirmCoord = &pt
			}
			if candidate == "删除" || strings.EqualFold(candidate, "Delete") {
				cx, cy := el.Center()
				pt := [2]int{cx, cy}
				deleteCoord = &pt
			}
		}
	}
	var cmds []string
	// Clear any leftover partial input if the keypad's delete button is already visible.
	if deleteCoord != nil {
		for range 6 {
			cmds = append(cmds, fmt.Sprintf("input tap %d %d", deleteCoord[0], deleteCoord[1]))
		}
	}
	for _, ch := range pin {
		pt, ok := digitCoords[ch]
		if !ok {
			return "", false
		}
		cmds = append(cmds, fmt.Sprintf("input tap %d %d", pt[0], pt[1]))
	}
	if len(cmds) == 0 {
		return "", false
	}
	if confirmCoord != nil {
		cmds = append(cmds, fmt.Sprintf("input tap %d %d", confirmCoord[0], confirmCoord[1]))
	}
	return strings.Join(cmds, "; sleep 0.12; "), true
}

func firstNonEmptyADB(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func isSafeASCIIUnlockPIN(pin string) bool {
	if len(pin) == 0 || len(pin) > 32 {
		return false
	}
	for _, r := range pin {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}

func (d *ADBDriver) tryBootAndroidAVD(ctx context.Context) error {
	d.mu.Lock()
	already := d.bootedVM
	d.bootedVM = true
	d.mu.Unlock()
	if already {
		return nil
	}
	emuBin := d.resolveEmulatorBinary()
	if emuBin == "" {
		return fmt.Errorf("未找到 Android emulator 命令，无法自动拉起安卓虚拟机 %q", d.vmName)
	}
	args := []string{"-avd", d.vmName, "-no-audio", "-no-boot-anim"}
	if d.vmHeadless {
		args = append(args, "-no-window")
	}
	cmd := procgroup.Isolate(exec.Command(emuBin, args...))
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动安卓虚拟机 %q 失败：%w", d.vmName, err)
	}
	// Wait briefly for ADB daemon to see the booted emulator.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if out, err := d.run(ctx, "", "devices", "-l"); err == nil {
			if booted, _ := parseADBDevices(string(out)); len(booted) > 0 {
				return nil
			}
		}
		timer := time.NewTimer(600 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

func (d *ADBDriver) resolveEmulatorBinary() string {
	if d.emulatorPath != "" {
		if bin, err := exec.LookPath(d.emulatorPath); err == nil {
			return bin
		}
	}
	if bin, err := exec.LookPath("emulator"); err == nil {
		return bin
	}
	for _, envKey := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
		if sdk := strings.TrimSpace(os.Getenv(envKey)); sdk != "" {
			candidate := filepath.Join(sdk, "emulator", "emulator")
			if bin, err := exec.LookPath(candidate); err == nil {
				return bin
			}
		}
	}
	return ""
}

func parseADBDevices(raw string) ([]DeviceInfo, []string) {
	var devices []DeviceInfo
	var unauthorized []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of devices") || strings.HasPrefix(line, "*") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[1] == "unauthorized" {
			unauthorized = append(unauthorized, fields[0])
			continue
		}
		if fields[1] != "device" {
			continue
		}
		id := fields[0]
		name := id
		virtual := isVirtualADBSerial(id, line)
		for _, field := range fields[2:] {
			if after, ok := strings.CutPrefix(field, "model:"); ok && after != "" {
				name = strings.ReplaceAll(after, "_", " ")
				break
			}
		}
		devices = append(devices, DeviceInfo{
			ID:       id,
			Name:     name,
			Platform: PlatformAndroid,
			State:    "online",
			Backend:  BackendADB,
			Virtual:  virtual,
		})
	}
	return devices, unauthorized
}

func isVirtualADBSerial(id, fullLine string) bool {
	lowerID := strings.ToLower(strings.TrimSpace(id))
	lowerLine := strings.ToLower(fullLine)
	if strings.HasPrefix(lowerID, "emulator-") ||
		strings.HasPrefix(lowerID, "127.0.0.1:") ||
		strings.HasPrefix(lowerID, "localhost:") ||
		strings.HasPrefix(lowerID, "192.168.240.") {
		return true
	}
	return strings.Contains(lowerLine, "sdk_gphone") ||
		strings.Contains(lowerLine, "generic_") ||
		strings.Contains(lowerLine, "redroid") ||
		strings.Contains(lowerLine, "waydroid") ||
		strings.Contains(lowerLine, "vbox86")
}

// LaunchApp launches an Android package via monkey launcher intent (no Activity name needed).
func (d *ADBDriver) LaunchApp(ctx context.Context, deviceID, appID string) error {
	pkg := strings.TrimSpace(appID)
	if pkg == "" || !isSafePackageToken(pkg) {
		return fmt.Errorf("无效的安卓应用包名：%q", appID)
	}
	if d.wakeAndCheckLocked(ctx, deviceID) {
		return errors.New("安卓手机当前处于锁屏状态，无法打开应用，请先解锁手机屏幕")
	}
	_, err := d.run(ctx, deviceID, "shell", "monkey", "-p", pkg, "-c", "android.intent.category.LAUNCHER", "1")
	return err
}

// OpenURL opens a URL or scheme via Android ACTION_VIEW intent.
func (d *ADBDriver) OpenURL(ctx context.Context, deviceID, rawURL string) error {
	clean := strings.TrimSpace(rawURL)
	if clean == "" {
		return errors.New("链接不能为空")
	}
	if d.wakeAndCheckLocked(ctx, deviceID) {
		return errors.New("安卓手机当前处于锁屏状态，无法打开链接，请先解锁手机屏幕")
	}
	_, err := d.run(ctx, deviceID, "shell", "am", "start", "-a", "android.intent.action.VIEW", "-d", shellSingleQuote(clean))
	return err
}

// Screenshot grabs raw PNG screen bytes via `adb exec-out screencap -p`.
func (d *ADBDriver) Screenshot(ctx context.Context, deviceID string) ([]byte, error) {
	out, err := d.run(ctx, deviceID, "exec-out", "screencap", "-p")
	if err != nil {
		return nil, err
	}
	// Strip any stray prefix before the PNG magic header if the ROM prints warnings.
	magic := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	idx := bytes.Index(out, magic)
	if idx < 0 {
		return nil, errors.New("adb screencap 未返回有效 PNG 图像")
	}
	return out[idx:], nil
}

// Elements dumps the Android UIAutomator hierarchy XML and extracts visible nodes.
func (d *ADBDriver) Elements(ctx context.Context, deviceID string) ([]Element, int, int, error) {
	rawXML, err := d.dumpUIAutomatorXML(ctx, deviceID)
	if err != nil {
		return nil, 0, 0, err
	}
	return ParseAndroidUIAutomatorXML(rawXML)
}

func (d *ADBDriver) dumpUIAutomatorXML(ctx context.Context, deviceID string) ([]byte, error) {
	const remoteDump = "/sdcard/diana_window_dump.xml"
	if out, err := d.run(ctx, deviceID, "shell", "uiautomator dump --compressed "+remoteDump+" >/dev/null 2>&1 && cat "+remoteDump+"; rm -f "+remoteDump); err == nil {
		if start := bytes.Index(out, []byte("<hierarchy")); start >= 0 {
			if end := bytes.LastIndex(out, []byte("</hierarchy>")); end > start {
				return out[start : end+len("</hierarchy>")], nil
			}
		}
	}
	if out, err := d.run(ctx, deviceID, "exec-out", "uiautomator", "dump", "--compressed", "/dev/tty"); err == nil {
		if start := bytes.Index(out, []byte("<hierarchy")); start >= 0 {
			if end := bytes.LastIndex(out, []byte("</hierarchy>")); end > start {
				return out[start : end+len("</hierarchy>")], nil
			}
		}
	}
	if _, err := d.run(ctx, deviceID, "shell", "uiautomator", "dump", remoteDump); err != nil {
		return nil, err
	}
	return d.run(ctx, deviceID, "exec-out", "cat", remoteDump)
}

// ParseAndroidUIAutomatorXML parses Android UIAutomator XML into visible UI elements and screen dimensions.
func ParseAndroidUIAutomatorXML(data []byte) ([]Element, int, int, error) {
	idx := bytes.Index(data, []byte("<hierarchy"))
	if idx >= 0 {
		data = data[idx:]
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var elements []Element
	maxW, maxH := 0, 0

	for {
		tok, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, 0, 0, fmt.Errorf("解析安卓控件树 XML 失败：%w", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Local == "hierarchy" {
			for _, attr := range start.Attr {
				switch attr.Name.Local {
				case "width":
					maxW, _ = strconv.Atoi(attr.Value)
				case "height":
					maxH, _ = strconv.Atoi(attr.Value)
				}
			}
			continue
		}
		if start.Name.Local != "node" {
			continue
		}
		var text, desc, className, boundsStr string
		visible := true
		clickable := false
		for _, attr := range start.Attr {
			switch attr.Name.Local {
			case "text":
				text = strings.TrimSpace(attr.Value)
			case "content-desc":
				desc = strings.TrimSpace(attr.Value)
			case "class":
				className = shortAndroidClass(attr.Value)
			case "bounds":
				boundsStr = strings.TrimSpace(attr.Value)
			case "displayed":
				visible = attr.Value != "false"
			case "clickable":
				clickable = attr.Value == "true"
			}
		}
		if !visible {
			continue
		}
		x1, y1, x2, y2, ok := parseADBBounds(boundsStr)
		if !ok || x2 <= x1 || y2 <= y1 {
			continue
		}
		if x2 > maxW {
			maxW = x2
		}
		if y2 > maxH {
			maxH = y2
		}
		if text == "" && desc == "" && !clickable {
			continue
		}
		elements = append(elements, Element{
			Type:   className,
			Label:  desc,
			Text:   text,
			X:      x1,
			Y:      y1,
			Width:  x2 - x1,
			Height: y2 - y1,
		})
	}
	return elements, maxW, maxH, nil
}

func shortAndroidClass(full string) string {
	full = strings.TrimSpace(full)
	if idx := strings.LastIndexByte(full, '.'); idx >= 0 && idx+1 < len(full) {
		return full[idx+1:]
	}
	return full
}

func parseADBBounds(raw string) (int, int, int, int, bool) {
	m := adbBoundsPattern.FindStringSubmatch(raw)
	if len(m) != 5 {
		return 0, 0, 0, 0, false
	}
	x1, err1 := strconv.Atoi(m[1])
	y1, err2 := strconv.Atoi(m[2])
	x2, err3 := strconv.Atoi(m[3])
	y2, err4 := strconv.Atoi(m[4])
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return 0, 0, 0, 0, false
	}
	return x1, y1, x2, y2, true
}

// Tap taps screen coordinates via `adb shell input tap`.
func (d *ADBDriver) Tap(ctx context.Context, deviceID string, x, y int) error {
	_, err := d.run(ctx, deviceID, "shell", "input", "tap", strconv.Itoa(x), strconv.Itoa(y))
	return err
}

// Swipe swipes between two points via `adb shell input swipe`.
func (d *ADBDriver) Swipe(ctx context.Context, deviceID string, x1, y1, x2, y2, durationMS int) error {
	if durationMS <= 0 {
		durationMS = 380
	}
	_, err := d.run(ctx, deviceID, "shell", "input", "swipe",
		strconv.Itoa(x1), strconv.Itoa(y1), strconv.Itoa(x2), strconv.Itoa(y2), strconv.Itoa(durationMS))
	return err
}

// TypeText inputs text via ADBKeyboard broadcast when non-ASCII, or `input text` for ASCII.
func (d *ADBDriver) TypeText(ctx context.Context, deviceID, text string) error {
	if text == "" {
		return nil
	}
	for _, r := range text {
		if r > 127 {
			// Standard `adb shell input text` only supports ASCII; try ADBKeyBoard broadcast first.
			_, err := d.run(ctx, deviceID, "shell", "am", "broadcast", "-a", "ADB_INPUT_TEXT", "--es", "msg", shellSingleQuote(text))
			return err
		}
	}
	escaped := strings.ReplaceAll(text, " ", "%s")
	_, err := d.run(ctx, deviceID, "shell", "input", "text", shellSingleQuote(escaped))
	return err
}

// PressKey maps logical keys ("home", "back", "enter") to Android keyevents.
func (d *ADBDriver) PressKey(ctx context.Context, deviceID, key string) error {
	code := "3"
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "home":
		code = "3"
	case "back":
		code = "4"
	case "enter", "return", "search":
		code = "66"
	case "power":
		code = "26"
	default:
		return fmt.Errorf("不支持的安卓按键 %q（支持 home、back、enter）", key)
	}
	_, err := d.run(ctx, deviceID, "shell", "input", "keyevent", code)
	return err
}

// Close resets wireless state.
func (d *ADBDriver) Close() error {
	d.mu.Lock()
	d.connected = false
	d.mu.Unlock()
	return nil
}

func isSafePackageToken(value string) bool {
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return strings.Contains(value, ".")
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
