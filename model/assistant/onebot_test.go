// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestCQToSegmentsAndPlainText 验证对应功能场景。
func TestCQToSegmentsAndPlainText(t *testing.T) {
	got := CQToSegments("hi [CQ:at,qq=123] 看图 [CQ:image,file=a.jpg]")
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4: %#v", len(got), got)
	}
	if got[1].Type != "at" || got[1].Data["qq"] != "123" {
		t.Fatalf("at segment = %#v", got[1])
	}
	if text := PlainText(got); text != "hi @123  看图" {
		t.Fatalf("PlainText = %q", text)
	}
}

func TestPlainTextIncludesForwardSummary(t *testing.T) {
	got := PlainText(CQToSegments("[CQ:forward,id=abc123]"))
	if got != "[合并转发:abc123]" {
		t.Fatalf("PlainText = %q", got)
	}
}

// TestImageURLsExtractsRemoteAndBase64Images 验证图片段能提取远端或 data URL。
func TestImageURLsExtractsRemoteAndBase64Images(t *testing.T) {
	got := ImageURLs([]MessageSegment{
		{Type: "image", Data: map[string]string{"url": "https://example.com/a.jpg"}},
		{Type: "image", Data: map[string]string{"file": "base64://abcd"}},
		{Type: "image", Data: map[string]string{"file": "local-cache.jpg"}},
	})
	want := []string{"https://example.com/a.jpg", "data:image/jpeg;base64,abcd"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestVideoURLsExtractsRemoteAndLocalVideos 验证视频段能提取远端 URL、本地路径并去重。
func TestVideoURLsExtractsRemoteAndLocalVideos(t *testing.T) {
	local := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(local, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	local, err := filepath.EvalSymlinks(local)
	if err != nil {
		t.Fatal(err)
	}
	got := VideoURLs([]MessageSegment{
		{Type: "video", Data: map[string]string{"file": "local-cache.mp4"}},
		{Type: "video", Data: map[string]string{"file": local}},
		{Type: "video", Data: map[string]string{"url": "https://example.com/a.mp4"}},
		{Type: "video", Data: map[string]string{"video_url": "https://example.com/a.mp4"}},
		{Type: "image", Data: map[string]string{"url": "https://example.com/ignore.jpg"}},
	})
	if len(got) != 2 || got[0] != local || got[1] != "https://example.com/a.mp4" {
		t.Fatalf("VideoURLs() = %#v", got)
	}
}

// TestTextToOneBotSegmentsKeepsCQAt 验证对应功能场景。
func TestTextToOneBotSegmentsKeepsCQAt(t *testing.T) {
	got := TextToOneBotSegments("[CQ:at,qq=123] hello")
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %#v", len(got), got)
	}
	if got[0].Type != "at" || got[0].Data["qq"] != "123" {
		t.Fatalf("first segment = %#v", got[0])
	}
}

func TestTextToOneBotSegmentsConvertsPlainMention(t *testing.T) {
	got := TextToOneBotSegments("看下 @10005 的好感度")
	if len(got) != 3 {
		t.Fatalf("segments = %#v", got)
	}
	if got[1].Type != "at" || got[1].Data["qq"] != "10005" {
		t.Fatalf("mention = %#v", got[1])
	}
	if got[2].Type != "text" || got[2].Data["text"] != " 的好感度" {
		t.Fatalf("text after mention = %#v", got[2])
	}
}

func TestTextToOneBotSegmentsAddsSpaceAfterMention(t *testing.T) {
	got := TextToOneBotSegments("[CQ:at,qq=10005]当前好感度是 5")
	if len(got) != 3 || got[0].Type != "at" || got[1].Type != "text" || got[1].Data["text"] != " " {
		t.Fatalf("segments = %#v", got)
	}
	plain := TextToOneBotSegments("联系 a@123456.com 或 https://example.com/@123456")
	for _, segment := range plain {
		if segment.Type == "at" {
			t.Fatalf("email or URL became a mention: %#v", plain)
		}
	}
}

// TestOneBotChannelSendPrefixesReplyAndMention 验证对应功能场景。
func TestOneBotChannelSendPrefixesReplyAndMention(t *testing.T) {
	message := buildOutgoingSegments(OutgoingMessage{
		GroupID:        "123",
		Text:           "你好",
		ImageURLs:      []string{"data:image/png;base64,abcd"},
		ReplyMessageID: "456",
		MentionUserID:  "789",
	})
	if len(message) < 4 {
		t.Fatalf("message = %#v", message)
	}
	if message[0]["type"] != "reply" || message[1]["type"] != "at" {
		t.Fatalf("message = %#v", message)
	}
	space, ok := message[2]["data"].(map[string]string)
	if message[2]["type"] != "text" || !ok || space["text"] != " " {
		t.Fatalf("mention spacer = %#v", message[2])
	}
	image, ok := message[len(message)-1]["data"].(map[string]string)
	if message[len(message)-1]["type"] != "image" || !ok || image["file"] != "base64://abcd" {
		t.Fatalf("image segment = %#v", message[len(message)-1])
	}
}

func TestOneBotOutgoingSegmentsIncludeImagesAndVideos(t *testing.T) {
	message := buildOutgoingSegments(OutgoingMessage{
		Text:      "媒体",
		ImageURLs: []string{"https://example.com/image.jpg"},
		VideoURLs: []string{"https://example.com/video.mp4"},
	})
	if len(message) != 3 {
		t.Fatalf("message = %#v", message)
	}
	if message[1]["type"] != "image" || message[2]["type"] != "video" {
		t.Fatalf("message = %#v", message)
	}
}

// TestOneBotOutgoingSegmentsDropNonNumericIDs 验证模型手写的 CQ 码里带着没还原的
// 脱敏别名时，只丢掉那一段，正文照发，数字账号的 @ 不受影响。
func TestOneBotOutgoingSegmentsDropNonNumericIDs(t *testing.T) {
	message := buildOutgoingSegments(OutgoingMessage{
		Text: "[CQ:reply,id=im_message_abc][CQ:at,qq=im_current_user_547f72c642a] 你说得对 [CQ:at,qq=10001] 看这里 [CQ:at,qq=all][CQ:poke,qq=im_user_x]",
	})
	var texts []string
	var mentions []string
	for _, segment := range message {
		data, _ := segment["data"].(map[string]string)
		switch segment["type"] {
		case "text":
			texts = append(texts, data["text"])
		case "at":
			mentions = append(mentions, data["qq"])
		default:
			t.Fatalf("unexpected segment survived: %#v", message)
		}
	}
	if strings.Join(mentions, ",") != "10001,all" {
		t.Fatalf("mentions = %v, message = %#v", mentions, message)
	}
	joined := strings.Join(texts, "")
	if strings.Contains(joined, "im_") || !strings.HasPrefix(joined, "你说得对 ") || !strings.Contains(joined, " 看这里 ") {
		t.Fatalf("texts = %q", texts)
	}

	valid := buildOutgoingSegments(OutgoingMessage{Text: "[CQ:reply,id=-2147483000][CQ:at,qq=10001] 好"})
	if len(valid) != 3 || valid[0]["type"] != "reply" || valid[1]["type"] != "at" {
		t.Fatalf("valid segments changed: %#v", valid)
	}
	if data := valid[1]["data"].(map[string]string); data["qq"] != "10001" {
		t.Fatalf("valid mention changed: %#v", valid)
	}

	// 入站解析不受影响：收到什么就记什么。
	inbound := CQToSegments("[CQ:at,qq=im_user_abc]")
	if len(inbound) != 1 || inbound[0].Type != "at" || inbound[0].Data["qq"] != "im_user_abc" {
		t.Fatalf("inbound parsing changed: %#v", inbound)
	}
}

func TestForwardOutgoingSegmentsRemoveMentions(t *testing.T) {
	message := buildForwardOutgoingSegments(OutgoingMessage{
		Text:          "[CQ:at,qq=123] 第一位，@456 第二位",
		MentionUserID: "789",
		ImageURLs:     []string{"data:image/png;base64,abcd"},
	})
	if len(message) == 0 {
		t.Fatal("forward message is empty")
	}
	for _, segment := range message {
		if segment["type"] == "at" {
			t.Fatalf("forward message contains mention: %#v", message)
		}
	}
	if message[len(message)-1]["type"] != "image" {
		t.Fatalf("non-mention segments were lost: %#v", message)
	}
}

func TestBuildForwardNodesRemoveMentions(t *testing.T) {
	nodes := buildForwardNodes([]string{"[CQ:at,qq=123] 节点内容"}, "Diana", "42")
	if len(nodes) != 1 {
		t.Fatalf("nodes = %#v", nodes)
	}
	data, ok := nodes[0]["data"].(map[string]any)
	if !ok {
		t.Fatalf("node data = %#v", nodes[0]["data"])
	}
	content, ok := data["content"].([]map[string]any)
	if !ok || len(content) == 0 {
		t.Fatalf("node content = %#v", data["content"])
	}
	for _, segment := range content {
		if segment["type"] == "at" {
			t.Fatalf("forward node contains mention: %#v", content)
		}
	}
}

// TestMessageEventFromEnvelopeNoticeGroupIncrease 验证对应功能场景。
func TestMessageEventFromEnvelopeNoticeGroupIncrease(t *testing.T) {
	event := messageEventFromEnvelope(oneBotEnvelope{
		Time:     123,
		SelfID:   "42",
		PostType: "notice",
		SubType:  "group_increase",
		UserID:   "10001",
		GroupID:  "20002",
	})
	if event.Kind != EventKindNotice || event.SubType != "group_increase" {
		t.Fatalf("event = %#v", event)
	}
	if event.UserID != "10001" || event.GroupID != "20002" {
		t.Fatalf("event = %#v", event)
	}
}

// TestMessageEventFromEnvelopeNoticeTypeGroupRecall 验证 OneBot 撤回 notice_type 能映射到内部 SubType。
func TestMessageEventFromEnvelopeNoticeTypeGroupRecall(t *testing.T) {
	event := messageEventFromEnvelope(oneBotEnvelope{
		Time:       123,
		SelfID:     "42",
		PostType:   "notice",
		NoticeType: "group_recall",
		UserID:     "10001",
		GroupID:    "20002",
		MessageID:  "old-1",
		OperatorID: "30003",
	})
	if event.Kind != EventKindNotice || event.SubType != "group_recall" || event.MessageID != "old-1" || event.OperatorID != "30003" {
		t.Fatalf("event = %#v", event)
	}
	if len(event.Segments) != 1 || event.Segments[0].Data["notice_type"] != "group_recall" || event.Segments[0].Data["operator_id"] != "30003" {
		t.Fatalf("segments = %#v", event.Segments)
	}
}

// TestOneBotEnvelopeAllowsObjectStatus 验证对应功能场景。
func TestOneBotEnvelopeAllowsObjectStatus(t *testing.T) {
	var envelope oneBotEnvelope
	err := json.Unmarshal([]byte(`{"status":{"online":true,"good":true},"retcode":0,"echo":"debug"}`), &envelope)
	if err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if !envelopeStatusOK(envelope) {
		t.Fatalf("status should be ok: %#v", envelope.Status)
	}
	if text := envelopeStatusText(envelope.Status); text == "" {
		t.Fatal("status text should not be empty")
	}
}

func TestOneBotDataMapWrapsArrayData(t *testing.T) {
	var envelope oneBotEnvelope
	err := json.Unmarshal([]byte(`{"status":"ok","retcode":0,"echo":"members","data":[{"user_id":10001}]}`), &envelope)
	if err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	data := oneBotDataMap(envelope.Data)
	items, ok := data["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items = %#v", data["items"])
	}
}

func TestReverseServerStaleReadLoopCannotDisconnectReplacement(t *testing.T) {
	server := NewOneBotReverseServer(OneBotConfig{Endpoint: "/onebot/v11/ws"})
	oldConn := &websocket.Conn{}
	newConn := &websocket.Conn{}
	server.conn = newConn
	server.status = ChannelStatus{Connected: true, SelfID: "42"}

	server.disconnectIfCurrent(oldConn, "old connection closed")
	if server.conn != newConn || !server.Status().Connected {
		t.Fatal("stale read loop disconnected the replacement websocket")
	}

	server.disconnectIfCurrent(newConn, "current connection closed")
	status := server.Status()
	if server.conn != nil || status.Connected || status.LastError != "current connection closed" {
		t.Fatalf("current disconnect status = %#v", status)
	}
}

// waitForReverseConnection 等反向服务端把连接登记好再断言。
//
// Dial 在客户端读到 HTTP 101 的那一刻就返回了，服务端的 handler 还在自己的
// goroutine 里往下跑，连接状态未必已经写进去。紧接着直接读 Status() 会偶发
// 读到 Connected=false——这不是实现有问题，是断言比被测行为跑得快。
func waitForReverseConnection(t *testing.T, reverse *OneBotReverseServer, epoch uint64) ChannelStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var status ChannelStatus
	for time.Now().Before(deadline) {
		if status = reverse.Status(); status.Connected && status.ConnectionEpoch == epoch {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("reverse server did not register connection epoch %d: %#v", epoch, status)
	return status
}

// attachReverseHandler 让监听器进入「有机器人在跑」的状态。握手要求有登记的
// handler：停用后连接就该被拒，所以直接拨号的用例得先登记一个。
// 返回的 cancel 等价于机器人被停用：通道拆掉，handler 跟着消失。
func attachReverseHandler(t *testing.T, reverse *OneBotReverseServer) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = reverse.Connect(ctx, func(context.Context, MessageEvent) error { return nil }) }()
	waitForCondition(t, 2*time.Second, reverse.handlerAttached)
	return cancel
}

