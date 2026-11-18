// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const personalBrowserIdleTimeout = 5 * time.Minute

// UserSessionPool keeps sender-scoped profiles outside the agent workspace.
// Reply leases serialize a user's tools; idle browsers briefly remain alive so
// QR login polling can finish after the reply containing the image is sent.
type UserSessionPool struct {
	root       string
	executable func() string
	// sandbox 是给群友用的公网沙箱：按会话分 profile、任何公网网站都能开、关掉就删
	// profile，不留登录态。个人浏览器那种按发送者长期保存 profile 的池子这里是 false。
	sandbox  bool
	mu       sync.Mutex
	profiles map[string]*userBrowserProfile
	active   map[*UserBrowserSession]bool
	slots    chan struct{}
	changed  chan struct{}
}

type userBrowserProfile struct {
	gate    chan struct{}
	dataDir string
	// tabID 是沙箱这个会话上一轮停在的标签页：群里下一句「选 B」要接着点同一页。
	// 个人浏览器每轮从自己打开的页开始，不用它。
	tabID    string
	manager  *Bot
	proxy    *userBrowserProxy
	hosts    []string
	idle     *time.Timer
	lastUsed time.Time
}

func NewUserSessionPool(dataDir string, executable func() string) *UserSessionPool {
	return &UserSessionPool{root: filepath.Join(dataDir, "user-browsers"), executable: executable, profiles: map[string]*userBrowserProfile{}, active: map[*UserBrowserSession]bool{}, slots: make(chan struct{}, 4), changed: make(chan struct{}, 1)}
}

// NewSandboxSessionPool 是群友沙箱浏览器的池子。profile 只活在一次会话里：进程关掉
// 就删目录，启动时也先清掉上次异常退出留下的目录。进程数单独限 3 个，不和个人浏览器
// 抢名额。
func NewSandboxSessionPool(dataDir string, executable func() string) *UserSessionPool {
	root := filepath.Join(dataDir, "sandbox-browsers")
	_ = os.RemoveAll(root)
	return &UserSessionPool{root: root, executable: executable, sandbox: true, profiles: map[string]*userBrowserProfile{}, active: map[*UserBrowserSession]bool{}, slots: make(chan struct{}, 3), changed: make(chan struct{}, 1)}
}

// NewSandboxRequest 按「机器人 + 平台 + 会话」取沙箱：同一个群里谁让机器人点的都是
// 同一个页面，前一个人点到一半，后一个人能接着说「下一题选 B」。
func (p *UserSessionPool) NewSandboxRequest(profile, platform, conversation string) *UserBrowserSession {
	return p.NewRequest(profile, platform, conversation, nil)
}

func userBrowserScope(profile, platform, user string) string {
	scope, _ := json.Marshal([3]string{strings.TrimSpace(profile), strings.TrimSpace(platform), strings.TrimSpace(user)})
	sum := sha256.Sum256(scope)
	return hex.EncodeToString(sum[:])
}

func (p *UserSessionPool) NewRequest(profile, platform, user string, hosts []string) *UserBrowserSession {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := userBrowserScope(profile, platform, user)
	entry := p.profiles[key]
	if entry == nil {
		entry = &userBrowserProfile{gate: make(chan struct{}, 1)}
		p.profiles[key] = entry
	}
	entry.dataDir = filepath.Join(p.root, key)
	s := &UserBrowserSession{pool: p, entry: entry, gate: entry.gate, dataDir: entry.dataDir, hosts: append([]string(nil), hosts...), closed: make(chan struct{})}
	p.active[s] = true
	return s
}

func (p *UserSessionPool) Stop() {
	p.mu.Lock()
	sessions := make([]*UserBrowserSession, 0, len(p.active))
	for session := range p.active {
		sessions = append(sessions, session)
	}
	profiles := make([]*userBrowserProfile, 0, len(p.profiles))
	for _, profile := range p.profiles {
		profiles = append(profiles, profile)
	}
	p.mu.Unlock()
	for _, session := range sessions {
		_ = session.Close()
	}
	for _, profile := range profiles {
		profile.gate <- struct{}{}
		p.closeProfile(profile)
		<-profile.gate
	}
}

