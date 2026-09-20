// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 连接位被一个不再收发的连接占住时，接入端每次重连都撞 409。这条路径以前一行日志
// 都不写：线上 QQ 整段时间收不到消息，日志里却什么都没有，只能手动发一次握手才看
// 得出来。409 必须和鉴权失败一样留下记录，并写明被谁占着。
func TestReverseServerLogsDuplicateClientConflict(t *testing.T) {
	const token = "0123456789abcdef"
	server := NewOneBotReverseServer(OneBotConfig{Endpoint: "ws://127.0.0.1:18080/onebot/v11/ws", AccessToken: token})

	// 挂上事件处理器，模拟「机器人正在跑」。没有它握手会先撞上「机器人已停用」
	// 那条 503，测不到冲突这一档。
	server.mu.Lock()
	server.handler = func(context.Context, MessageEvent) error { return nil }
	server.mu.Unlock()

	// 手动占住连接位，模拟「已有客户端连着」。
	server.connMu.Lock()
	server.accepting = true
	server.status.ConnectionOwner = "client-holder"
	server.connMu.Unlock()

	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)

	request := httptest.NewRequest("GET", "http://localhost/onebot/v11/ws", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusConflict)
	}
	line := logs.String()
	if !strings.Contains(line, "duplicate_client_conflict") || !strings.Contains(line, "holder=client-holder") {
		t.Fatalf("冲突没有被记录或缺少占用者信息：%q", line)
	}
	if status := server.Status(); status.DuplicateConnections != 1 || status.LastConnectionEvent != "duplicate_client_conflict" {
		t.Fatalf("status = %+v", status)
	}

	// 同一个客户端每几秒重连一次，不能刷屏：一分钟内只记一条。
	logs.Reset()
	retry := httptest.NewRequest("GET", "http://localhost/onebot/v11/ws", nil)
	retry.Header.Set("Authorization", "Bearer "+token)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, retry)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("重试 status = %d, want %d", recorder.Code, http.StatusConflict)
	}
	if strings.Contains(logs.String(), "duplicate_client_conflict") {
		t.Fatalf("同一客户端的重复冲突被重复记录：%q", logs.String())
	}
	if server.Status().DuplicateConnections != 2 {
		t.Fatalf("重试没有计入 DuplicateConnections: %+v", server.Status())
	}

	// 换一个客户端撞上来是新情况，必须记下来——否则看不出是谁在抢连接位。
	other := httptest.NewRequest("GET", "http://localhost/onebot/v11/ws", nil)
	other.Header.Set("Authorization", "Bearer "+token)
	other.Header.Set("X-Self-ID", "90001")
	other.RemoteAddr = "10.1.2.3:54321"
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, other)
	if !strings.Contains(logs.String(), "duplicate_client_conflict") {
		t.Fatalf("不同客户端的冲突没有被记录：%q", logs.String())
	}
}
