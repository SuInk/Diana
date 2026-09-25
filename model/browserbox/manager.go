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
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/internal/procgroup"
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

// Status 是管理接口看到的运行状态。Bot 为空时只有配置和 Available，进程相关的
// 字段都属于某一台机器人。
type Status struct {
	Settings   Settings  `json:"settings"`
	Bot        string    `json:"bot,omitempty"`
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
	// TakeoverIdleTimeout 是人工接管闲置多久后自动交还给机器人。
	//
	// 接管期间模型的每一次浏览器调用都会被拒，而用户很容易点完就走开、忘了交还——
	// 以前接管没有期限，机器人就一直用不了浏览器，直到有人回来点「交还」。五分钟
	// 足够填完一张表、等一条短信验证码；真要更久，再点一下画面就重新接管了。
	TakeoverIdleTimeout = 5 * time.Minute
	// TakeoverLeaveGrace 是接管中的人离开画面（切到别的页面、关掉标签页、网页被切到后台）
	// 之后多久交还给机器人。
	//
	// 浏览器默认就是机器人在用，人不看了就该马上还回去，不必干等闲置的五分钟。留这
	// 几秒只为断线重连：中间的代理掐掉长连接时，前端半秒左右就连回来，不能因此把人
	// 正在做的事打断。
	TakeoverLeaveGrace = 3 * time.Second
)

// 自动交还的原因，操作记录里用它区分。
const (
	AutoReleaseIdle = "idle"
	AutoReleaseLeft = "left"
)

var devToolsLine = regexp.MustCompile(`DevTools listening on (ws://[^\s]+)`)

// Manager 看管内置浏览器：一份全局配置，加上每台机器人各自的一个进程。
//
// 每台机器人各用一个 profile、各起一个进程，是为了让登录态互不相通：A 机器人里登录
// 的账号，B 机器人看不到也用不了。Chrome 的 BrowserContext 也能隔离，但它不落盘，
// 重启就丢登录态，而「登录一次以后一直有效」正是这一档的意义，所以只能分进程。
//
// 进程按需起：机器人第一次要用、或者用户在 WebUI 上点启动时才拉起，不会一开机就
// 为每台机器人各起一个 Chrome。
type Manager struct {
	store   Store
	dataDir string

	mu       sync.RWMutex
	settings Settings
	bots     map[string]*instance
	watchers map[chan struct{}]struct{}
	// saved 表示配置落过盘。没落过盘的才轮得到 EnableByDefault 按本机条件自动打开。
	saved bool

	// now、takeoverIdle 和 takeoverLeave 是自动交还用的时钟和期限，测试里替换掉。
	now           func() time.Time
	takeoverIdle  time.Duration
	takeoverLeave time.Duration
	// onAutoRelease 在自动交还后调用，WebUI 用它记一条操作记录。
	onAutoRelease func(botID, reason string, after time.Duration)
}

// instance 是一台机器人的浏览器进程。字段都由 Manager.mu 保护。
type instance struct {
	id         string
	cmd        *exec.Cmd
	display    *virtualDisplay
	cdpURL     string
	executable string
	startedAt  time.Time
	lastError  string
	takeover   bool
	// takeoverTouched 是接管期间人最后一次有意操作的时间，idleTimer 按它判断闲置。
	takeoverTouched time.Time
	idleTimer       *time.Timer
	// viewers 是正在看这台机器人实时画面的连接数；降到零时 leaveTimer 开始计时。
	viewers    int
	leaveTimer *time.Timer
	stopping   bool
	// generation 用来分辨「这次退出属于哪一次启动」，避免旧进程的退出把新进程的
	// 状态清掉——和扩展那边旧 socket 的 close 是同一类坑。
	generation uint64
	// starting 在拉起过程中非空，同一台机器人的并发启动等同一次结果。
	starting chan struct{}
	startErr error
}

