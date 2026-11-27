// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package mobilectl

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func makeTinyPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 20, G: 140, B: 90, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestParseAndroidUIAutomatorXML(t *testing.T) {
	const raw = `UI hierchary dumped to: /dev/tty
<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<hierarchy rotation="0">
  <node index="0" text="" resource-id="" class="android.widget.FrameLayout" clickable="false" displayed="true" bounds="[0,0][1080,2400]">
    <node index="0" text="英伟达官方宣布重要人事回归" content-desc="热榜第一" class="android.widget.TextView" clickable="true" displayed="true" bounds="[40,320][1040,460]" />
    <node index="1" text="被隐藏的节点" content-desc="" class="android.widget.TextView" clickable="false" displayed="false" bounds="[40,500][500,600]" />
  </node>
</hierarchy>`

	elems, w, h, err := ParseAndroidUIAutomatorXML([]byte(raw))
	if err != nil {
		t.Fatalf("ParseAndroidUIAutomatorXML failed: %v", err)
	}
	if w != 1080 || h != 2400 {
		t.Fatalf("unexpected screen size %dx%d", w, h)
	}
	if len(elems) != 1 {
		t.Fatalf("expected 1 visible element, got %d: %+v", len(elems), elems)
	}
	if got := elems[0].DisplayText(); !strings.Contains(got, "热榜第一") || !strings.Contains(got, "英伟达") {
		t.Fatalf("unexpected DisplayText: %q", got)
	}
	cx, cy := elems[0].Center()
	if cx != 540 || cy != 390 {
		t.Fatalf("unexpected center (%d,%d)", cx, cy)
	}
}

