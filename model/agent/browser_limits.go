// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// 浏览器的资源治理，两条线各管各的：
//
//   - 一次性浏览器（browser_render、HTML 截图）每次起一整个 Chrome 进程，群成员也能
//     触发，所以限的是同时在跑的进程数，超了排一小会儿队，排不上就明确拒绝。
//   - 交互式浏览器（主人的 browser_* 那组）连的是一个常驻浏览器，限的是机器人自己
//     开出来、还没关的标签页数，并且按对话分配标签页，不同对话不抢同一页。

const (
	defaultDisposableBrowserLimit = 3
	maxDisposableBrowserLimit     = 16
	// disposableBrowserQueueWait 是一次性浏览器排队的上限。排不上就当场说「忙」，
	// 不把请求挂到工具总超时才失败。
	disposableBrowserQueueWait = 20 * time.Second

	// maxAgentBrowserTabs 是机器人在同一个浏览器里自己开出来、还开着的标签页上限。
	// 只数机器人开的：主人在实时画面或自己的 Chrome 里开的页不占名额，也不会被回收。
	maxAgentBrowserTabs = 8
	// browserTabLeaseIdle 内用过某个标签页的对话算「占着」它：别的对话自动挑页时
	// 绕开，到上限回收时也不关。
	browserTabLeaseIdle = 10 * time.Minute
	// browserSessionIdle 之后对话的「当前标签页」记录被清掉，下次从头挑。
	browserSessionIdle = time.Hour
	// browserAbortTimeout 是超时或取消之后补发 Page.stopLoading、关标签页的时间，
	// 独立于调用方已经失效的 ctx。
	browserAbortTimeout = 3 * time.Second
)

// browserTimeoutError 是浏览器自己的超时：给模型看的是一句能照着改做法的话，
// errors.Is 仍认得出是 context.DeadlineExceeded（插件据此显示「页面渲染超时」）。
type browserTimeoutError struct{ message string }

func (e *browserTimeoutError) Error() string { return e.message }
func (e *browserTimeoutError) Unwrap() error { return context.DeadlineExceeded }

// ---- 一次性浏览器的并发上限 ----

type browserSlots struct {
	slots chan struct{}
	wait  time.Duration
}

func newBrowserSlots(limit int, wait time.Duration) *browserSlots {
	if limit <= 0 {
		limit = defaultDisposableBrowserLimit
	}
	return &browserSlots{slots: make(chan struct{}, limit), wait: wait}
}

// ErrBrowserBusy 表示一次性浏览器的并发名额满了、排队也没等到。
var ErrBrowserBusy = errors.New("browser busy")

func (s *browserSlots) acquire(ctx context.Context) (func(), error) {
	release := func() func() {
		var once sync.Once
		return func() { once.Do(func() { <-s.slots }) }
	}
	select {
	case s.slots <- struct{}{}:
		return release(), nil
	default:
	}
	timer := time.NewTimer(s.wait)
	defer timer.Stop()
	select {
	case s.slots <- struct{}{}:
		return release(), nil
	case <-timer.C:
		return nil, fmt.Errorf("%w：同时运行的一次性浏览器已达上限（%d 个），排队 %s 仍没轮到，稍后再试", ErrBrowserBusy, cap(s.slots), s.wait)
	case <-ctx.Done():
		return nil, fmt.Errorf("等待一次性浏览器名额时任务结束：%w", ctx.Err())
	}
}

var disposableBrowsers = newBrowserSlots(disposableBrowserLimitFromEnv(), disposableBrowserQueueWait)

func disposableBrowserLimitFromEnv() int {
	limit := intFromEnv("DIANA_HEADLESS_BROWSER_MAX_CONCURRENT", defaultDisposableBrowserLimit)
	return min(limit, maxDisposableBrowserLimit)
}

// ---- 交互式浏览器：按对话分标签页 ----

// browserTabRegistry 记着每个对话正在用哪个标签页，以及机器人自己开过哪些标签页。
// 进程级共享：同一台机器人的不同对话各建各的工具表，但连的是同一个浏览器。
type browserTabRegistry struct {
	mu       sync.Mutex
	sessions map[string]*browserSession
	opened   map[string]*openedBrowserTab
	// openMu 把「数名额 → 回收 → 新开」串成一步，并发的两次新开不会一起越过上限。
	openMu sync.Mutex
	anon   atomic.Int64
	now    func() time.Time
}

type openedBrowserTab struct {
	baseURL  string
	lastUsed time.Time
}

func newBrowserTabRegistry() *browserTabRegistry {
	return &browserTabRegistry{
		sessions: map[string]*browserSession{},
		opened:   map[string]*openedBrowserTab{},
		now:      time.Now,
	}
}

