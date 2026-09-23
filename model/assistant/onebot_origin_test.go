// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

// OneBotEndpointOrigin 是媒体回源地址推断的基石：正向 ws / HTTP 接入没有入站
// 握手，只能拿配置里的接入端地址回推桥端回源本服务用的主机。
func TestOneBotEndpointOrigin(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		want     string
	}{
		{"ws LAN endpoint", "ws://192.168.1.20:6700", "http://192.168.1.20"},
		{"ws localhost", "ws://127.0.0.1:6700/", "http://127.0.0.1"},
		{"ws docker host", "ws://host.docker.internal:3001", "http://host.docker.internal"},
		{"wss", "wss://onebot.example.com:6701/ws", "https://onebot.example.com"},
		{"http passthrough", "http://127.0.0.1:5700", "http://127.0.0.1"},
		{"https passthrough", "https://onebot.example.com:5700", "https://onebot.example.com"},
		{"upper scheme", "WS://127.0.0.1:6700", "http://127.0.0.1"},
		{"ipv6", "ws://[::1]:6700", "http://::1"},
		{"empty", "", ""},
		{"garbage", "not a url", ""},
		{"unsupported scheme", "ftp://127.0.0.1:6700", ""},
		{"missing host", "ws:///path", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := OneBotEndpointOrigin(tc.endpoint); got != tc.want {
				t.Fatalf("OneBotEndpointOrigin(%q) = %q, want %q", tc.endpoint, got, tc.want)
			}
		})
	}
}

func TestForwardWSChannelConnectionOriginDerivesFromEndpoint(t *testing.T) {
	channel := NewOneBotChannel(OneBotConfig{Endpoint: "ws://host.docker.internal:3001"})
	if got := channel.ConnectionOrigin(); got != "http://host.docker.internal" {
		t.Fatalf("ConnectionOrigin() = %q", got)
	}
	// 配置档重建渠道后（含清空 endpoint），推导结果跟随新配置。
	rebuilt := NewOneBotChannel(OneBotConfig{Endpoint: ""})
	if got := rebuilt.ConnectionOrigin(); got != "" {
		t.Fatalf("empty endpoint origin = %q", got)
	}
}

func TestHTTPChannelConnectionOriginDerivesFromEndpoint(t *testing.T) {
	channel := NewOneBotHTTPChannel(OneBotConfig{Endpoint: "http://127.0.0.1:5700"})
	if got := channel.ConnectionOrigin(); got != "http://127.0.0.1" {
		t.Fatalf("ConnectionOrigin() = %q", got)
	}
	// 没绑配置档时 SetConfig 清空 endpoint，推导应为空（退回静态基址）。
	channel.SetConfig(OneBotConfig{})
	if got := channel.ConnectionOrigin(); got != "" {
		t.Fatalf("cleared endpoint origin = %q", got)
	}
}
