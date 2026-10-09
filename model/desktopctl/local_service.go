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
	connection *Connection
	lastError  string
}

func NewLocalService(hub *Hub, path string) *LocalService { return &LocalService{hub: hub, path: path} }
func (s *LocalService) Run(ctx context.Context) {
	defer func() { safego.Report("desktopctl.local-service", recover()) }()
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
func (s *LocalService) Status() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path != "" || s.adapter != nil, s.lastError
}
func (s *LocalService) Sync(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hub.Policy().Enabled {
		if s.connection != nil {
			s.connection.Close()
			s.connection = nil
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
		if s.path == "" {
			s.lastError = "未配置本机桌面 helper"
			return
		}
		if runtime.GOOS != "darwin" {
			s.lastError = "本机桌面 helper 当前仅支持 macOS"
			return
		}
		a, err := NewProcessAdapter(s.path)
		if err != nil {
			s.lastError = err.Error()
			return
		}
		s.adapter = a
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

func (s *LocalService) Process() *ProcessAdapter {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, _ := s.adapter.(*ProcessAdapter)
	return p
}
