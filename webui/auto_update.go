package webui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/updater"
)

// UpdateSettingsStore 持久化系统自动更新设置。
type UpdateSettingsStore interface {
	LoadUpdateSettings(ctx context.Context) (updater.Settings, bool, error)
	SaveUpdateSettings(ctx context.Context, settings updater.Settings) error
}

type LatestReleaseUpdater interface {
	UpdateLatest(context.Context, bool) (updater.Result, error)
}

// AutoUpdater 周期性安装最新稳定 Release。
type AutoUpdater struct {
	updater  LatestReleaseUpdater
	store    UpdateSettingsStore
	logs     AppLogWriter
	interval time.Duration // 循环唤醒粒度，测试可调小

	mu         sync.Mutex
	settings   updater.Settings
	lastRunAt  time.Time
	lastResult string
	lastError  string
}

// NewAutoUpdater 创建自动更新循环，settings 从存储加载，新安装默认每 30 分钟自动更新。
func NewAutoUpdater(releaseUpdater LatestReleaseUpdater, store UpdateSettingsStore, logs AppLogWriter) *AutoUpdater {
	a := &AutoUpdater{
		updater:  releaseUpdater,
		store:    store,
		logs:     logs,
		interval: time.Minute,
	}
	if store != nil {
		if settings, ok, err := store.LoadUpdateSettings(context.Background()); err == nil && ok {
			a.settings = settings.WithDefaults()
		} else {
			a.settings = updater.DefaultSettings()
		}
	} else {
		a.settings = updater.DefaultSettings()
	}
	return a
}

// Settings 返回当前生效的自动更新设置。
func (a *AutoUpdater) Settings() updater.Settings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.settings
}

// SaveSettings 校验并持久化自动更新设置。
func (a *AutoUpdater) SaveSettings(ctx context.Context, settings updater.Settings) (updater.Settings, error) {
	settings = settings.WithDefaults()
	if a.store != nil {
		if err := a.store.SaveUpdateSettings(ctx, settings); err != nil {
			return updater.Settings{}, err
		}
	}
	a.mu.Lock()
	a.settings = settings
	a.mu.Unlock()
	return settings, nil
}

// LastRun 返回最近一次自动更新的时间与结果摘要。
func (a *AutoUpdater) LastRun() (time.Time, string, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastRunAt, a.lastResult, a.lastError
}

// Run 阻塞运行自动更新循环，直到 ctx 结束；应在独立 goroutine 中调用。
func (a *AutoUpdater) Run(ctx context.Context) {
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.tick(ctx)
		}
	}
}

// tick 判断是否到达自动更新周期并执行一次更新。
func (a *AutoUpdater) tick(ctx context.Context) {
	a.mu.Lock()
	settings := a.settings
	last := a.lastRunAt
	a.mu.Unlock()
	if !settings.AutoUpdateEnabled {
		return
	}
	if !last.IsZero() && time.Since(last) < time.Duration(settings.IntervalMinutes)*time.Minute {
		return
	}
	a.runOnce(ctx)
}

// runOnce 执行一次自动更新并记录结果。
func (a *AutoUpdater) runOnce(ctx context.Context) {
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	result, err := a.updater.UpdateLatest(runCtx, false)
	cancel()
	managedExternally := errors.Is(err, updater.ErrRemoteNotConfigured) || errors.Is(err, updater.ErrReleaseUpdateUnsupported)
	if managedExternally {
		// Unsupported package layouts and containers are replaced by their deployment manager.
		// 这不是运行故障，不应周期性写错误日志或向用户弹出英文 Git 错误。
		err = nil
	}

	a.mu.Lock()
	a.lastRunAt = time.Now()
	if managedExternally {
		a.lastResult = "由部署环境管理更新"
		a.lastError = ""
	} else if err != nil {
		a.lastResult = ""
		a.lastError = err.Error()
	} else {
		a.lastError = ""
		if result.Updated {
			target := result.TargetCommit
			if target == "" {
				target = result.Status.HeadCommit
			}
			a.lastResult = "已更新到 " + target
		} else {
			a.lastResult = "已是最新"
		}
	}
	a.mu.Unlock()

	if a.logs == nil {
		return
	}
	// 自动更新只在真正拉到新提交或失败时写日志，"已是最新"不刷屏。
	switch {
	case err != nil:
		recordError(context.Background(), a.logs, "system.update.auto", err, "", nil)
	case managedExternally:
		return
	case result.Updated:
		target := result.TargetCommit
		if target == "" {
			target = result.Status.HeadCommit
		}
		summary := strings.TrimSpace(target + " " + result.Status.HeadSubject)
		recordOperation(context.Background(), a.logs, "system.update.auto", "自动更新完成: "+summary, "", map[string]any{
			"branch":  result.Status.Branch,
			"updated": true,
		})
	}
}