// New 创建管理器并读取已保存的配置。不在这里拉起任何进程：进程按机器人按需起。
func New(ctx context.Context, store Store, dataDir string) *Manager {
	m := &Manager{
		store:    store,
		dataDir:  strings.TrimSpace(dataDir),
		settings: Settings{}.WithDefaults(),
		bots:     map[string]*instance{},
		watchers: map[chan struct{}]struct{}{},

		now:           time.Now,
		takeoverIdle:  TakeoverIdleTimeout,
		takeoverLeave: TakeoverLeaveGrace,
	}
	if store != nil {
		if doc, ok, err := store.LoadBrowserBox(ctx); err == nil && ok {
			m.settings = doc.Settings.WithDefaults()
			m.saved = true
		}
	}
	return m
}

// 本机条件的探测，测试里替换掉。
var (
	findBrowserExecutable = func() bool {
		_, err := agent.FindBrowserExecutable("")
		return err == nil
	}
	headfulDisplayAvailable = func() bool { return systemDisplayAvailable() || xvfbAvailable() }
)

// EnableByDefault 在内置浏览器从没保存过配置时，按本机条件替用户打开它：找得到
// Chrome/Chromium 就开；有显示器，或者能自己拉起 Xvfb（完整版容器镜像自带），就开
// 真窗口，否则无头。返回这次有没有打开。
//
// 只看「落没落过盘」：用户关过、改过的配置一律不动。找不到浏览器时也不落盘，这样
// 以后装上了，下次启动还会再探测一次。
func (m *Manager) EnableByDefault(ctx context.Context) (bool, error) {
	if m == nil {
		return false, nil
	}
	m.mu.RLock()
	saved := m.saved
	next := m.settings
	m.mu.RUnlock()
	if saved || next.Enabled || !findBrowserExecutable() {
		return false, nil
	}
	next.Enabled = true
	next.Headful = headfulDisplayAvailable()
	if _, err := m.SetSettings(ctx, next); err != nil {
		return true, err
	}
	return true, nil
}

func (m *Manager) rootDir() string     { return filepath.Join(m.dataDir, "browser-box") }
func (m *Manager) profilesDir() string { return filepath.Join(m.rootDir(), "profiles") }

// legacyProfileDir 是按机器人拆分之前那一份共用的 profile。
func (m *Manager) legacyProfileDir() string { return filepath.Join(m.rootDir(), "profile") }

// botDir 是一台机器人的数据目录：profile、缓存和崩溃转储都在下面。
func (m *Manager) botDir(id string) string { return filepath.Join(m.profilesDir(), id) }

// ProfileDir 是一台机器人登录态所在的目录。放在数据目录下而不是临时目录：这一档的
// 全部意义就是「登录一次以后一直有效」，profile 跟着挂卷走才谈得上持久。
func (m *Manager) ProfileDir(botID string) string {
	if m == nil || m.dataDir == "" {
		return ""
	}
	return filepath.Join(m.botDir(normalizeBotID(botID)), "profile")
}

// AdoptLegacyProfile 把拆分之前那份共用的 profile 交给 botID，登录态不丢。只在那台
// 机器人还没有自己的 profile 时搬；搬过之后旧目录就不在了，重复调用没有副作用。
func (m *Manager) AdoptLegacyProfile(botID string) error {
	if m == nil || m.dataDir == "" || strings.TrimSpace(botID) == "" {
		return nil
	}
	legacy := m.legacyProfileDir()
	if _, err := os.Stat(legacy); err != nil {
		return nil
	}
	target := m.ProfileDir(botID)
	if _, err := os.Stat(target); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("建机器人浏览器目录失败：%w", err)
	}
	if err := os.Rename(legacy, target); err != nil {
		return fmt.Errorf("迁移旧的浏览器登录态失败：%w", err)
	}
	return nil
}