func TestReverseServerRejectsDuplicateClientWithoutReplacingHealthyConnection(t *testing.T) {
	reverse := NewOneBotReverseServer(OneBotConfig{AccessToken: "test-token", Endpoint: "/onebot/v11/ws"})
	server := httptest.NewServer(reverse)
	attachReverseHandler(t, reverse)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	headers := http.Header{
		"Authorization": []string{"Bearer test-token"},
		"X-Self-ID":     []string{"42"},
		"User-Agent":    []string{"onebot-primary"},
	}

	primary, response, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("primary dial error = %v response=%v", err, response)
	}
	defer primary.Close()
	status := waitForReverseConnection(t, reverse, 1)
	if status.ConnectionOwner == "" {
		t.Fatalf("primary connection status = %#v", status)
	}

	duplicateHeaders := headers.Clone()
	duplicateHeaders.Set("User-Agent", "onebot-duplicate")
	duplicate, response, err := websocket.DefaultDialer.Dial(wsURL, duplicateHeaders)
	if duplicate != nil {
		_ = duplicate.Close()
	}
	if !errors.Is(err, websocket.ErrBadHandshake) || response == nil || response.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate dial error=%v status=%v", err, response)
	}
	_ = response.Body.Close()
	status = reverse.Status()
	if !status.Connected || status.ConnectionEpoch != 1 || status.DuplicateConnections != 1 || status.LastRejectedClient == "" || status.LastConnectionEventTime == nil {
		t.Fatalf("duplicate conflict status = %#v", status)
	}
	if status.ConnectionOwner == status.LastRejectedClient {
		t.Fatal("distinct clients received the same fingerprint")
	}

	if err := primary.Close(); err != nil {
		t.Fatal(err)
	}
	waitUntil := time.Now().Add(2 * time.Second)
	for reverse.Status().Connected && time.Now().Before(waitUntil) {
		time.Sleep(10 * time.Millisecond)
	}
	if reverse.Status().Connected {
		t.Fatal("primary connection did not transition to disconnected")
	}

	reconnected, response, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("reconnect dial error = %v response=%v", err, response)
	}
	defer reconnected.Close()
	if status = waitForReverseConnection(t, reverse, 2); status.LastRejectedClient == "" {
		t.Fatalf("reconnected status = %#v", status)
	}
}