// Caller holds this profile's gate; no process can use it while it is stopped.
func (p *UserSessionPool) closeProfile(profile *userBrowserProfile) {
	p.mu.Lock()
	manager, proxy := profile.manager, profile.proxy
	profile.manager, profile.proxy, profile.tabID = nil, nil, ""
	if profile.idle != nil {
		profile.idle.Stop()
		profile.idle = nil
	}
	p.mu.Unlock()
	if manager != nil {
		manager.stopUserBrowserGracefully()
		<-p.slots
	}
	if proxy != nil {
		proxy.Close()
	}
	if p.sandbox && profile.dataDir != "" {
		_ = os.RemoveAll(profile.dataDir)
	}
	p.notifyChanged()
}

func (p *UserSessionPool) notifyChanged() {
	select {
	case p.changed <- struct{}{}:
	default:
	}
}

func (p *UserSessionPool) acquireSlot(ctx context.Context, closed <-chan struct{}) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-closed:
			return errors.New("个人浏览器会话已结束")
		default:
		}
		select {
		case p.slots <- struct{}{}:
			return nil
		default:
		}
		// Prefer evicting the oldest idle process to making another user wait five
		// minutes. A nonblocking gate acquisition pins the chosen profile.
		p.mu.Lock()
		var oldest *userBrowserProfile
		for _, profile := range p.profiles {
			if profile.manager != nil && len(profile.gate) == 0 && (oldest == nil || profile.lastUsed.Before(oldest.lastUsed)) {
				oldest = profile
			}
		}
		evicted := false
		if oldest != nil {
			select {
			case oldest.gate <- struct{}{}:
				evicted = true
			default:
			}
		}
		p.mu.Unlock()
		if evicted {
			p.closeProfile(oldest)
			<-oldest.gate
			continue
		}
		select {
		case p.slots <- struct{}{}:
			return nil
		case <-p.changed:
		case <-ctx.Done():
			return ctx.Err()
		case <-closed:
			return errors.New("个人浏览器会话已结束")
		}
	}
}

type UserBrowserSession struct {
	pool    *UserSessionPool
	entry   *userBrowserProfile
	gate    chan struct{}
	dataDir string
	hosts   []string
	mu      sync.Mutex
	manager *Bot
	leased  bool
	tabID   string
	closed  chan struct{}
	once    sync.Once
}

func (s *UserBrowserSession) Endpoint(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.closed:
		return "", errors.New("个人浏览器会话已结束")
	default:
	}
	if s.manager != nil {
		return s.manager.CDPURL(), nil
	}
	if !s.leased {
		select {
		case s.gate <- struct{}{}:
			s.leased = true
		case <-ctx.Done():
			return "", ctx.Err()
		case <-s.closed:
			return "", errors.New("个人浏览器会话已结束")
		}
	}
	s.pool.mu.Lock()
	if s.entry.idle != nil {
		s.entry.idle.Stop()
		s.entry.idle = nil
	}
	manager := s.entry.manager
	samePolicy := slices.Equal(s.entry.hosts, s.hosts)
	s.pool.mu.Unlock()
	if manager != nil && samePolicy && manager.CDPURL() != "" {
		s.manager = manager
		return manager.CDPURL(), nil
	}
	if manager != nil {
		s.pool.closeProfile(s.entry)
	}
	if err := s.pool.acquireSlot(ctx, s.closed); err != nil {
		s.releaseLease()
		return "", err
	}
	proxy, err := newUserBrowserProxy(s.hosts, s.pool.sandbox)
	if err != nil {
		<-s.pool.slots
		s.releaseLease()
		return "", err
	}
	managerOwner := New(context.Background(), nil, s.dataDir)
	manager = managerOwner.Bot("personal")
	managerOwner.settings.Enabled = true
	if s.pool.executable != nil {
		managerOwner.settings.Executable = s.pool.executable()
	}
	// Disable multiplexing across different origins through an allowed TLS tunnel.
	managerOwner.extraArgs = []string{"--proxy-server=" + proxy.url, "--proxy-bypass-list=<-loopback>", "--disable-quic", "--disable-http2", "--force-webrtc-ip-handling-policy=disable_non_proxied_udp"}
	if s.pool.sandbox {
		// 沙箱里没有登录态可偷，关掉站点隔离换来跨域 iframe 和主页面同进程：B 站小游戏
		// 这类页面整个内容都在别的域名的 iframe 里，同进程才能逐个 frame 编号元素、
		// 一次截图看全。
		managerOwner.extraArgs = append(managerOwner.extraArgs, "--disable-site-isolation-trials", "--disable-features=IsolateOrigins,site-per-process")
	}
	if err := manager.Start(ctx); err != nil {
		manager.Stop()
		proxy.Close()
		<-s.pool.slots
		s.releaseLease()
		return "", err
	}
	s.pool.mu.Lock()
	s.entry.manager, s.entry.proxy, s.entry.hosts = manager, proxy, append([]string(nil), s.hosts...)
	s.pool.mu.Unlock()
	s.manager = manager
	return manager.CDPURL(), nil
}