var browserTabs = newBrowserTabRegistry()

// session 取一个对话的标签页记录。key 为空（没有对话可认的调用方）时每次给一份
// 新的，只在这一张工具表里有效。
func (r *browserTabRegistry) session(key string) *browserSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for k, s := range r.sessions {
		if now.Sub(s.lastUsedAt()) > browserSessionIdle {
			delete(r.sessions, k)
		}
	}
	if key == "" {
		key = "anon:" + strconv.FormatInt(r.anon.Add(1), 10)
	} else if s := r.sessions[key]; s != nil {
		return s
	}
	s := &browserSession{key: key, lastUsed: now, now: r.now}
	r.sessions[key] = s
	return s
}

// heldByOther 判断这个标签页是不是正被别的对话用着。
func (r *browserTabRegistry) heldByOther(targetID string, self *browserSession) bool {
	if r == nil || targetID == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for _, s := range r.sessions {
		if s == self {
			continue
		}
		id, lastUsed := s.snapshot()
		if id == targetID && now.Sub(lastUsed) < browserTabLeaseIdle {
			return true
		}
	}
	return false
}

func (r *browserTabRegistry) markOpened(baseURL, targetID string) {
	r.mu.Lock()
	r.opened[targetID] = &openedBrowserTab{baseURL: baseURL, lastUsed: r.now()}
	r.mu.Unlock()
}

func (r *browserTabRegistry) touch(targetID string) {
	r.mu.Lock()
	if tab := r.opened[targetID]; tab != nil {
		tab.lastUsed = r.now()
	}
	r.mu.Unlock()
}

// isOpened 判断这个标签页是不是机器人自己开的。
func (r *browserTabRegistry) isOpened(targetID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.opened[targetID] != nil
}

func (r *browserTabRegistry) forget(targetID string) {
	r.mu.Lock()
	delete(r.opened, targetID)
	r.mu.Unlock()
}

// openedOn 返回机器人在这个浏览器里开过、现在还开着的标签页，最久没用的在前。
// 已经不在的（被人关了、浏览器重启了）顺手从记录里去掉。
func (r *browserTabRegistry) openedOn(baseURL string, targets []browserTarget) []string {
	alive := make(map[string]bool, len(targets))
	for _, target := range targets {
		if target.Type == "page" {
			alive[target.ID] = true
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.opened))
	for id, tab := range r.opened {
		if tab.baseURL != baseURL {
			continue
		}
		if !alive[id] {
			delete(r.opened, id)
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return r.opened[ids[i]].lastUsed.Before(r.opened[ids[j]].lastUsed) })
	return ids
}

// openTab 在名额内新开一个空白标签页，名额满了先回收机器人自己开的闲置页。
func (b browserToolBase) openTab(ctx context.Context, baseURL string) (browserTarget, error) {
	registry := b.tabRegistry()
	registry.openMu.Lock()
	defer registry.openMu.Unlock()
	targets, err := listBrowserTargets(ctx, baseURL)
	if err != nil {
		return browserTarget{}, err
	}
	opened := registry.openedOn(baseURL, targets)
	own := b.session.active()
	count := len(opened)
	for _, id := range opened {
		if count < maxAgentBrowserTabs {
			break
		}
		if id == own || registry.heldByOther(id, b.session) {
			continue
		}
		if err := browserTargetCommand(ctx, baseURL, "close", id); err != nil {
			continue
		}
		registry.forget(id)
		count--
	}
	if count >= maxAgentBrowserTabs {
		return browserTarget{}, fmt.Errorf("机器人开的标签页已达上限（%d 个），没有可以自动回收的闲置页，不能再新开。用 browser_open 不带 new_tab 沿用当前标签页，或先用 browser_tabs 的 list 查看、close 关掉不用的", maxAgentBrowserTabs)
	}
	target, err := newBrowserTarget(ctx, baseURL, "about:blank")
	if err != nil {
		return browserTarget{}, err
	}
	registry.markOpened(baseURL, target.ID)
	return target, nil
}

func (b browserToolBase) tabRegistry() *browserTabRegistry {
	if b.tabs != nil {
		return b.tabs
	}
	return browserTabs
}

// discardTab 关掉一个刚开出来却没能加载成功的标签页，不给后台留一个卡在半路的页。
func (b browserToolBase) discardTab(baseURL, targetID string) {
	if targetID == "" {
		return
	}
	closeBrowserTarget(baseURL, targetID)
	b.tabRegistry().forget(targetID)
	if b.session.active() == targetID {
		b.session.setActive("")
	}
}