func TestReverseServerStatusUpdatesPreserveConnectionEpoch(t *testing.T) {
	server := NewOneBotReverseServer(OneBotConfig{Endpoint: "/onebot/v11/ws"})
	server.status.ConnectionEpoch = 3
	server.setStatus(true, "42", "")
	server.setStatus(true, "42", "temporary parse error")
	if status := server.Status(); status.ConnectionEpoch != 3 || !status.Connected || status.SelfID != "42" {
		t.Fatalf("status update lost connection identity: %#v", status)
	}
}

func TestReverseServerHeartbeatTracksBotAccountHealth(t *testing.T) {
	server := NewOneBotReverseServer(OneBotConfig{Endpoint: "/onebot/v11/ws"})
	if err := server.handleFrame([]byte(`{"post_type":"meta_event","meta_event_type":"heartbeat","self_id":42,"status":{"online":false,"good":false}}`)); err != nil {
		t.Fatal(err)
	}
	status := server.Status()
	if !status.Connected || !status.AccountStatusKnown || status.AccountOnline || status.AccountGood || !strings.Contains(status.AccountStatusMessage, "重新登录") {
		t.Fatalf("offline heartbeat status = %#v", status)
	}

	if err := server.handleFrame([]byte(`{"post_type":"meta_event","meta_event_type":"heartbeat","self_id":42,"status":{"online":true,"good":true}}`)); err != nil {
		t.Fatal(err)
	}
	status = server.Status()
	if !status.AccountStatusKnown || !status.AccountOnline || !status.AccountGood || status.AccountStatusMessage != "" {
		t.Fatalf("healthy heartbeat status = %#v", status)
	}
}

