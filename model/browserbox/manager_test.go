// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	status := manager.Bot("bot-a").Status()
	if status.Running || status.Settings.Enabled {
		t.Fatalf("默认应是关着的，实际 %+v", status)
	}
	// 没启用时给空地址且不报错：让位给机器人配置里的外部 CDP 地址。
	if url, err := manager.Bot("bot-a").Endpoint(context.Background()); url != "" || err != nil {
		t.Fatalf("没启用时应让位给外部 CDP 地址，实际 %q %v", url, err)
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

// 接管打开时模型那一侧必须当场失效，且理由要说清楚是谁在占着；接管只作用于那一台机器人。
func TestTakeoverHidesCDPURLFromAgent(t *testing.T) {
	manager := New(context.Background(), &memoryStore{}, t.TempDir())
	manager.mu.Lock()
	manager.settings.Enabled = true
	manager.instanceLocked("bot-a").cdpURL = "http://127.0.0.1:12345"
	manager.instanceLocked("bot-b").cdpURL = "http://127.0.0.1:23456"
	manager.mu.Unlock()
	ctx := context.Background()

	if url, err := manager.Bot("bot-a").Endpoint(ctx); err != nil || url != "http://127.0.0.1:12345" {
		t.Fatalf("正常状态下应给出自己的地址，实际 %q %v", url, err)
	}
	manager.Bot("bot-a").SetTakeover(true)
	if _, err := manager.Bot("bot-a").Endpoint(ctx); err == nil || !strings.Contains(err.Error(), "接管") {
		t.Fatalf("接管时要给模型一句能看懂的理由，实际 %v", err)
	}
	if url, err := manager.Bot("bot-b").Endpoint(ctx); err != nil || url != "http://127.0.0.1:23456" {
		t.Fatalf("接管 A 不该影响 B，实际 %q %v", url, err)
	}
	manager.Bot("bot-a").SetTakeover(false)
	if url, _ := manager.Bot("bot-a").Endpoint(ctx); url == "" {
		t.Fatal("交还控制权后应恢复")
	}
}

// 每台机器人的登录态目录互不相同，ID 里带路径分隔符也拼不出别人的目录。
func TestProfileDirIsPerBot(t *testing.T) {
	manager := New(context.Background(), &memoryStore{}, t.TempDir())
	a, b := manager.ProfileDir("bot-a"), manager.ProfileDir("bot-b")
	if a == b {
		t.Fatalf("两台机器人共用了登录态目录：%s", a)
	}
	if escaped := manager.ProfileDir("../bot-a"); strings.Contains(escaped, "..") || escaped == a {
		t.Fatalf("ID 里的路径分隔符没挡住：%s", escaped)
	}
}

// 拆分之前那份共用的登录态交给第一台机器人，升级后不用重新登录；它已经有自己的就不动。
func TestAdoptLegacyProfile(t *testing.T) {
	dir := t.TempDir()
	manager := New(context.Background(), &memoryStore{}, dir)
	legacy := filepath.Join(dir, "browser-box", "profile")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "Cookies"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.AdoptLegacyProfile("bot-a"); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	if _, err := os.Stat(filepath.Join(manager.ProfileDir("bot-a"), "Cookies")); err != nil {
		t.Fatalf("旧登录态没搬到第一台机器人名下：%v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("搬完旧目录应该不在了")
	}
	if err := manager.AdoptLegacyProfile("bot-b"); err != nil {
		t.Fatalf("重复调用不该报错：%v", err)
	}
	if _, err := os.Stat(manager.ProfileDir("bot-b")); !os.IsNotExist(err) {
		t.Fatal("旧登录态只该给一台机器人")
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
		enabled, err := manager.EnableByDefault(context.Background())
		if err != nil {
			t.Fatalf("display=%v：打开时不该起进程，也就不该报错：%v", display, err)
		}
		if !enabled {
			t.Fatalf("display=%v：找得到浏览器时应打开", display)
		}
		if !store.ok || !store.doc.Settings.Enabled || store.doc.Settings.Headful != display {
			t.Fatalf("display=%v：落盘的配置不对 %+v", display, store.doc.Settings)
		}
	}
}

// Chrome 的单实例套接字建在 TMPDIR 下，路径超过 108 字节（macOS 104）就直接退出。
// 按机器人拆 profile 之后数据目录带上了 UUID，TMPDIR 再跟着它走就会超。
func TestShortTempDirLeavesRoomForSingletonSocket(t *testing.T) {
	dir, err := shortTempDir()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "org.chromium.Chromium.XXXXXX", "SingletonSocket")
	if len(socket) > 100 {
		t.Fatalf("临时目录太长，单实例套接字会超限：%s（%d 字节）", socket, len(socket))
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("临时目录权限应当是 0700：%v %v", info, err)
	}
}
