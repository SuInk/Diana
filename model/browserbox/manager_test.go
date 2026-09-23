// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserbox

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

type memoryStore struct {
	mu  sync.Mutex
	doc Document
	ok  bool
}

func (s *memoryStore) LoadBrowserBox(context.Context) (Document, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.doc, s.ok, nil
}

func (s *memoryStore) SaveBrowserBox(_ context.Context, doc Document) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.doc = doc
	s.ok = true
	return nil
}

// 关着的时候不该有进程，也不该给模型任何地址。
func TestManagerStartsDisabled(t *testing.T) {
	manager := New(context.Background(), &memoryStore{}, t.TempDir())
	status := manager.Status()
	if status.Running || status.Settings.Enabled {
		t.Fatalf("默认应是关着的，实际 %+v", status)
	}
	if manager.AgentCDPURL() != "" {
		t.Fatal("没启用时不该给出 CDP 地址")
	}
	if manager.Unavailable() != "" {
		t.Fatal("没启用时应让位给外部 CDP 地址，不该报不可用")
	}
}

// 配置要落盘，进程起不起来是另一回事——测试环境里没有浏览器也不能丢配置。
func TestManagerPersistsSettings(t *testing.T) {
	store := &memoryStore{}
	dir := t.TempDir()
	manager := New(context.Background(), store, dir)
	saved, err := manager.SetSettings(context.Background(), Settings{DeniedHosts: []string{"blocked.example.com"}})
	if err != nil {
		t.Fatalf("保存配置失败：%v", err)
	}
	if len(saved.DeniedHosts) != 1 {
		t.Fatalf("黑名单没存住：%+v", saved)
	}
	reloaded := New(context.Background(), store, dir)
	if got := reloaded.Settings(); len(got.DeniedHosts) != 1 || got.DeniedHosts[0] != "blocked.example.com" {
		t.Fatalf("重新加载后配置对不上：%+v", got)
	}
}

// 接管打开时模型那一侧必须当场失效，且理由要说清楚是谁在占着。
func TestTakeoverHidesCDPURLFromAgent(t *testing.T) {
	manager := New(context.Background(), &memoryStore{}, t.TempDir())
	manager.mu.Lock()
	manager.settings.Enabled = true
	manager.cdpURL = "http://127.0.0.1:12345"
	manager.mu.Unlock()

	if manager.AgentCDPURL() == "" {
		t.Fatal("正常状态下应给出地址")
	}
	manager.SetTakeover(true)
	if manager.AgentCDPURL() != "" {
		t.Fatal("接管时不该再给模型地址")
	}
	if reason := manager.Unavailable(); reason == "" {
		t.Fatal("接管时要给模型一句能看懂的理由")
	}
	manager.SetTakeover(false)
	if manager.AgentCDPURL() == "" {
		t.Fatal("交还控制权后应恢复")
	}
}

// 调试地址要能从 Chrome 的输出里解析出来，端口是随机的。
func TestDebugHTTPBase(t *testing.T) {
	got, err := debugHTTPBase("ws://127.0.0.1:53201/devtools/browser/8f2c")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if got != "http://127.0.0.1:53201" {
		t.Fatalf("解析结果不对：%s", got)
	}
	if _, err := debugHTTPBase("nonsense"); err == nil {
		t.Fatal("看不懂的地址应该报错")
	}
}

// 既没有图形会话、也没有 Xvfb 时打开有头，必须在落盘前就被挡下来：存下去等于
// 把正在跑的无头换成一个永远起不来的开关。
func TestSetSettingsRejectsHeadfulWithoutDisplay(t *testing.T) {
	if systemDisplayAvailable() || xvfbAvailable() {
		t.Skip("这台机器凑得出屏幕，挡不住也是对的")
	}
	store := &memoryStore{}
	manager := New(context.Background(), store, t.TempDir())
	if _, err := manager.SetSettings(context.Background(), Settings{Enabled: true, Headful: true}); !errors.Is(err, ErrNoDisplay) {
		t.Fatalf("应报缺显示器，实际 %v", err)
	}
	if manager.Settings().Headful {
		t.Fatal("被拒绝的配置不该落到内存里")
	}
	if store.ok {
		t.Fatal("被拒绝的配置不该落盘")
	}
}

// 关着的时候不碰进程，有头配置也就没必要拦——留给用户在没开的状态下先填好。
func TestSetSettingsAllowsHeadfulWhileDisabled(t *testing.T) {
	manager := New(context.Background(), &memoryStore{}, t.TempDir())
	if _, err := manager.SetSettings(context.Background(), Settings{Headful: true}); err != nil {
		t.Fatalf("没启用时不该因为有头报错：%v", err)
	}
}

