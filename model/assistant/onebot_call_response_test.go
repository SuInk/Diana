// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"sync"
	"testing"
	"time"
)

// API 调用超时与响应到达可能同时发生，部分实现还会重复发送同一 echo。结果通道
// 已满时 resolveCall 绝不能卡住唯一的 WebSocket 读循环，否则连接表面在线，所有
// 后续消息却都会断流。
func TestOneBotResolveCallDoesNotBlockOnAbandonedResponse(t *testing.T) {
	tests := []struct {
		name  string
		setup func() (*sync.Map, func(oneBotEnvelope))
	}{
		{
			name: "forward",
			setup: func() (*sync.Map, func(oneBotEnvelope)) {
				channel := NewOneBotChannel(OneBotConfig{})
				return &channel.pending, channel.resolveCall
			},
		},
		{
			name: "reverse",
			setup: func() (*sync.Map, func(oneBotEnvelope)) {
				server := NewOneBotReverseServer(OneBotConfig{})
				return &server.pending, server.resolveCall
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pending, resolve := test.setup()
			const echo = "duplicate-echo"
			resultCh := make(chan callResult, 1)
			pending.Store(echo, resultCh)
			envelope := oneBotEnvelope{Echo: echo, Status: "ok", Data: map[string]any{"ok": true}}

			// 第一帧正常投递，但调用方还没来得及从 resultCh 取走。
			resolve(envelope)
			if len(resultCh) != 1 {
				t.Fatalf("first response delivery count = %d, want 1", len(resultCh))
			}

			done := make(chan struct{})
			go func() {
				resolve(envelope)
				close(done)
			}()

			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("resolveCall blocked the WebSocket read loop on a full result channel")
			}
			if _, ok := pending.Load(echo); ok {
				t.Fatal("resolved echo remained pending")
			}
		})
	}
}