// normalizeBotID 把机器人 ID 变成能直接当目录名的样子。ID 本来就是字母数字和连字符，
// 这里只是防御：带路径分隔符或别的字符的 ID 不能拿去拼路径。
func normalizeBotID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// instanceLocked 取（没有就建）一台机器人的实例。调用方持有写锁。
func (m *Manager) instanceLocked(id string) *instance {
	inst := m.bots[id]
	if inst == nil {
		inst = &instance{id: id}
		m.bots[id] = inst
	}
	return inst
}

// Settings 返回当前配置副本。
func (m *Manager) Settings() Settings {
	if m == nil {
		return Settings{}.WithDefaults()
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.settings
}

// Status 汇总配置和本机能不能找到浏览器，不涉及任何一台机器人。
func (m *Manager) Status() Status {
	return m.statusFor("")
}

func (m *Manager) statusFor(botID string) Status {
	if m == nil {
		return Status{Settings: Settings{}.WithDefaults()}
	}
	m.mu.RLock()
	status := Status{Settings: m.settings}
	if botID != "" {
		id := normalizeBotID(botID)
		status.Bot = id
		status.ProfileDir = m.ProfileDir(id)
		if inst := m.bots[id]; inst != nil {
			status.Running = inst.cmd != nil && inst.cdpURL != ""
			status.Takeover = inst.takeover
			status.CDPURL = inst.cdpURL
			status.Executable = inst.executable
			status.StartedAt = inst.startedAt
			status.LastError = inst.lastError
		}
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

// SetSettings 保存配置。关掉就停掉所有机器人的进程；无头/尺寸/程序路径变了就把正在
// 跑的那些重开。打开时不主动起进程，由各台机器人按需起。
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
		m.mu.Lock()
		m.saved = true
		m.mu.Unlock()
	}
	var firstErr error
	switch {
	case !next.Enabled:
		m.StopAll()
	case renderingChanged(previous, next):
		// 尺寸或无头模式变了要重开进程才生效，但登录态不受影响：profile 目录没动。
		for _, id := range m.runningBots() {
			m.stop(id)
			if err := m.start(ctx, id); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	m.notify()
	return next, firstErr
}

func renderingChanged(previous, next Settings) bool {
	return previous.Headful != next.Headful ||
		previous.WindowWidth != next.WindowWidth ||
		previous.WindowHeight != next.WindowHeight ||
		previous.Executable != next.Executable
}

func (m *Manager) runningBots() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.bots))
	for id, inst := range m.bots {
		if inst.cmd != nil {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// StopAll 停掉所有机器人的浏览器进程。登录态留在各自的 profile 里。
func (m *Manager) StopAll() {
	if m == nil {
		return
	}
	for _, id := range m.runningBots() {
		m.stop(id)
	}
}

// Stop 是 StopAll 的旧名字，进程退出时 defer 调它。
func (m *Manager) Stop() { m.StopAll() }

// Bot 返回一台机器人的浏览器句柄。它同时是交给工具那一侧的 agent.BuiltinBrowserBridge。
func (m *Manager) Bot(botID string) *Bot {
	return &Bot{m: m, id: normalizeBotID(botID)}
}

// BrowserFor 实现 assistant.BuiltinBrowserProvider：运行时按机器人取浏览器。
func (m *Manager) BrowserFor(botID string) agent.BuiltinBrowserBridge {
	return m.Bot(botID)
}

// Bot 是一台机器人的内置浏览器。
type Bot struct {
	m  *Manager
	id string
}

// ID 是规范化后的机器人 ID。
func (b *Bot) ID() string { return b.id }

// Status 返回这台机器人的状态。
func (b *Bot) Status() Status { return b.m.statusFor(b.id) }

// AllowsURL 实现 agent.BuiltinBrowserURLPolicy：机器人主动打开的地址和实时画面里
// 手动打开的一样，要过 denied_hosts。
func (b *Bot) AllowsURL(rawURL string) bool { return b.m.Settings().HostAllowed(rawURL) }

// Start 拉起这台机器人的浏览器。已经在跑就什么都不做。
func (b *Bot) Start(ctx context.Context) error {
	if !b.m.Settings().Enabled {
		return errors.New("内置浏览器没有打开：先在「浏览器」页把来源选成「Diana 内置」")
	}
	return b.m.start(ctx, b.id)
}

// Stop 结束这台机器人的浏览器。登录态留在 profile 目录里，下次起来还在。
func (b *Bot) Stop() { b.m.stop(b.id) }

// SetTakeover 切换人工接管。接管打开时模型拿不到这台机器人的浏览器，一条指令都
// 下不去，用户自己在实时画面里点。接管闲置 TakeoverIdleTimeout、或者人离开画面
// TakeoverLeaveGrace 之后自动交还。
func (b *Bot) SetTakeover(active bool) {
	b.m.mu.Lock()
	inst := b.m.instanceLocked(b.id)
	inst.takeover = active
	if active {
		inst.takeoverTouched = b.m.now()
		b.m.armIdleTimerLocked(inst, b.m.takeoverIdle)
		// 没人在看画面就接管（比如直接调接口）等于人已经不在了。
		if inst.viewers == 0 {
			b.m.armLeaveTimerLocked(inst)
		}
	} else {
		stopTakeoverTimersLocked(inst)
	}
	b.m.mu.Unlock()
	b.m.notify()
}

// AttachViewer 记下有人开始看这台机器人的实时画面，返回的函数在不看了（画面连接断开）
// 时调用，多调用几次也只算一次。最后一个人离开时如果还在接管，过 TakeoverLeaveGrace
// 没人回来就自动交还。
func (b *Bot) AttachViewer() (detach func()) {
	b.m.mu.Lock()
	inst := b.m.instanceLocked(b.id)
	inst.viewers++
	if inst.leaveTimer != nil {
		inst.leaveTimer.Stop()
		inst.leaveTimer = nil
	}
	b.m.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			b.m.mu.Lock()
			defer b.m.mu.Unlock()
			if inst.viewers > 0 {
				inst.viewers--
			}
			if inst.viewers == 0 && inst.takeover {
				b.m.armLeaveTimerLocked(inst)
			}
		})
	}
}