func TestReverseServerRequiresAccessToken(t *testing.T) {
	server := NewOneBotReverseServer(OneBotConfig{})
	request := httptest.NewRequest("GET", "http://localhost/onebot/v11/ws", nil)
	if ok, reason := server.authorized(request); ok || reason != "server_token_unset" {
		t.Fatalf("empty OneBot token authorized=%v reason=%q", ok, reason)
	}

	server.SetConfig(OneBotConfig{AccessToken: "0123456789abcdef"})
	if ok, reason := server.authorized(request); ok || reason != "token_missing" {
		t.Fatalf("request without credentials authorized=%v reason=%q", ok, reason)
	}
	request.Header.Set("Authorization", "Bearer 0123456789abcdef")
	if ok, _ := server.authorized(request); !ok {
		t.Fatal("matching bearer token was rejected")
	}
	request.Header.Set("Authorization", "Bearer wrong")
	if ok, reason := server.authorized(request); ok || reason != "token_mismatch" {
		t.Fatalf("wrong token authorized=%v reason=%q", ok, reason)
	}
	request.URL.RawQuery = "access_token=0123456789abcdef"
	if ok, _ := server.authorized(request); !ok {
		t.Fatal("matching query token was rejected")
	}
}

// TestReverseServerAcceptsCommonAuthorizationSchemes 覆盖各家 OneBot 实现的
// Authorization 写法：规范是 Bearer，但也有发 Token 前缀或裸 token 的。
func TestReverseServerAcceptsCommonAuthorizationSchemes(t *testing.T) {
	const token = "0123456789abcdef"
	server := NewOneBotReverseServer(OneBotConfig{AccessToken: token})
	for _, header := range []string{"Bearer " + token, "bearer " + token, "Token " + token, token} {
		request := httptest.NewRequest("GET", "http://localhost/onebot/v11/ws", nil)
		request.Header.Set("Authorization", header)
		if ok, reason := server.authorized(request); !ok {
			t.Fatalf("Authorization %q rejected: %s", header, reason)
		}
	}
	for _, header := range []string{"Bearer wrong", "Token wrong", "wrong", "Basic " + token} {
		request := httptest.NewRequest("GET", "http://localhost/onebot/v11/ws", nil)
		request.Header.Set("Authorization", header)
		if ok, _ := server.authorized(request); ok {
			t.Fatalf("Authorization %q must not authorize", header)
		}
	}
}