// PublicSandbox 告诉 agent 这是公网沙箱：任何公网网站都能开（代理那层仍拦内网），
// 不按个人浏览器的网站白名单检查。
func (s *UserBrowserSession) PublicSandbox() bool { return s.pool.sandbox }

func (s *UserBrowserSession) CurrentTab() string {
	s.mu.Lock()
	tab := s.tabID
	s.mu.Unlock()
	if tab == "" && s.pool.sandbox {
		s.pool.mu.Lock()
		tab = s.entry.tabID
		s.pool.mu.Unlock()
	}
	return tab
}

func (s *UserBrowserSession) SetCurrentTab(id string) {
	s.mu.Lock()
	s.tabID = id
	s.mu.Unlock()
	if s.pool.sandbox {
		s.pool.mu.Lock()
		s.entry.tabID = id
		s.pool.mu.Unlock()
	}
}

// Caller holds s.mu and the profile gate if leased.
func (s *UserBrowserSession) releaseLease() {
	if !s.leased {
		return
	}
	s.pool.mu.Lock()
	s.entry.lastUsed = time.Now()
	if s.entry.manager != nil {
		var timer *time.Timer
		timer = time.AfterFunc(personalBrowserIdleTimeout, func() {
			select {
			case s.gate <- struct{}{}:
				s.pool.mu.Lock()
				current := s.entry.idle == timer
				s.pool.mu.Unlock()
				if current {
					s.pool.closeProfile(s.entry)
				}
				<-s.gate
			default:
			}
		})
		s.entry.idle = timer
	}
	s.pool.mu.Unlock()
	<-s.gate
	s.leased = false
	s.pool.notifyChanged()
}

func (s *UserBrowserSession) Close() error {
	s.once.Do(func() {
		close(s.closed)
		s.mu.Lock()
		s.releaseLease()
		s.mu.Unlock()
		s.pool.mu.Lock()
		delete(s.pool.active, s)
		s.pool.mu.Unlock()
	})
	return nil
}

func (b *Bot) stopUserBrowserGracefully() {
	m := b.m
	m.mu.Lock()
	inst := m.bots[b.id]
	if inst == nil {
		m.mu.Unlock()
		return
	}
	inst.stopping = true
	m.settings.Enabled = false
	endpoint := inst.cdpURL
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if target, err := browserWebSocket(ctx, endpoint); err == nil {
		if session, err := Dial(ctx, target); err == nil {
			_ = session.Call(ctx, "Browser.close", nil, nil)
			session.Close()
		}
	}
	for ctx.Err() == nil {
		m.mu.RLock()
		done := inst.cmd == nil
		m.mu.RUnlock()
		if done {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	b.Stop()
}

func browserWebSocket(ctx context.Context, endpoint string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/json/version", nil)
	if err != nil {
		return "", err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("CDP version HTTP %d", response.StatusCode)
	}
	var version struct {
		WebSocket string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(response.Body).Decode(&version); err != nil {
		return "", err
	}
	if version.WebSocket == "" {
		return "", errors.New("browser websocket is unavailable")
	}
	return version.WebSocket, nil
}