// 进程退出的理由要带上它自己打印的那几行，只写 exit status 1 等于没说。
func TestExitErrorMessageKeepsDiagnostics(t *testing.T) {
	tail := &diagnosticTail{limit: 2048}
	_, _ = tail.Write([]byte("Missing X server or $DISPLAY\n"))
	message := exitErrorMessage(errors.New("exit status 1"), tail)
	if !strings.Contains(message, "exit status 1") || !strings.Contains(message, "Missing X server") {
		t.Fatalf("退出原因丢了：%s", message)
	}
	if got := exitErrorMessage(errors.New("exit status 1"), nil); !strings.Contains(got, "exit status 1") {
		t.Fatalf("没有诊断输出时也要给出退出码：%s", got)
	}
}

// 装了 Xvfb 就不该再拦：容器里的有头靠的正是它，拦掉等于把这条路堵死。
func TestHeadfulAllowedWithXvfb(t *testing.T) {
	if !xvfbAvailable() {
		t.Skip("这台机器没有 Xvfb")
	}
	if err := checkHeadful(Settings{Enabled: true, Headful: true}); err != nil {
		t.Fatalf("有 Xvfb 时不该拦：%v", err)
	}
}

// 虚拟屏要真的起得来，并且报回一个能用的显示号——显示号是交给 Xvfb 自己挑的，
// 挑错或者没报回来，Chromium 会连到一块不存在的屏上。
func TestStartVirtualDisplay(t *testing.T) {
	if !xvfbAvailable() {
		t.Skip("这台机器没有 Xvfb")
	}
	display, err := startVirtualDisplay(800, 600)
	if err != nil {
		t.Fatalf("虚拟显示起不来：%v", err)
	}
	defer display.Stop()
	if !strings.HasPrefix(display.display, ":") || len(display.display) < 2 {
		t.Fatalf("显示号不像话：%q", display.display)
	}
	if env := display.Env(); len(env) != 1 || env[0] != "DISPLAY="+display.display {
		t.Fatalf("没把 DISPLAY 传给浏览器：%v", env)
	}
}

// 没起虚拟屏时（宿主机自己有图形会话，或者无头）不能凭空往环境里塞 DISPLAY。
func TestNilVirtualDisplayIsInert(t *testing.T) {
	var display *virtualDisplay
	if env := display.Env(); len(env) != 0 {
		t.Fatalf("不该有额外环境变量：%v", env)
	}
	display.Stop()
}

func stubDetection(t *testing.T, browser, display bool) {
	t.Helper()
	oldBrowser, oldDisplay := findBrowserExecutable, headfulDisplayAvailable
	findBrowserExecutable = func() bool { return browser }
	headfulDisplayAvailable = func() bool { return display }
	t.Cleanup(func() { findBrowserExecutable, headfulDisplayAvailable = oldBrowser, oldDisplay })
}

// 找不到浏览器时不开，也不落盘：以后装上了，下次启动还要再探测。
func TestEnableByDefaultSkipsWithoutBrowser(t *testing.T) {
	stubDetection(t, false, true)
	store := &memoryStore{}
	manager := New(context.Background(), store, t.TempDir())
	enabled, err := manager.EnableByDefault(context.Background())
	if err != nil || enabled {
		t.Fatalf("没有浏览器时不该打开：enabled=%v err=%v", enabled, err)
	}
	if store.ok {
		t.Fatal("没有浏览器时不该落盘")
	}
}

// 保存过的配置一律不动，哪怕用户当时是关着的。
func TestEnableByDefaultRespectsSavedSettings(t *testing.T) {
	stubDetection(t, true, true)
	store := &memoryStore{doc: Document{Settings: Settings{Enabled: false}}, ok: true}
	manager := New(context.Background(), store, t.TempDir())
	enabled, err := manager.EnableByDefault(context.Background())
	if err != nil || enabled || manager.Settings().Enabled {
		t.Fatalf("保存过的配置不该被改：enabled=%v err=%v settings=%+v", enabled, err, manager.Settings())
	}
}

// 新装且找得到浏览器：打开，并按有没有显示器决定有头还是无头。
func TestEnableByDefaultPicksHeadfulByDisplay(t *testing.T) {
	for _, display := range []bool{true, false} {
		stubDetection(t, true, display)
		store := &memoryStore{}
		manager := New(context.Background(), store, t.TempDir())
		// 指向一个不存在的可执行文件，不在开发机上真拉起浏览器；这里只关心配置有没有
		// 按探测结果落盘。
		manager.settings.Executable = "/nonexistent/diana-test-chrome"
		enabled, _ := manager.EnableByDefault(context.Background())
		t.Cleanup(manager.Stop)
		if !enabled {
			t.Fatalf("display=%v：找得到浏览器时应打开", display)
		}
		if !store.ok || !store.doc.Settings.Enabled || store.doc.Settings.Headful != display {
			t.Fatalf("display=%v：落盘的配置不对 %+v", display, store.doc.Settings)
		}
	}
}