// TestReverseServerRecordsUnauthorizedHandshake 固定 401 的可排查性：
// 状态里要留下计数和原因，不然接入端只看到 401，两边都查不出为什么。
func TestReverseServerRecordsUnauthorizedHandshake(t *testing.T) {
	server := NewOneBotReverseServer(OneBotConfig{AccessToken: "0123456789abcdef"})
	request := httptest.NewRequest("GET", "http://localhost/onebot/v11/ws", nil)
	request.Header.Set("Authorization", "Bearer wrong-token-value")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	status := server.Status()
	if status.UnauthorizedConnections != 1 || status.LastConnectionEvent != "unauthorized:token_mismatch" || status.LastRejectedClient == "" {
		t.Fatalf("status = %#v", status)
	}
}

// 握手被拒以前只打到终端。机器人全停用时运行时已经退出，只有监听器自己能记，
// 所以由它直接写运行日志；接入端几秒重连一次，同一原因和客户端一分钟只记一条。
func TestReverseServerWritesRejectionToAppLog(t *testing.T) {
	const token = "0123456789abcdef"
	server := NewOneBotReverseServer(OneBotConfig{AccessToken: token})
	logs := &captureAppLogs{}
	server.SetAppLogWriter(logs)
	// 接入端每次重连换一个本地端口，仍然是同一个客户端。
	for port := range 3 {
		request := httptest.NewRequest("GET", "http://localhost/onebot/v11/ws", nil)
		request.RemoteAddr = fmt.Sprintf("172.18.0.3:%d", 40000+port)
		request.Header.Set("Authorization", "Bearer wrong-token-value")
		request.Header.Set("X-Self-ID", "10001")
		request.Header.Set("User-Agent", "SnowLuma/1.2 (linux)")
		server.ServeHTTP(httptest.NewRecorder(), request)
	}
	entries := logs.entriesSnapshot()
	if len(entries) != 1 {
		t.Fatalf("rejection logs = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Action != "onebot_handshake_rejected" || entry.Metadata["reason"] != "token_mismatch" || !strings.Contains(entry.Message, "不一致") {
		t.Fatalf("entry = %#v", entry)
	}
	if entry.Target != "172.18.0.3 · QQ 10001 · SnowLuma/1.2" {
		t.Fatalf("client label = %q", entry.Target)
	}
	if strings.Contains(entry.Message+entry.Detail+entry.Target, "wrong-token-value") {
		t.Fatal("rejection log leaked the presented token")
	}
}

func TestReverseServerRejectsCrossOriginBrowser(t *testing.T) {
	request := httptest.NewRequest("GET", "http://bot.example/onebot/v11/ws", nil)
	request.Host = "bot.example"
	if !sameOriginWebSocketRequest(request) {
		t.Fatal("origin-less OneBot request was rejected")
	}
	request.Header.Set("Origin", "https://attacker.example")
	if sameOriginWebSocketRequest(request) {
		t.Fatal("cross-origin browser WebSocket was accepted")
	}
	request.Header.Set("Origin", "http://bot.example")
	if !sameOriginWebSocketRequest(request) {
		t.Fatal("same-origin browser WebSocket was rejected")
	}
}

func TestReverseServerConnectionOriginFollowsHandshake(t *testing.T) {
	reverse := NewOneBotReverseServer(OneBotConfig{AccessToken: "test-token", Endpoint: "/onebot/v11/ws"})
	server := httptest.NewServer(reverse)
	attachReverseHandler(t, reverse)
	defer server.Close()
	if origin := reverse.ConnectionOrigin(); origin != "" {
		t.Fatalf("ConnectionOrigin() = %q before any connection", origin)
	}

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	headers := http.Header{
		"Authorization": []string{"Bearer test-token"},
		"X-Self-ID":     []string{"42"},
	}
	conn, response, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("dial error = %v response=%v", err, response)
	}
	defer conn.Close()

	// 同样要等服务端登记完再断言：Dial 返回时 handler 可能还没写下握手来源。
	want := server.URL // httptest 的 URL 就是客户端握手用的 http://host:port
	waitForCondition(t, 2*time.Second, func() bool { return reverse.ConnectionOrigin() == want })
	if origin := reverse.ConnectionOrigin(); origin != want {
		t.Fatalf("ConnectionOrigin() = %q, want %q", origin, want)
	}

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	waitUntil := time.Now().Add(2 * time.Second)
	for reverse.ConnectionOrigin() != "" && time.Now().Before(waitUntil) {
		time.Sleep(10 * time.Millisecond)
	}
	if origin := reverse.ConnectionOrigin(); origin != "" {
		t.Fatalf("ConnectionOrigin() = %q after disconnect", origin)
	}
}