func stopTakeoverTimersLocked(inst *instance) {
	if inst.idleTimer != nil {
		inst.idleTimer.Stop()
		inst.idleTimer = nil
	}
	if inst.leaveTimer != nil {
		inst.leaveTimer.Stop()
		inst.leaveTimer = nil
	}
}

// TouchTakeover 记下接管期间的一次有意操作，把闲置交还往后推。没在接管时什么都
// 不做：碰一下不该顺手把接管打开，那是调用方按输入种类决定的事。
func (b *Bot) TouchTakeover() {
	b.m.mu.Lock()
	defer b.m.mu.Unlock()
	if inst := b.m.bots[b.id]; inst != nil && inst.takeover {
		inst.takeoverTouched = b.m.now()
	}
}

// OnAutoRelease 注册自动交还的回调：reason 是 AutoReleaseIdle 或 AutoReleaseLeft，
// after 是对应的期限。回调在锁外、在定时器的 goroutine 里调用。
func (m *Manager) OnAutoRelease(fn func(botID, reason string, after time.Duration)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.onAutoRelease = fn
	m.mu.Unlock()
}

// armLeaveTimerLocked 在人离开画面时开始计时，到点还没人回来就交还。
func (m *Manager) armLeaveTimerLocked(inst *instance) {
	if inst.leaveTimer != nil {
		inst.leaveTimer.Stop()
	}
	id := inst.id
	inst.leaveTimer = time.AfterFunc(m.takeoverLeave, func() {
		defer recoverGoroutinePanic("takeoverLeave")
		m.releaseLeftTakeover(id)
	})
}

// releaseLeftTakeover 在人离开画面满宽限时交还给机器人，返回这次有没有交还。
func (m *Manager) releaseLeftTakeover(id string) bool {
	m.mu.Lock()
	inst := m.bots[id]
	if inst == nil || !inst.takeover || inst.viewers > 0 {
		m.mu.Unlock()
		return false
	}
	return m.autoReleaseLocked(inst, AutoReleaseLeft, m.takeoverLeave)
}