func TestWDADriverAndControllerFlow(t *testing.T) {
	pngBytes := makeTinyPNG(t, 390, 844)
	var mu sync.Mutex
	var launchedBundle, lastURL, lastPressed string
	var tapped [2]int
	var dragged [4]int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sessionId": "sid-1",
				"value": map[string]any{
					"ready": true,
					"os":    map[string]any{"name": "iOS", "version": "18.2"},
					"ios":   map[string]any{"ip": "192.168.1.25"},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/session/sid-1/wda/apps/launch":
			var body struct {
				BundleID string `json:"bundleId"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			launchedBundle = body.BundleID
			_, _ = w.Write([]byte(`{"value":{}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/session/sid-1/url":
			var body struct {
				URL string `json:"url"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			lastURL = body.URL
			_, _ = w.Write([]byte(`{"value":{}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/screenshot":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": base64.StdEncoding.EncodeToString(pngBytes),
			})
		case r.Method == http.MethodGet && r.URL.Path == "/source":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": map[string]any{
					"type":      "XCUIElementTypeApplication",
					"isVisible": true,
					"rect":      map[string]any{"x": 0, "y": 0, "width": 390, "height": 844},
					"children": []any{
						map[string]any{
							"type":      "XCUIElementTypeCell",
							"label":     "小红书热门笔记：显卡价格大跳水",
							"value":     "2.3万赞",
							"isVisible": "1",
							"rect":      map[string]any{"x": 10, "y": 120, "width": 370, "height": 200},
						},
					},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/session/sid-1/wda/tap":
			var body struct {
				X int `json:"x"`
				Y int `json:"y"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			tapped = [2]int{body.X, body.Y}
			_, _ = w.Write([]byte(`{"value":{}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/session/sid-1/wda/dragfromtoforduration":
			var body struct {
				FromX int `json:"fromX"`
				FromY int `json:"fromY"`
				ToX   int `json:"toX"`
				ToY   int `json:"toY"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			dragged = [4]int{body.FromX, body.FromY, body.ToX, body.ToY}
			_, _ = w.Write([]byte(`{"value":{}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/session/sid-1/wda/pressButton":
			var body struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			lastPressed = body.Name
			_, _ = w.Write([]byte(`{"value":{}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/session/sid-1/wda/keys":
			_, _ = w.Write([]byte(`{"value":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ctrl := NewController(Config{
		Backend:    BackendAuto,
		ADBPath:    "/nonexistent/adb",
		WDAURL:     srv.URL,
		HTTPClient: srv.Client(),
		Timeout:    5 * time.Second,
	})
	defer ctrl.Close()

	ctx := context.Background()
	err := ctrl.WithExclusiveSession(ctx, func(ctx context.Context) error {
		devs, err := ctrl.ListDevices(ctx)
		if err != nil {
			t.Fatalf("ListDevices: %v", err)
		}
		if len(devs) != 1 || devs[0].Platform != PlatformIOS {
			t.Fatalf("expected 1 iOS device, got %+v", devs)
		}
		if _, err := ctrl.LaunchApp(ctx, "", "com.xingin.discover"); err != nil {
			t.Fatalf("LaunchApp: %v", err)
		}
		if _, err := ctrl.OpenURL(ctx, "", "https://x.com/home"); err != nil {
			t.Fatalf("OpenURL: %v", err)
		}
		if _, rawShot, err := ctrl.Screenshot(ctx, ""); err != nil || len(rawShot) == 0 {
			t.Fatalf("Screenshot: err=%v len=%d", err, len(rawShot))
		}
		snap, err := ctrl.Elements(ctx, "")
		if err != nil {
			t.Fatalf("Elements: %v", err)
		}
		if len(snap.Elements) != 1 || snap.Observation <= 0 {
			t.Fatalf("unexpected elements snapshot: %+v", snap)
		}
		if _, _, err := ctrl.TapElement(ctx, "", snap.Observation, 1); err != nil {
			t.Fatalf("TapElement: %v", err)
		}
		if _, err := ctrl.SwipeDirection(ctx, "", "up"); err != nil {
			t.Fatalf("SwipeDirection: %v", err)
		}
		if _, err := ctrl.TypeText(ctx, "", "NVDA"); err != nil {
			t.Fatalf("TypeText: %v", err)
		}
		if _, err := ctrl.PressKey(ctx, "", "home"); err != nil {
			t.Fatalf("PressKey: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithExclusiveSession: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if launchedBundle != "com.xingin.discover" {
		t.Fatalf("launchedBundle = %q", launchedBundle)
	}
	if lastURL != "https://x.com/home" {
		t.Fatalf("lastURL = %q", lastURL)
	}
	if tapped != [2]int{195, 220} {
		t.Fatalf("unexpected tap coordinates: %+v", tapped)
	}
	if dragged[1] <= dragged[3] {
		t.Fatalf("expected upward drag (fromY > toY), got %+v", dragged)
	}
	if lastPressed != "home" {
		t.Fatalf("lastPressed = %q", lastPressed)
	}
}

type scriptedMCPCaller struct {
	png []byte
}

func (f *scriptedMCPCaller) Call(_ context.Context, name string, _ map[string]any) (mcpToolOutput, error) {
	switch name {
	case "mobile_list_available_devices":
		raw, _ := json.Marshal(map[string]any{
			"devices": []map[string]any{
				{"id": "pixel-9", "name": "Pixel 9 Pro", "platform": "android", "state": "online"},
			},
		})
		return mcpToolOutput{Structured: raw}, nil
	case "mobile_list_elements_on_screen":
		raw, _ := json.Marshal(map[string]any{
			"elements": []map[string]any{
				{
					"type":        "TextView",
					"label":       "小黑盒热帖",
					"text":        "Steam 春促史低汇总",
					"coordinates": map[string]any{"x": 20, "y": 200, "width": 800, "height": 120},
				},
			},
		})
		return mcpToolOutput{Structured: raw}, nil
	case "mobile_take_screenshot":
		return mcpToolOutput{ImageBytes: f.png}, nil
	default:
		return mcpToolOutput{Text: "ok"}, nil
	}
}

func (f *scriptedMCPCaller) Close() error { return nil }

func TestMCPDriverWithCaller(t *testing.T) {
	drv := NewMCPDriverWithCaller(&scriptedMCPCaller{png: makeTinyPNG(t, 1080, 2400)})
	ctrl := NewControllerWithDriver(Config{Backend: BackendMobileMCP}, drv)
	defer ctrl.Close()

	ctx := context.Background()
	devs, err := ctrl.ListDevices(ctx)
	if err != nil || len(devs) != 1 || devs[0].ID != "pixel-9" {
		t.Fatalf("ListDevices: devs=%+v err=%v", devs, err)
	}
	snap, err := ctrl.Elements(ctx, "pixel-9")
	if err != nil || len(snap.Elements) != 1 {
		t.Fatalf("Elements: snap=%+v err=%v", snap, err)
	}
	if _, err := ctrl.Tap(ctx, "pixel-9", 100, 200); err != nil {
		t.Fatalf("Tap: %v", err)
	}
	if _, err := ctrl.Swipe(ctx, "pixel-9", 500, 1600, 500, 400, 350); err != nil {
		t.Fatalf("Swipe: %v", err)
	}
	if _, err := ctrl.PressKey(ctx, "pixel-9", "back"); err != nil {
		t.Fatalf("PressKey: %v", err)
	}
}

func TestADBDriverScriptedBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake adb is unix-only")
	}
	dir := t.TempDir()
	fakeADB := filepath.Join(dir, "adb")
	pngB64 := base64.StdEncoding.EncodeToString(makeTinyPNG(t, 1080, 2400))
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *\"connect\"*) echo 'connected to 192.168.1.88:5555' ;;\n" +
		"  *\"devices -l\"*) echo 'List of devices attached'; echo '192.168.1.88:5555 device product:husky model:Pixel_8_Pro' ;;\n" +
		"  *\"getprop\"*) exit 0 ;;\n" +
		"  *\"dumpsys\"*) echo 'mWakefulness=Awake' ;;\n" +
		"  *\"screencap -p\"*) printf '%s' '" + pngB64 + "' | base64 -d 2>/dev/null || printf '%s' '" + pngB64 + "' | base64 -D ;;\n" +
		"  *\"uiautomator dump\"*) echo \"<hierarchy><node text='X热帖：英伟达CEO今日宣布重大回归' content-desc='' class='android.widget.TextView' clickable='true' displayed='true' bounds='[30,200][1020,400]'/></hierarchy>\" ;;\n" +
		"  *) echo 'ok' ;;\n" +
		"esac\n"
	if err := os.WriteFile(fakeADB, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake adb: %v", err)
	}

	ctrl := NewController(Config{
		Backend:    BackendADB,
		ADBPath:    fakeADB,
		ADBAddress: "192.168.1.88:5555",
		Timeout:    5 * time.Second,
	})
	defer ctrl.Close()

	ctx := context.Background()
	devs, err := ctrl.ListDevices(ctx)
	if err != nil || len(devs) != 1 || devs[0].Name != "Pixel 8 Pro" {
		t.Fatalf("ListDevices: devs=%+v err=%v", devs, err)
	}
	if _, err := ctrl.LaunchApp(ctx, "", "com.twitter.android"); err != nil {
		t.Fatalf("LaunchApp: %v", err)
	}
	if _, shot, err := ctrl.Screenshot(ctx, ""); err != nil || len(shot) == 0 {
		t.Fatalf("Screenshot: len=%d err=%v", len(shot), err)
	}
	snap, err := ctrl.Elements(ctx, "")
	if err != nil || len(snap.Elements) != 1 {
		t.Fatalf("Elements: snap=%+v err=%v", snap, err)
	}
	if _, err := ctrl.TypeText(ctx, "", "hello world"); err != nil {
		t.Fatalf("TypeText ascii: %v", err)
	}
	if _, err := ctrl.TypeText(ctx, "", "中文测试"); err != nil {
		t.Fatalf("TypeText unicode: %v", err)
	}
	if _, err := ctrl.PressKey(ctx, "", "home"); err != nil {
		t.Fatalf("PressKey: %v", err)
	}
	// Also keep NewADBDriver directly covered.
	plainADB := NewADBDriver(fakeADB, "", 3*time.Second)
	_ = plainADB.Close()
}

func TestSimctlAutoBootAndScreenshot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake xcrun is unix-only")
	}
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "booted")
	fakeXcrun := filepath.Join(dir, "xcrun")
	pngB64 := base64.StdEncoding.EncodeToString(makeTinyPNG(t, 393, 852))
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *\"simctl list devices --json\"*)\n" +
		"    if [ -f '" + stateFile + "' ]; then st='Booted'; else st='Shutdown'; fi\n" +
		"    printf '{\"devices\":{\"com.apple.CoreSimulator.SimRuntime.iOS-18-2\":[{\"udid\":\"SIM-UUID-1\",\"name\":\"iPhone 16\",\"state\":\"%s\",\"isAvailable\":true}]}}' \"$st\"\n" +
		"    ;;\n" +
		"  *\"simctl boot SIM-UUID-1\"*) touch '" + stateFile + "' ;;\n" +
		"  *\"simctl bootstatus\"*) exit 0 ;;\n" +
		// 和真实 simctl 一样：截图写进最后一个参数给的文件，标准输出只有提示文字。
		"  *\"simctl io\"*\"screenshot\"*)\n" +
		"    for out; do :; done\n" +
		"    { printf '%s' '" + pngB64 + "' | base64 -d 2>/dev/null || printf '%s' '" + pngB64 + "' | base64 -D; } > \"$out\"\n" +
		"    echo \"Wrote screenshot to: $out\" ;;\n" +
		"  *) echo 'ok' ;;\n" +
		"esac\n"
	if err := os.WriteFile(fakeXcrun, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake xcrun: %v", err)
	}

	ctrl := NewController(Config{
		Backend:   BackendSimctl,
		XcrunPath: fakeXcrun,
		VMName:    "iPhone 16",
		Timeout:   5 * time.Second,
	})
	defer ctrl.Close()

	ctx := context.Background()
	devs, err := ctrl.ListDevices(ctx)
	if err != nil || len(devs) != 1 || !devs[0].Virtual || devs[0].ID != "SIM-UUID-1" {
		t.Fatalf("expected auto-booted virtual iOS Simulator, got devs=%+v err=%v", devs, err)
	}
	if _, err := ctrl.LaunchApp(ctx, "", "com.atebits.Tweetie2"); err != nil {
		t.Fatalf("simctl LaunchApp: %v", err)
	}
	if _, err := ctrl.OpenURL(ctx, "", "https://news.ycombinator.com"); err != nil {
		t.Fatalf("simctl OpenURL: %v", err)
	}
	if _, shot, err := ctrl.Screenshot(ctx, ""); err != nil || len(shot) == 0 {
		t.Fatalf("simctl Screenshot: len=%d err=%v", len(shot), err)
	}
	if _, err := ctrl.PressKey(ctx, "", "home"); err != nil {
		t.Fatalf("simctl PressKey home: %v", err)
	}
}

func TestADBAutoBootAVDAndRedroidVirtualDetection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake emulator/adb is unix-only")
	}
	dir := t.TempDir()
	bootFlag := filepath.Join(dir, "avd_booted")
	fakeEmu := filepath.Join(dir, "emulator")
	fakeADB := filepath.Join(dir, "adb")

	emuScript := "#!/bin/sh\ntouch '" + bootFlag + "'\nexit 0\n"
	adbScript := "#!/bin/sh\n" +
		"if [ -f '" + bootFlag + "' ]; then\n" +
		"  echo 'List of devices attached'\n" +
		"  echo 'emulator-5554 device product:sdk_gphone64_arm64 model:Pixel_8_AVD'\n" +
		"  echo '127.0.0.1:5555 device product:redroid_arm64 model:Redroid_14'\n" +
		"else\n" +
		"  echo 'List of devices attached'\n" +
		"fi\n"
	if err := os.WriteFile(fakeEmu, []byte(emuScript), 0o755); err != nil {
		t.Fatalf("write fake emulator: %v", err)
	}
	if err := os.WriteFile(fakeADB, []byte(adbScript), 0o755); err != nil {
		t.Fatalf("write fake adb: %v", err)
	}

	ctrl := NewController(Config{
		Backend:      BackendADB,
		ADBPath:      fakeADB,
		EmulatorPath: fakeEmu,
		VMName:       "Pixel_8_API_35",
		VMHeadless:   true,
		Timeout:      5 * time.Second,
	})
	defer ctrl.Close()

	devs, err := ctrl.ListDevices(context.Background())
	if err != nil || len(devs) != 2 {
		t.Fatalf("expected 2 booted virtual Android devices (AVD + Redroid), got devs=%+v err=%v", devs, err)
	}
	for _, d := range devs {
		if !d.Virtual {
			t.Fatalf("expected Virtual=true for %+v", d)
		}
	}
}

func TestADBUnlockPINAndCircuitBreaker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake adb is unix-only")
	}
	dir := t.TempDir()
	unlockedFile := filepath.Join(dir, "unlocked")
	pinCountFile := filepath.Join(dir, "pin_attempts")
	fakeADB := filepath.Join(dir, "adb")

	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *\"devices -l\"*) echo 'List of devices attached'; echo 'usb-1 device model:Find_X6' ;;\n" +
		"  *\"getprop\"*) exit 0 ;;\n" +
		"  *\"dumpsys\"*)\n" +
		"    if [ -f '" + unlockedFile + "' ]; then echo 'mWakefulness=Awake'; else echo 'mWakefulness=Dozing showing=true'; fi\n" +
		"    ;;\n" +
		"  *\"input text\"*)\n" +
		"    echo x >> '" + pinCountFile + "'\n" +
		"    case \"$*\" in *\"123456\"*) touch '" + unlockedFile + "' ;; esac\n" +
		"    ;;\n" +
		"  *) exit 0 ;;\n" +
		"esac\n"
	if err := os.WriteFile(fakeADB, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake adb: %v", err)
	}

	// Wrong PIN should only be attempted ONCE across multiple ListDevices calls (circuit breaker).
	wrongCtrl := NewController(Config{
		Backend:      BackendADB,
		ADBPath:      fakeADB,
		UnlockPIN:    "000000",
		StayAwakeUSB: true,
		Timeout:      5 * time.Second,
	})
	defer wrongCtrl.Close()

	devs1, err := wrongCtrl.ListDevices(context.Background())
	if err != nil || len(devs1) != 1 || !devs1[0].Locked {
		t.Fatalf("expected device still locked on wrong PIN, got devs=%+v err=%v", devs1, err)
	}
	_, _ = wrongCtrl.ListDevices(context.Background())
	attemptsRaw, _ := os.ReadFile(pinCountFile)
	if strings.Count(string(attemptsRaw), "x") != 1 {
		t.Fatalf("expected circuit breaker to cap wrong PIN at 1 attempt, got %d", strings.Count(string(attemptsRaw), "x"))
	}

	// Correct PIN unlocks automatically.
	rightCtrl := NewController(Config{
		Backend:   BackendADB,
		ADBPath:   fakeADB,
		UnlockPIN: "123456",
		Timeout:   5 * time.Second,
	})
	defer rightCtrl.Close()

	devs2, err := rightCtrl.ListDevices(context.Background())
	if err != nil || len(devs2) != 1 || devs2[0].Locked {
		t.Fatalf("expected device auto-unlocked with right PIN, got devs=%+v err=%v", devs2, err)
	}
}

func TestADBColorOSTwoStepKeypadUnlock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake adb is unix-only")
	}
	dir := t.TempDir()
	keypadShown := filepath.Join(dir, "keypad_shown")
	unlockedFile := filepath.Join(dir, "unlocked")
	fakeADB := filepath.Join(dir, "adb")

	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *\"devices -l\"*) echo 'List of devices attached'; echo 'c94322e8 device model:PGEM10' ;;\n" +
		"  *\"getprop\"*) echo 'OPPO Find X6 Pro' ;;\n" +
		"  *\"dumpsys\"*)\n" +
		"    if [ -f '" + unlockedFile + "' ]; then echo 'mWakefulness=Awake'; else echo 'mWakefulness=Dozing showing=true'; fi\n" +
		"    ;;\n" +
		"  *\"uiautomator dump\"*)\n" +
		"    if [ -f '" + keypadShown + "' ]; then\n" +
		"      echo \"<hierarchy><node text='' content-desc='1' class='android.widget.Button' clickable='true' displayed='true' bounds='[224,1356][496,1628]'/><node text='' content-desc='2' class='android.widget.Button' clickable='true' displayed='true' bounds='[584,1356][856,1628]'/><node text='' content-desc='0' class='android.widget.Button' clickable='true' displayed='true' bounds='[584,2388][856,2660]'/></hierarchy>\"\n" +
		"    else\n" +
		"      echo \"<hierarchy><node text='输入密码' content-desc='' class='android.widget.Button' clickable='true' displayed='true' bounds='[552,1651][888,1763]'/></hierarchy>\"\n" +
		"    fi\n" +
		"    ;;\n" +
		"  *\"input tap 720 1707\"*) touch '" + keypadShown + "' ;;\n" +
		"  *\"input tap 360 1492; sleep 0.12; input tap 720 1492; sleep 0.12; input tap 720 2524\"*) touch '" + unlockedFile + "' ;;\n" +
		"  *) exit 0 ;;\n" +
		"esac\n"
	if err := os.WriteFile(fakeADB, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake adb: %v", err)
	}

	ctrl := NewController(Config{
		Backend:   BackendADB,
		ADBPath:   fakeADB,
		UnlockPIN: "120",
		Timeout:   10 * time.Second,
	})
	defer ctrl.Close()

	devs, err := ctrl.ListDevices(context.Background())
	if err != nil || len(devs) != 1 || devs[0].Locked {
		t.Fatalf("expected ColorOS two-step keypad auto-unlock to succeed, got devs=%+v err=%v", devs, err)
	}
}