func TestOneBotRequestOriginRespectsForwardedProto(t *testing.T) {
	request := httptest.NewRequest("GET", "http://bridge.example:18080/", nil)
	if origin := oneBotRequestOrigin(request); origin != "http://bridge.example:18080" {
		t.Fatalf("oneBotRequestOrigin() = %q", origin)
	}
	request.Header.Set("X-Forwarded-Proto", "https")
	if origin := oneBotRequestOrigin(request); origin != "https://bridge.example:18080" {
		t.Fatalf("oneBotRequestOrigin() with X-Forwarded-Proto = %q", origin)
	}
}

func TestIsOneBotReverseHandshake(t *testing.T) {
	tests := []struct {
		name    string
		upgrade string
		headers map[string]string
		want    bool
	}{
		{"bare path with self id", "websocket", map[string]string{"X-Self-ID": "42"}, true},
		{"client role only", "websocket", map[string]string{"X-Client-Role": "Universal"}, true},
		{"browser websocket without onebot headers", "websocket", nil, false},
		{"plain http with self id", "", map[string]string{"X-Self-ID": "42"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "http://bot.example/", nil)
			if tt.upgrade != "" {
				request.Header.Set("Upgrade", tt.upgrade)
			}
			for key, value := range tt.headers {
				request.Header.Set(key, value)
			}
			if got := IsOneBotReverseHandshake(request); got != tt.want {
				t.Fatalf("IsOneBotReverseHandshake() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractOutgoingReplyMarkerParsesLeadingMarker(t *testing.T) {
	// 入站 reply 段渲染成引用标记给模型看，模型照抄后必须还原成 reply 段。
	for _, tc := range []struct {
		name     string
		input    string
		wantID   string
		wantRest string
	}{
		{name: "negative id", input: "[diana-reply:-797497448]就是这张，我喜欢的第 3 个房间。", wantID: "-797497448", wantRest: "就是这张，我喜欢的第 3 个房间。"},
		{name: "positive id", input: "[diana-reply:30006]收到", wantID: "30006", wantRest: "收到"},
		{name: "space after marker", input: "[diana-reply:30006]  收到", wantID: "30006", wantRest: "收到"},
		{name: "marker only", input: "[diana-reply:30006]", wantID: "30006", wantRest: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, rest, ok := extractOutgoingReplyMarker(tc.input)
			if !ok || id != tc.wantID || rest != tc.wantRest {
				t.Fatalf("extractOutgoingReplyMarker(%q) = (%q, %q, %t)", tc.input, id, rest, ok)
			}
		})
	}
}

func TestExtractOutgoingReplyMarkerKeepsNonMarkerText(t *testing.T) {
	// 只认开头且 ID 是数字的写法，正文里提到的字样必须原样留在文本里。
	for _, input := range []string{
		"我刚才[diana-reply:30006]过了",
		"[diana-reply:谁]",
		"[diana-reply:]",
		"[diana-reply:30006",
		"[diana-reply:12ab]",
		"没有标记",
	} {
		id, rest, ok := extractOutgoingReplyMarker(input)
		if ok || id != "" || rest != input {
			t.Fatalf("extractOutgoingReplyMarker(%q) = (%q, %q, %t), want untouched", input, id, rest, ok)
		}
	}
}

func TestApplyOutgoingReplyMarkerOverridesDefaultTarget(t *testing.T) {
	// 用户要求引用旧消息时，模型指定的目标要盖过默认的“回复当前消息”。
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "current-message"}
	runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "-797497448", RawMessage: "第 3 个房间"})

	msg := runtime.applyOutgoingReplyMarker(context.Background(), event, OutgoingMessage{
		Text:           "[diana-reply:-797497448]就是这张。",
		ReplyMessageID: "current-message",
	})
	if msg.ReplyMessageID != "-797497448" {
		t.Fatalf("ReplyMessageID = %q, want the id the model asked for", msg.ReplyMessageID)
	}
	if msg.Text != "就是这张。" {
		t.Fatalf("Text = %q, want the marker stripped", msg.Text)
	}
	segments := buildOutgoingSegments(msg)
	if len(segments) == 0 || segments[0]["type"] != "reply" {
		t.Fatalf("segments = %#v, want a leading reply segment", segments)
	}
	if data, _ := segments[0]["data"].(map[string]string); data["id"] != "-797497448" {
		t.Fatalf("reply segment = %#v", segments[0])
	}
}

