// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package localembed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func tinyManager(t *testing.T, dir string, sources ...string) *Manager {
	t.Helper()
	weights, err := os.ReadFile("testdata/tiny/model.safetensors")
	if err != nil {
		t.Fatal(err)
	}
	config, _ := os.ReadFile("testdata/tiny/config.json")
	vocab, _ := os.ReadFile("testdata/tiny/vocab.txt")
	sum := sha256.Sum256(weights)
	m := NewManager(dir, nil)
	m.sources = sources
	m.sha256 = hex.EncodeToString(sum[:])
	m.size = int64(len(weights))
	m.config = config
	m.vocab = vocab
	return m
}

func waitState(t *testing.T, m *Manager, want State) Status {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status := m.Status()
		if status.State == want {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("state = %+v, want %s", m.Status(), want)
	return Status{}
}

func TestManagerDownloadsFallsBackAndLoadsFromDisk(t *testing.T) {
	weights, _ := os.ReadFile("testdata/tiny/model.safetensors")
	var badHits, goodHits atomic.Int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		badHits.Add(1)
		_, _ = w.Write(make([]byte, len(weights))) // 长度对、内容不对：只有校验和能拦住
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		goodHits.Add(1)
		_, _ = w.Write(weights)
	}))

	dir := t.TempDir()
	m := tinyManager(t, dir, bad.URL, good.URL)
	if _, err := m.Embed(context.Background(), []string{"今天"}); err == nil {
		t.Fatal("embed before ready should fail instead of blocking")
	}
	if m.Ready() {
		t.Fatal("first Ready should start preparing, not report ready")
	}
	waitState(t, m, StateReady)
	if !m.Ready() {
		t.Fatal("Ready after load")
	}
	vectors, err := m.Embed(context.Background(), []string{"今天吃什么"})
	if err != nil || len(vectors) != 1 || len(vectors[0]) != 16 {
		t.Fatalf("embed = %v, %v", vectors, err)
	}
	if badHits.Load() != 1 || goodHits.Load() != 1 {
		t.Fatalf("hits bad=%d good=%d", badHits.Load(), goodHits.Load())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != weightsFileName {
		t.Fatalf("model dir should hold only the verified weights, got %v", entries)
	}

	// 已经下载过：不再联网。
	good.Close()
	again := tinyManager(t, dir, good.URL)
	again.Ready()
	waitState(t, again, StateReady)
}

func TestManagerReplacesCorruptedFile(t *testing.T) {
	weights, _ := os.ReadFile("testdata/tiny/model.safetensors")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(weights)
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, weightsFileName), []byte("truncated"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := tinyManager(t, dir, server.URL)
	m.Ready()
	waitState(t, m, StateReady)
}

func TestManagerFailureWaitsBeforeRetry(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer server.Close()
	m := tinyManager(t, t.TempDir(), server.URL)
	m.Ready()
	status := waitState(t, m, StateFailed)
	if status.Error == "" {
		t.Fatal("failed status should carry the reason")
	}
	// 每条消息都会问一次 Ready，失败后不能每次都重新下载。
	for range 5 {
		if m.Ready() {
			t.Fatal("failed manager reported ready")
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("hits = %d, want 1", got)
	}
	m.mu.Lock()
	m.failedAt = time.Now().Add(-retryAfterFailure - time.Second)
	m.mu.Unlock()
	m.Ready()
	waitState(t, m, StateFailed)
	if got := hits.Load(); got != 2 {
		t.Fatalf("hits after retry window = %d, want 2", got)
	}
}

func TestManagerRejectsOversizedResponse(t *testing.T) {
	weights, _ := os.ReadFile("testdata/tiny/model.safetensors")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(append(weights, make([]byte, 1<<20)...))
	}))
	defer server.Close()
	m := tinyManager(t, t.TempDir(), server.URL)
	m.Ready()
	waitState(t, m, StateFailed)
}
