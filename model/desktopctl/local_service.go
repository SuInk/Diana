// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.
package desktopctl

import (
	"context"
	"github.com/SuInk/diana/internal/safego"
	"runtime"
	"sync"
	"time"
)

// LocalService owns the single local helper connection. No network listener or
// external command path is exposed to models or HTTP clients.
type LocalService struct {
	mu         sync.Mutex
	hub        *Hub
	adapter    Adapter
	path       string
	cuaPath    string
	installer  *CUADriverInstaller
	base       context.Context
	executor   string
	connection *Connection
	lastError  string
}

func NewLocalService(hub *Hub, path string) *LocalService { return &LocalService{hub: hub, path: path} }

// UseCUADriver prefers trycua's cua-driver over the native helper. path is an
// operator override (DIANA_CUA_DRIVER); installer fetches the pinned build on
// demand when desktop control is first enabled. Either may be empty/nil.
func (s *LocalService) UseCUADriver(path string, installer *CUADriverInstaller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cuaPath, s.installer = path, installer
}

func (s *LocalService) Run(ctx context.Context) {
	defer func() { safego.Report("desktopctl.local-service", recover()) }()
	s.mu.Lock()
	s.base = ctx
	s.mu.Unlock()
	defer s.closeAdapter()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	defer s.Close()
	for {
		s.Sync(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *LocalService) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.connection != nil {
		s.connection.Close()
		s.connection = nil
	}
}
func (s *LocalService) closeAdapter() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.adapter.(*ProcessAdapter); ok {
		p.Close()
	}
}

// Status reports whether any executor is available (configured, installed or
// downloadable) plus the last problem for the console.
func (s *LocalService) Status() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path != "" || s.cuaPath != "" || s.installer != nil || s.adapter != nil, s.lastError
}

// Executor names the backend in use, e.g. "cua-driver 0.38.0".
func (s *LocalService) Executor() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.executor
}
func (s *LocalService) Sync(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hub.Policy().Enabled {
		if s.connection != nil {
			s.connection.Close()
			s.connection = nil
		}
		// Turning desktop control off also stops the executor process, so no
		// child holding Screen Recording / Accessibility stays alive.
		if p, ok := s.adapter.(*ProcessAdapter); ok {
			p.Close()
			s.adapter, s.executor = nil, ""
		}
		return
	}
	if s.connection != nil {
		if _, ok := s.hub.Connection(s.connection.id); ok {
			return
		}
		s.connection = nil
	}
	if s.adapter == nil {
		if runtime.GOOS != "darwin" {
			s.lastError = "本机桌面执行器当前仅支持 macOS"
			return
		}
		a, executor, pending := s.newAdapter(ctx)
		if a == nil {
			s.lastError = pending
			return
		}
		s.adapter, s.executor = a, executor
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c, _, err := s.hub.AttachLocal(ctx, s.adapter, Hello{HelperID: "local-macos", HelperName: "本机桌面", Platform: runtime.GOOS}, TokenInfo{})
	if err != nil {
		s.lastError = err.Error()
		return
	}
	s.connection = c
	s.lastError = ""
}

// newAdapter picks the executor. cua-driver wins when it is available; while
// the pinned build is still downloading Diana waits instead of starting the
// native helper, so the backend does not switch under a running task. Only a
// failed download falls back to the helper.
func (s *LocalService) newAdapter(ctx context.Context) (*ProcessAdapter, string, string) {
	cuaPath, label := s.cuaPath, "cua-driver（DIANA_CUA_DRIVER 指定）"
	if cuaPath == "" {
		cuaPath, label = s.installer.Installed(), "cua-driver "+CUADriverVersion
	}
	if cuaPath != "" {
		a, err := NewCUADriverAdapter(cuaPath)
		if err != nil {
			return nil, "", err.Error()
		}
		return a, label, ""
	}
	pending := ""
	if s.installer != nil {
		base := s.base
		if base == nil {
			base = context.WithoutCancel(ctx)
		}
		s.installer.Start(base, nil)
		installing, err := s.installer.Status()
		switch {
		case installing:
			return nil, "", "正在下载桌面执行器 cua-driver " + CUADriverVersion + "…"
		case err == nil:
			// A download that finished between Start and Status is picked up next tick.
			return nil, "", "正在准备桌面执行器 cua-driver " + CUADriverVersion + "…"
		}
		pending = "cua-driver 下载失败：" + err.Error()
	}
	if s.path == "" {
		if pending == "" {
			pending = "未配置本机桌面执行器"
		}
		return nil, "", pending
	}
	a, err := NewProcessAdapter(s.path)
	if err != nil {
		return nil, "", err.Error()
	}
	return a, "desktopctl-helper", ""
}

func (s *LocalService) Process() *ProcessAdapter {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, _ := s.adapter.(*ProcessAdapter)
	return p
}