func TestApplyOutgoingReplyMarkerIgnoresUnknownMessageID(t *testing.T) {
	// 标记与入站渲染同形，模型可能是在照抄用户原话或编了个 ID：本会话查不到这条
	// 消息时只去掉标记，不生成指向空消息的 reply 段。
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "current-message"}

	msg := runtime.applyOutgoingReplyMarker(context.Background(), event, OutgoingMessage{
		Text:           "[diana-reply:123456]原样发这句",
		ReplyMessageID: "current-message",
	})
	if msg.ReplyMessageID != "" {
		t.Fatalf("ReplyMessageID = %q, invalid auto reference must not fall back to another target", msg.ReplyMessageID)
	}
	if msg.Text != "原样发这句" {
		t.Fatalf("Text = %q, want the marker stripped from the text", msg.Text)
	}
}

func TestApplyOutgoingReplyMarkerLeavesOtherMessagesAlone(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "current-message"}
	msg := runtime.applyOutgoingReplyMarker(context.Background(), event, OutgoingMessage{Text: "普通回复", ReplyMessageID: "current-message"})
	if msg.Text != "普通回复" || msg.ReplyMessageID != "current-message" {
		t.Fatalf("msg = %#v, want untouched", msg)
	}
}

func TestPlainTextRendersNeutralReplyMarker(t *testing.T) {
	// 入站渲染用自有标记而不是某个平台的说法，模型在各平台看到的写法一致。
	got := PlainText([]MessageSegment{
		{Type: "reply", Data: map[string]string{"id": "-797497448"}},
		{Type: "text", Data: map[string]string{"text": "这张不错"}},
	})
	if got != "[diana-reply:-797497448]这张不错" {
		t.Fatalf("PlainText = %q", got)
	}
}