// autoReleaseLocked 交还接管、解锁，然后通知和回调。调用方持有写锁，返回时锁已释放。
func (m *Manager) autoReleaseLocked(inst *instance, reason string, after time.Duration) bool {
	inst.takeover = false
	stopTakeoverTimersLocked(inst)
	hook := m.onAutoRelease
	id := inst.id
	m.mu.Unlock()
	m.notify()
	if hook != nil {
		hook(id, reason, after)
	}
	return true
}

// armIdleTimerLocked 在 after 之后检查一次闲置。每次有意操作只改时间戳、不重设
// 定时器：键盘一秒能打出十几条，逐条 Reset 没有必要；到点时没闲够就按剩下的再等。
func (m *Manager) armIdleTimerLocked(inst *instance, after time.Duration) {
	if inst.idleTimer != nil {
		inst.idleTimer.Stop()
	}
	id := inst.id
	inst.idleTimer = time.AfterFunc(after, func() {
		defer recoverGoroutinePanic("takeoverIdle")
		m.releaseIdleTakeover(id)
	})
}

// releaseIdleTakeover 在接管闲置满期限时交还给机器人，返回这次有没有交还。
func (m *Manager) releaseIdleTakeover(id string) bool {
	m.mu.Lock()
	inst := m.bots[id]
	if inst == nil || !inst.takeover {
		m.mu.Unlock()
		return false
	}
	idle := m.now().Sub(inst.takeoverTouched)
	if remaining := m.takeoverIdle - idle; remaining > 0 {
		m.armIdleTimerLocked(inst, remaining)
		m.mu.Unlock()
		return false
	}
	return m.autoReleaseLocked(inst, AutoReleaseIdle, m.takeoverIdle)
}

// Takeover 返回这台机器人当前是否由人接管。
func (b *Bot) Takeover() bool {
	b.m.mu.RLock()
	defer b.m.mu.RUnlock()
	if inst := b.m.bots[b.id]; inst != nil {
		return inst.takeover
	}
	return false
}

// CDPURL 返回调试地址，给 WebUI 的实时画面用。它不看接管状态：接管时用户自己要
// 操作，画面和输入照样得通。
func (b *Bot) CDPURL() string {
	b.m.mu.RLock()
	defer b.m.mu.RUnlock()
	if inst := b.m.bots[b.id]; inst != nil {
		return inst.cdpURL
	}
	return ""
}

// Endpoint 返回交给模型那一侧的调试地址，实现 agent.BuiltinBrowserBridge。
//
// 没打开时返回空串和 nil，工具那一侧回落到机器人配置里的外部 CDP 地址；打开了但
// 进程还没起来就当场拉起；有人在接管或起不来时返回一句模型能看懂的原因。拉起不跟着
// 这次工具调用的超时走：起到一半被取消的话，下一次调用还得从头再等一遍。
func (b *Bot) Endpoint(ctx context.Context) (string, error) {
	b.m.mu.RLock()
	enabled := b.m.settings.Enabled
	var takeover bool
	var cdpURL string
	if inst := b.m.bots[b.id]; inst != nil {
		takeover = inst.takeover
		cdpURL = inst.cdpURL
	}
	b.m.mu.RUnlock()
	switch {
	case !enabled:
		return "", nil
	case takeover:
		return "", errors.New("用户正在人工接管内置浏览器，本轮不要操作它；等用户交还控制权再试")
	case cdpURL != "":
		return cdpURL, nil
	}
	if err := b.m.start(context.WithoutCancel(ctx), b.id); err != nil {
		return "", fmt.Errorf("内置浏览器没能启动：%w", err)
	}
	if url := b.CDPURL(); url != "" {
		return url, nil
	}
	return "", errors.New("内置浏览器还没起来，稍后再试")
}

