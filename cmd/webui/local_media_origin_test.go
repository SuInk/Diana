// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gorilla/websocket"
)

// 正向 ws 接入没有入站握手，媒体回源地址要按配置档的 ws 地址回推主机，
// 并补本服务自己的 web 端口（接入端的 ws 端口上并没有媒体服务）。
// 反向 ws 一旦有活跃握手，握手 Host 优先——那是桥真实在用的地址。
func TestLocalMediaOriginProviderPrefersReverseHandshake(t *testing.T) {
	reverse := assistant.NewOneBotReverseServer(assistant.OneBotConfig{AccessToken: "token", Endpoint: "/onebot/v11/ws"})
	server := httptest.NewServer(reverse)
	defer server.Close()

	tracker := &forwardWSOriginTracker{}
	tracker.reset([]*assistant.OneBotChannel{
		assistant.NewOneBotChannel(assistant.OneBotConfig{Endpoint: "ws://host.docker.internal:3001"}),
	})
	httpChannel := assistant.NewOneBotHTTPChannel(assistant.OneBotConfig{Endpoint: "http://127.0.0.1:5700"})
	provider := localMediaOriginProvider(reverse, tracker, httpChannel, "18080")

	// 无反向连接时，正向 ws 配置地址是推断来源。
	if got := provider(); got != "http://host.docker.internal:18080" {
		t.Fatalf("origin = %q", got)
	}

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Authorization": []string{"Bearer token"},
		"X-Self-ID":     []string{"42"},
	})
	if err != nil {
		t.Fatalf("dial error = %v", err)
	}
	defer conn.Close()
	waitUntil := time.Now().Add(2 * time.Second)
	for reverse.ConnectionOrigin() == "" && time.Now().Before(waitUntil) {
		time.Sleep(10 * time.Millisecond)
	}
	// 握手地址是 httptest 随机端口，整体优先于正向 ws 推导结果。
	if got := provider(); got != server.URL {
		t.Fatalf("origin = %q, want reverse handshake %q", got, server.URL)
	}
}

func TestLocalMediaOriginProviderFallsBackAcrossTransports(t *testing.T) {
	reverse := assistant.NewOneBotReverseServer(assistant.OneBotConfig{})
	tracker := &forwardWSOriginTracker{}

	// 只有 HTTP 接入：按 HTTP API 地址回推。
	httpChannel := assistant.NewOneBotHTTPChannel(assistant.OneBotConfig{Endpoint: "http://127.0.0.1:5700"})
	provider := localMediaOriginProvider(reverse, tracker, httpChannel, "18080")
	if got := provider(); got != "http://127.0.0.1:18080" {
		t.Fatalf("http origin = %q", got)
	}

	// 正向 ws 优先于 HTTP。
	tracker.reset([]*assistant.OneBotChannel{
		assistant.NewOneBotChannel(assistant.OneBotConfig{Endpoint: "ws://192.168.1.20:6700"}),
	})
	if got := provider(); got != "http://192.168.1.20:18080" {
		t.Fatalf("forward ws origin = %q", got)
	}

	// 配置档全部停用时 tracker 被清空，退回 HTTP；HTTP 也没有则返回空串，
	// LocalMediaStore 用它自己的静态基址。
	tracker.reset(nil)
	httpChannel.SetConfig(assistant.OneBotConfig{})
	if got := provider(); got != "" {
		t.Fatalf("empty origin = %q", got)
	}
}