// at 段带昵称时渲染成「@昵称（账号）」，只有号码时退回「@QQ」。
func TestPlainTextRendersAtMentionsWithNicknames(t *testing.T) {
	segments := []MessageSegment{
		{Type: "at", Data: map[string]string{"qq": "90001", "name": "小满"}},
		{Type: "at", Data: map[string]string{"qq": "4200000001", "card": "群名片"}},
		{Type: "at", Data: map[string]string{"qq": "9999999999"}},
		{Type: "at", Data: map[string]string{"qq": "all"}},
		{Type: "text", Data: map[string]string{"text": "少回复点"}},
	}
	got := PlainText(segments)
	for _, want := range []string{"@小满（90001）", "@群名片（4200000001）", "@9999999999", "少回复点"} {
		if !strings.Contains(got, want) {
			t.Fatalf("plain text missing %q: %q", want, got)
		}
	}
	// @全体成员没有具体对象，仍旧不渲染出来。
	if strings.Contains(got, "@all") {
		t.Fatalf("at-all should not render as a raw id: %q", got)
	}
}

func TestAtMentionTextHandlesMissingPieces(t *testing.T) {
	if got := AtMentionText("123", ""); got != "@123" {
		t.Fatalf("id-only mention = %q", got)
	}
	if got := AtMentionText("", "小满"); got != "@小满" {
		t.Fatalf("name-only mention = %q", got)
	}
	if got := AtMentionText("123", "  小满  "); got != "@小满（123）" {
		t.Fatalf("trimmed mention = %q", got)
	}
}

// TestReverseServerRejectsHandshakeWithoutRunningBot 固定停用语义：机器人停用后
// 通道被拆掉，凭据哪怕还对得上，握手也要当场被拒——接受连接再把事件丢掉的话，
// 接入端和控制台都显示「已连接」，看不出机器人其实是关着的。
func TestReverseServerRejectsHandshakeWithoutRunningBot(t *testing.T) {
	const token = "0123456789abcdef"
	server := NewOneBotReverseServer(OneBotConfig{AccessToken: token})
	newRequest := func() *http.Request {
		request := httptest.NewRequest("GET", "http://localhost/onebot/v11/ws", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		return request
	}

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, newRequest())
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 before any Connect", recorder.Code)
	}
	if status := server.Status(); status.LastConnectionEvent != "rejected:bot_disabled" || status.UnauthorizedConnections != 0 {
		t.Fatalf("status = %#v", status)
	}

	cancel := attachReverseHandler(t, server)

	// 运行中不该再拒：这条路径只认「有没有机器人在跑」。
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, newRequest())
	if recorder.Code == http.StatusServiceUnavailable {
		t.Fatal("running bot must not be rejected")
	}

	// 停用等价于 Connect 的 context 被取消：handler 必须跟着通道一起消失。
	cancel()
	waitForCondition(t, 2*time.Second, func() bool { return !server.handlerAttached() })
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, newRequest())
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 after the bot stopped", recorder.Code)
	}
}