// start 拉起一台机器人的浏览器进程。已经在跑就什么都不做；正在起就等那一次的结果。
func (m *Manager) start(ctx context.Context, id string) error {
	m.mu.Lock()
	inst := m.instanceLocked(id)
	if inst.cmd != nil {
		m.mu.Unlock()
		return nil
	}
	if inst.starting != nil {
		wait := inst.starting
		m.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
		m.mu.RLock()
		err := inst.startErr
		m.mu.RUnlock()
		return err
	}
	done := make(chan struct{})
	inst.starting = done
	settings := m.settings
	inst.stopping = false
	inst.generation++
	generation := inst.generation
	m.mu.Unlock()

	err := m.launch(ctx, inst, settings, generation)
	m.mu.Lock()
	inst.starting = nil
	inst.startErr = err
	if err != nil {
		inst.lastError = err.Error()
	}
	m.mu.Unlock()
	close(done)
	m.notify()
	return err
}

func (m *Manager) launch(ctx context.Context, inst *instance, settings Settings, generation uint64) error {
	if err := checkHeadful(settings); err != nil {
		return err
	}
	// 有头而又没有现成的图形会话时，自己拉一块虚拟屏。容器里的有头就是这么跑的：
	// 浏览器在 Xvfb 上真开窗口，画面照旧走 screencast，不需要 VNC。
	var display *virtualDisplay
	if settings.Headful && !systemDisplayAvailable() {
		created, err := startVirtualDisplay(settings.WindowWidth, settings.WindowHeight)
		if err != nil {
			return err
		}
		display = created
	}
	// 后面任何一条失败的出路都要把它带走，否则每失败一次就多留一个 Xvfb。
	started := false
	defer func() {
		if !started {
			display.Stop()
		}
	}()

	executable, err := agent.FindBrowserExecutable(settings.Executable)
	if err != nil {
		return fmt.Errorf("找不到 Chrome/Chromium：%w", err)
	}
	dir := m.botDir(inst.id)
	profileDir := filepath.Join(dir, "profile")
	cacheDir := filepath.Join(dir, "cache")
	crashDir := filepath.Join(dir, "crash")
	for _, path := range []string{profileDir, cacheDir, crashDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("建浏览器数据目录失败：%w", err)
		}
	}
	clearSingletonLocks(profileDir)
	tmpDir, err := shortTempDir()
	if err != nil {
		return fmt.Errorf("建浏览器临时目录失败：%w", err)
	}
	// 进程起来之后由 waitProcess 那条 goroutine 在它退出时收掉；起不来就在这里收。
	keepTmp := false
	defer func() {
		if !keepTmp {
			_ = os.RemoveAll(tmpDir)
		}
	}()
	args := agent.PersistentBrowserArgs(profileDir, cacheDir, crashDir,
		!settings.Headful, 0, settings.WindowWidth, settings.WindowHeight)
	cmd := procgroup.Isolate(exec.Command(executable, args...))
	cmd.Dir = dir
	// TMPDIR 不能跟着 dir 走：Chrome 在 TMPDIR 下建单实例用的 Unix 套接字，路径上限
	// 108 字节（macOS 104），而 dir 是「数据目录/browser-box/profiles/<机器人 UUID>」，
	// 容器里光这一段就七十多字节，再拼上 org.chromium.Chromium.XXXXXX/SingletonSocket
	// 就超了，Chrome 报「Socket path too long」直接退出。后写的同名变量覆盖前面的。
	cmd.Env = append(agent.BrowserLaunchEnvironment(os.Environ(), dir), display.Env()...)
	cmd.Env = append(cmd.Env, "TMPDIR="+tmpDir)
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
	keepTmp = true
	go func() {
		defer recoverGoroutinePanic("waitProcess")
		// 放在 recover 之后注册，先执行：进程没了临时目录就该走，panic 也一样。
		defer os.RemoveAll(tmpDir)
		m.waitProcess(inst, cmd, generation, diagnostics, exited)
	}()

	select {
	case wsURL := <-found:
		httpURL, err := debugHTTPBase(wsURL)
		if err != nil {
			_ = procgroup.Kill(cmd)
			return err
		}
		started = true
		m.mu.Lock()
		inst.cmd = cmd
		inst.display = display
		inst.cdpURL = httpURL
		inst.executable = executable
		inst.startedAt = time.Now()
		inst.lastError = ""
		m.mu.Unlock()
		return nil
	case <-exited:
		// 进程自己退了就别再等满超时：绝大多数情况是 profile 被占用或者缺依赖，
		// 原因就在它刚打印的那几行里，等 30 秒只是把这条信息推迟 30 秒。
		return fmt.Errorf("浏览器启动后立即退出：%s", diagnostics.String())
	case <-time.After(launchTimeout):
		_ = procgroup.Kill(cmd)
		return fmt.Errorf("浏览器启动超时：没有等到 DevTools 调试地址。最后的输出：%s", diagnostics.String())
	case <-ctx.Done():
		_ = procgroup.Kill(cmd)
		return ctx.Err()
	}
}

