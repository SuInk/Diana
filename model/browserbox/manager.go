// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserbox

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// Store 是配置落盘接口，由 model/storage 实现。
type Store interface {
	LoadBrowserBox(ctx context.Context) (Document, bool, error)
	SaveBrowserBox(ctx context.Context, doc Document) error
}

// Document 是落盘形态。只存配置，不存进程状态——进程是跑起来才有的东西。
type Document struct {
	Settings Settings `json:"settings"`
}

// Status 是管理接口看到的运行状态。
type Status struct {
	Settings   Settings  `json:"settings"`
	Running    bool      `json:"running"`
	Takeover   bool      `json:"takeover"`
	CDPURL     string    `json:"cdp_url,omitempty"`
	Executable string    `json:"executable,omitempty"`
	ProfileDir string    `json:"profile_dir,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
	// Available 表示这台机器上找得到浏览器可执行文件。找不到时 WebUI 要给出
	// 安装指引，而不是让用户对着一个永远起不来的开关猜。
	Available bool `json:"available"`
}

const (
	// launchTimeout 是等 Chrome 报出调试地址的时间。
	launchTimeout = 30 * time.Second
	// restartBackoff 是进程意外退出后的重启间隔。
	restartBackoff = 3 * time.Second
)

var devToolsLine = regexp.MustCompile(`DevTools listening on (ws://[^\s]+)`)

// Manager 看管内置浏览器进程：起、停、崩了自动拉起，以及对外回答
// 「现在能不能用、CDP 在哪」。
type Manager struct {
	store   Store
	dataDir string

	mu         sync.RWMutex
	settings   Settings
	cmd        *exec.Cmd
	cdpURL     string
	executable string
	startedAt  time.Time
	lastError  string
	takeover   bool
	stopping   bool
	// generation 用来分辨「这次退出属于哪一次启动」，避免旧进程的退出把新进程的
	// 状态清掉——和扩展那边旧 socket 的 close 是同一类坑。
	generation uint64
	watchers   map[chan struct{}]struct{}
}

// New 创建管理器并读取已保存的配置。开着的话顺手把浏览器拉起来。
func New(ctx context.Context, store Store, dataDir string) *Manager {
	m := &Manager{
		store:    store,
		dataDir:  strings.TrimSpace(dataDir),
		settings: Settings{}.WithDefaults(),
		watchers: map[chan struct{}]struct{}{},
	}
	if store != nil {
		if doc, ok, err := store.LoadBrowserBox(ctx); err == nil && ok {
			m.settings = doc.Settings.WithDefaults()
		}
	}
	if m.settings.Enabled {
		if err := m.Start(ctx); err != nil {
			m.setLastError(err)
		}
	}
	return m
}

// ProfileDir 是登录态所在目录。放在数据目录下而不是临时目录：这一档的全部意义
// 就是「登录一次以后一直有效」，profile 跟着挂卷走才谈得上持久。
func (m *Manager) ProfileDir() string {
	if m == nil || m.dataDir == "" {
		return ""
	}
	return filepath.Join(m.dataDir, "browser-box", "profile")
}

func (m *Manager) cacheDir() string { return filepath.Join(m.dataDir, "browser-box", "cache") }
func (m *Manager) crashDir() string { return filepath.Join(m.dataDir, "browser-box", "crash") }
func (m *Manager) rootDir() string  { return filepath.Join(m.dataDir, "browser-box") }

// Settings 返回当前配置副本。
func (m *Manager) Settings() Settings {
	if m == nil {
		return Settings{}.WithDefaults()
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.settings
}

// Status 汇总当前状态。
func (m *Manager) Status() Status {
	if m == nil {
		return Status{Settings: Settings{}.WithDefaults()}
	}
	m.mu.RLock()
	status := Status{
		Settings:   m.settings,
		Running:    m.cmd != nil && m.cdpURL != "",
		Takeover:   m.takeover,
		CDPURL:     m.cdpURL,
		Executable: m.executable,
		ProfileDir: m.ProfileDir(),
		StartedAt:  m.startedAt,
		LastError:  m.lastError,
	}
	configured := m.settings.Executable
	m.mu.RUnlock()
	if path, err := agent.FindBrowserExecutable(configured); err == nil {
		status.Available = true
		if status.Executable == "" {
			status.Executable = path
		}
	}
	return status
}

// SetSettings 保存配置，并按 Enabled 的新值启停进程。
func (m *Manager) SetSettings(ctx context.Context, next Settings) (Settings, error) {
	if m == nil {
		return Settings{}, errors.New("内置浏览器未初始化")
	}
	next = next.WithDefaults()
	// 起不来的有头配置在落盘前就挡掉：存下去的话，正在跑的无头会被这次重启杀掉，
	// 换来一个永远起不来的开关，用户下次打开界面看到的是「开着但没在跑」。
	if next.Enabled {
		if err := checkHeadful(next); err != nil {
			return m.Settings(), err
		}
	}
	m.mu.Lock()
	previous := m.settings
	m.settings = next
	m.mu.Unlock()
	if m.store != nil {
		if err := m.store.SaveBrowserBox(ctx, Document{Settings: next}); err != nil {
			m.mu.Lock()
			m.settings = previous
			m.mu.Unlock()
			return previous, err
		}
	}
	switch {
	case !next.Enabled:
		m.Stop()
	case !previous.Enabled && next.Enabled:
		if err := m.Start(ctx); err != nil {
			m.setLastError(err)
			return next, err
		}
	case renderingChanged(previous, next):
		// 尺寸或无头模式变了要重开进程才生效，但登录态不受影响：profile 目录没动。
		m.Stop()
		if err := m.Start(ctx); err != nil {
			m.setLastError(err)
			return next, err
		}
	}
	m.notify()
	return next, nil
}

func renderingChanged(previous, next Settings) bool {
	return previous.Headful != next.Headful ||
		previous.WindowWidth != next.WindowWidth ||
		previous.WindowHeight != next.WindowHeight ||
		previous.Executable != next.Executable
}

// SetTakeover 切换人工接管。接管打开时模型拿不到 CDP 地址，一条指令都下不去，
// 用户自己在实时画面里点。
func (m *Manager) SetTakeover(active bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.takeover = active
	m.mu.Unlock()
	m.notify()
}

// Takeover 返回当前接管状态。
func (m *Manager) Takeover() bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.takeover
}

// CDPURL 返回调试地址，给 WebUI 的实时画面用。它不看接管状态：接管时用户
// 自己要操作，画面和输入照样得通。
func (m *Manager) CDPURL() string {
	if m == nil {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cdpURL
}

// AgentCDPURL 返回交给模型那一侧的调试地址。没开、没起来或有人在接管时返回空串，
// 对应的效果是 browser_* 那组工具在这台机器人上根本不登记。
func (m *Manager) AgentCDPURL() string {
	if m == nil {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.settings.Enabled || m.takeover {
		return ""
	}
	return m.cdpURL
}

// Start 拉起浏览器进程。已经在跑就什么都不做。
func (m *Manager) Start(ctx context.Context) error {
	if m == nil {
		return errors.New("内置浏览器未初始化")
	}
	m.mu.Lock()
	if m.cmd != nil {
		m.mu.Unlock()
		return nil
	}
	settings := m.settings
	m.stopping = false
	m.generation++
	generation := m.generation
	m.mu.Unlock()

	if err := checkHeadful(settings); err != nil {
		return err
	}

	executable, err := agent.FindBrowserExecutable(settings.Executable)
	if err != nil {
		return fmt.Errorf("找不到 Chrome/Chromium：%w", err)
	}
	for _, dir := range []string{m.ProfileDir(), m.cacheDir(), m.crashDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("建浏览器数据目录失败：%w", err)
		}
	}
	clearSingletonLocks(m.ProfileDir())
	args := agent.PersistentBrowserArgs(m.ProfileDir(), m.cacheDir(), m.crashDir(),
		!settings.Headful, 0, settings.WindowWidth, settings.WindowHeight)
	cmd := exec.Command(executable, args...)
	cmd.Dir = m.rootDir()
	cmd.Env = agent.BrowserLaunchEnvironment(os.Environ(), m.rootDir())
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("读浏览器输出失败：%w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动浏览器失败：%w", err)
	}

	found := make(chan string, 1)
	diagnostics := &diagnosticTail{limit: 2048}
	go func() {
		defer recoverGoroutinePanic("scanEndpoint")
		scanDevToolsEndpoint(io.TeeReader(stderr, diagnostics), found)
	}()
	// exited 由 waitProcess 在 cmd.Wait() 一返回就关掉，而不是等它做完退避重启：
	// 关在整个函数末尾的话，下面那个「进程自己退了就别再等满超时」的分支永远轮不到，
	// 每一次起不来都要白等 30 秒。
	exited := make(chan struct{})
	go func() {
		defer recoverGoroutinePanic("waitProcess")
		m.waitProcess(cmd, generation, diagnostics, exited)
	}()

	select {
	case wsURL := <-found:
		httpURL, err := debugHTTPBase(wsURL)
		if err != nil {
			m.Stop()
			return err
		}
		m.mu.Lock()
		m.cmd = cmd
		m.cdpURL = httpURL
		m.executable = executable
		m.startedAt = time.Now()
		m.lastError = ""
		m.mu.Unlock()
		m.notify()
		return nil
	case <-exited:
		// 进程自己退了就别再等满超时：绝大多数情况是 profile 被占用或者缺依赖，
		// 原因就在它刚打印的那几行里，等 30 秒只是把这条信息推迟 30 秒。
		return fmt.Errorf("浏览器启动后立即退出：%s", diagnostics.String())
	case <-time.After(launchTimeout):
		_ = cmd.Process.Kill()
		return fmt.Errorf("浏览器启动超时：没有等到 DevTools 调试地址。最后的输出：%s", diagnostics.String())
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return ctx.Err()
	}
}

// Stop 结束进程。登录态留在 profile 目录里，下次起来还在。
func (m *Manager) Stop() {
	if m == nil {
		return
	}
	m.mu.Lock()
	cmd := m.cmd
	m.stopping = true
	m.cmd = nil
	m.cdpURL = ""
	m.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	m.notify()
}

// waitProcess 等进程退出。开关还开着、又不是我们主动停的，就按退避重启：
// 浏览器崩掉之后静悄悄地没了，比崩掉本身更难排查。
func (m *Manager) waitProcess(cmd *exec.Cmd, generation uint64, diagnostics *diagnosticTail, exited chan<- struct{}) {
	err := cmd.Wait()
	close(exited)
	m.mu.Lock()
	current := m.generation == generation
	stopping := m.stopping
	enabled := m.settings.Enabled
	if current {
		m.cmd = nil
		m.cdpURL = ""
		if !stopping && err != nil {
			// 只写 exit status 1 等于没说：真正的原因（缺显示器、profile 被占用、
			// 缺依赖）在进程自己打印的那几行里，状态里不带上就只能去翻后台日志。
			m.lastError = exitErrorMessage(err, diagnostics)
		}
	}
	m.mu.Unlock()
	if !current || stopping || !enabled {
		return
	}
	m.notify()
	time.Sleep(restartBackoff)
	// 退避期间配置可能已经改过，新的进程也可能已经起来了。generation 变了就说明
	// 这一条重启链已经被接替，继续下去只会把新状态的错误信息覆盖成旧的那条。
	m.mu.RLock()
	stale := m.generation != generation
	stillEnabled := m.settings.Enabled && m.cmd == nil
	m.mu.RUnlock()
	if stale || !stillEnabled {
		return
	}
	if err := m.Start(context.Background()); err != nil {
		m.setLastError(err)
	}
}

// exitErrorMessage 把退出码和进程最后的输出拼成一句能照着查的话。
func exitErrorMessage(err error, diagnostics *diagnosticTail) string {
	message := "浏览器进程退出：" + err.Error()
	if diagnostics == nil {
		return message
	}
	if tail := diagnostics.String(); tail != "" {
		message += "。最后的输出：" + tail
	}
	return message
}

func (m *Manager) setLastError(err error) {
	if err == nil {
		return
	}
	m.mu.Lock()
	m.lastError = err.Error()
	m.mu.Unlock()
	m.notify()
}

// Watch 返回一个状态变更通知通道，WebUI 用它把状态推给前端。
func (m *Manager) Watch() (<-chan struct{}, func()) {
	if m == nil {
		closed := make(chan struct{})
		close(closed)
		return closed, func() {}
	}
	ch := make(chan struct{}, 1)
	m.mu.Lock()
	m.watchers[ch] = struct{}{}
	m.mu.Unlock()
	return ch, func() {
		m.mu.Lock()
		delete(m.watchers, ch)
		m.mu.Unlock()
	}
}

func (m *Manager) notify() {
	m.mu.RLock()
	watchers := make([]chan struct{}, 0, len(m.watchers))
	for ch := range m.watchers {
		watchers = append(watchers, ch)
	}
	m.mu.RUnlock()
	for _, ch := range watchers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// clearSingletonLocks 清掉上一次运行留下的单实例锁。容器被强制删掉时锁文件会
// 留在 profile 里，指向一个早就不存在的进程，新进程于是要么拒绝启动、要么卡住，
// 而用户看到的只是「启动超时」。锁文件是可以安全删的：真有另一个进程在用这份
// profile 的话，它自己会重新建。
func clearSingletonLocks(profileDir string) {
	for _, name := range []string{"SingletonLock", "SingletonSocket", "SingletonCookie"} {
		_ = os.Remove(filepath.Join(profileDir, name))
	}
}

// diagnosticTail 留着浏览器最后几行输出，启动失败时把原因带出去。
type diagnosticTail struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (d *diagnosticTail) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.buf = append(d.buf, p...)
	if len(d.buf) > d.limit {
		d.buf = d.buf[len(d.buf)-d.limit:]
	}
	return len(p), nil
}

func (d *diagnosticTail) String() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strings.TrimSpace(string(d.buf))
}

func scanDevToolsEndpoint(reader io.Reader, found chan<- string) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	sent := false
	for scanner.Scan() {
		line := scanner.Text()
		if sent {
			continue
		}
		if match := devToolsLine.FindStringSubmatch(line); len(match) == 2 {
			found <- match[1]
			sent = true
		}
	}
}

// debugHTTPBase 把 ws://127.0.0.1:PORT/devtools/browser/<id> 换成 http://127.0.0.1:PORT。
func debugHTTPBase(wsURL string) (string, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(wsURL), "ws://")
	host, _, found := strings.Cut(trimmed, "/")
	if !found || host == "" {
		return "", fmt.Errorf("看不懂浏览器报出的调试地址：%s", wsURL)
	}
	return "http://" + host, nil
}

// Unavailable 说明现在为什么用不了内置浏览器。这句话会原样交给模型，
// 所以要能让它知道下一步该干什么：是去开开关，还是等用户交还控制权。
func (m *Manager) Unavailable() string {
	if m == nil {
		return "内置浏览器未初始化"
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	switch {
	case !m.settings.Enabled:
		return ""
	case m.takeover:
		return "用户正在人工接管内置浏览器，本轮不要操作它；等用户交还控制权再试"
	case m.cdpURL == "":
		if m.lastError != "" {
			return "内置浏览器没有运行：" + m.lastError
		}
		return "内置浏览器还没起来，稍后再试"
	}
	return ""
}