// stop 结束一台机器人的进程。
func (m *Manager) stop(id string) {
	m.mu.Lock()
	inst := m.bots[id]
	if inst == nil {
		m.mu.Unlock()
		return
	}
	cmd := inst.cmd
	display := inst.display
	inst.stopping = true
	inst.cmd = nil
	inst.display = nil
	inst.cdpURL = ""
	m.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = procgroup.Kill(cmd)
	}
	// 浏览器没了这块虚拟屏就没人用了，留着只是一个占内存的孤儿进程。
	display.Stop()
	m.notify()
}

// waitProcess 等进程退出。开关还开着、又不是我们主动停的，就按退避重启：
// 浏览器崩掉之后静悄悄地没了，比崩掉本身更难排查。
func (m *Manager) waitProcess(inst *instance, cmd *exec.Cmd, generation uint64, diagnostics *diagnosticTail, exited chan<- struct{}) {
	err := cmd.Wait()
	close(exited)
	m.mu.Lock()
	current := inst.generation == generation && inst.cmd == cmd
	stopping := inst.stopping
	enabled := m.settings.Enabled
	display := inst.display
	if current {
		inst.cmd = nil
		inst.display = nil
		inst.cdpURL = ""
		if !stopping && err != nil {
			// 只写 exit status 1 等于没说：真正的原因（缺显示器、profile 被占用、
			// 缺依赖）在进程自己打印的那几行里，状态里不带上就只能去翻后台日志。
			inst.lastError = exitErrorMessage(err, diagnostics)
		}
	}
	m.mu.Unlock()
	if current {
		// 这一次的进程走完了，它那块屏也跟着走：重启会另开一块。
		display.Stop()
	}
	if !current || stopping || !enabled {
		return
	}
	m.notify()
	time.Sleep(restartBackoff)
	// 退避期间配置可能已经改过，新的进程也可能已经起来了。generation 变了就说明
	// 这一条重启链已经被接替，继续下去只会把新状态的错误信息覆盖成旧的那条。
	m.mu.RLock()
	stale := inst.generation != generation
	stillEnabled := m.settings.Enabled && inst.cmd == nil && !inst.stopping
	m.mu.RUnlock()
	if stale || !stillEnabled {
		return
	}
	_ = m.start(context.Background(), inst.id)
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

// shortTempDir 给浏览器建一个路径足够短的临时目录，理由见 launch 里 TMPDIR 那段。
// 类 Unix 系统优先用 /tmp：os.TempDir() 在 macOS 上是 /var/folders/... 下一长串，
// 本身就吃掉一半预算。
func shortTempDir() (string, error) {
	root := os.TempDir()
	if runtime.GOOS != "windows" {
		if info, err := os.Stat("/tmp"); err == nil && info.IsDir() {
			root = "/tmp"
		}
	}
	return os.MkdirTemp(root, "dbx-")
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
