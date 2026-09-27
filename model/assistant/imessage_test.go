// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const testIMessageWebhookToken = "hook-token-0123456789abcdef"

// fakeBlueBubbles 模拟 BlueBubbles Server 的 REST 接口，字段形状照 bluebubbles-server
// 的 MessageSerializer / ChatSerializer / GeneralInterface.getServerMetadata，
// message/query 的 after 与源码一样按 >= 过滤。
type fakeBlueBubbles struct {
	t          *testing.T
	password   string
	privateAPI bool

	mu          sync.Mutex
	textSends   []map[string]any
	attachments []map[string]string
	messages    []map[string]any
	chats       map[string]bool
	queryBodies []map[string]any
	queryFail   bool
	// downloadGates 让某个附件的下载停住，直到测试放行。
	downloadGates map[string]chan struct{}
}

func (f *fakeBlueBubbles) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("password") != f.password {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": 401, "message": "You are not authorized to access this resource",
				"error": map[string]any{"type": "Authentication Error", "message": "Invalid password"},
			})
			return
		}
		ok := func(data any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "message": "Success", "data": data})
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/server/info":
			ok(map[string]any{
				"server_version": "1.9.9", "os_version": "14.5", "private_api": f.privateAPI,
				"helper_connected": f.privateAPI, "detected_imessage": "diana@icloud.com",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/message/text":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.textSends = append(f.textSends, body)
			f.mu.Unlock()
			ok(map[string]any{"guid": "sent-text-guid", "tempGuid": body["tempGuid"], "isFromMe": true})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/message/attachment":
			if err := r.ParseMultipartForm(8 << 20); err != nil {
				f.t.Errorf("multipart: %v", err)
				return
			}
			file, header, err := r.FormFile("attachment")
			if err != nil {
				f.t.Errorf("attachment field: %v", err)
				return
			}
			content, _ := io.ReadAll(file)
			f.mu.Lock()
			f.attachments = append(f.attachments, map[string]string{
				"chatGuid": r.FormValue("chatGuid"), "tempGuid": r.FormValue("tempGuid"), "name": r.FormValue("name"),
				"method": r.FormValue("method"), "filename": header.Filename, "content": string(content),
			})
			f.mu.Unlock()
			ok(map[string]any{"guid": "sent-attachment-guid"})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/attachment/") && strings.HasSuffix(r.URL.Path, "/download"):
			guid := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/attachment/"), "/download")
			f.mu.Lock()
			gate := f.downloadGates[guid]
			f.mu.Unlock()
			if gate != nil {
				select {
				case <-gate:
				case <-r.Context().Done():
					return
				}
			}
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("\xff\xd8\xff\xe0fake-jpeg-" + guid))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/message/query":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.queryBodies = append(f.queryBodies, body)
			fail := f.queryFail
			selected := make([]map[string]any, 0, len(f.messages))
			after, hasAfter := body["after"].(float64)
			for _, message := range f.messages {
				if hasAfter && float64(message["dateCreated"].(int64)) < after {
					continue
				}
				selected = append(selected, message)
			}
			f.mu.Unlock()
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"status":500,"message":"Database locked","error":{"type":"Server Error","message":"Database locked"}}`))
				return
			}
			sort.SliceStable(selected, func(i, j int) bool {
				if body["sort"] == "DESC" {
					return selected[i]["dateCreated"].(int64) > selected[j]["dateCreated"].(int64)
				}
				return selected[i]["dateCreated"].(int64) < selected[j]["dateCreated"].(int64)
			})
			if limit, _ := body["limit"].(float64); limit > 0 && int(limit) < len(selected) {
				selected = selected[:int(limit)]
			}
			ok(selected)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/chat/query":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			guid, _ := body["guid"].(string)
			f.mu.Lock()
			found := f.chats[guid]
			f.mu.Unlock()
			if found {
				ok([]map[string]any{{"guid": guid, "style": 45}})
				return
			}
			ok([]map[string]any{})
		default:
			http.NotFound(w, r)
		}
	})
}

func (f *fakeBlueBubbles) addMessage(message map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, message)
}

func newFakeBlueBubbles(t *testing.T, privateAPI bool) (*fakeBlueBubbles, *httptest.Server) {
	t.Helper()
	fake := &fakeBlueBubbles{t: t, password: "bb-secret", privateAPI: privateAPI, chats: map[string]bool{}, downloadGates: map[string]chan struct{}{}}
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)
	return fake, server
}

func imessageWebhookMessage(overrides map[string]any) map[string]any {
	message := map[string]any{
		"guid":        "msg-1",
		"text":        "你好",
		"isFromMe":    false,
		"dateCreated": int64(1726000000123),
		"itemType":    0,
		"handle":      map[string]any{"address": "+8613800000000"},
		"chats": []any{map[string]any{
			"guid": "iMessage;-;+8613800000000", "chatIdentifier": "+8613800000000", "style": 45,
		}},
		"attachments":           []any{},
		"associatedMessageGuid": nil,
		"associatedMessageType": nil,
		"threadOriginatorGuid":  nil,
	}
	for key, value := range overrides {
		message[key] = value
	}
	return message
}

func imessageGroupChats() []any {
	return []any{map[string]any{"guid": "iMessage;+;chat123456", "chatIdentifier": "chat123456", "displayName": "周末爬山", "style": 43}}
}

func decodeIMessage(t *testing.T, payload map[string]any) imessageMessage {
	t.Helper()
	raw, _ := json.Marshal(payload)
	var message imessageMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatal(err)
	}
	return message
}

// isolateIMessageState 把持久状态放进测试自己的目录，重启用例靠同一个目录模拟。
func isolateIMessageState(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DIANA_IMESSAGE_STATE_DIR", dir)
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	return dir
}

func testIMessageConfig(server *httptest.Server) IMessageConfig {
	return IMessageConfig{ProfileID: "bot-imessage", ServerURL: server.URL, Password: "bb-secret", WebhookToken: testIMessageWebhookToken}
}

func TestIMessageEventMapsDirectAndGroupChats(t *testing.T) {
	event, ok := imessageEventFromMessage(decodeIMessage(t, imessageWebhookMessage(nil)), "diana@icloud.com")
	if !ok {
		t.Fatal("direct message was dropped")
	}
	if event.Kind != EventKindPrivate || !event.ToMe || event.GroupID != "" {
		t.Fatalf("direct chat mapped wrong: %+v", event)
	}
	if event.UserID != "+8613800000000" || event.MessageID != "msg-1" || event.Time != 1726000000 || event.Platform != PlatformIMessage {
		t.Fatalf("direct chat fields: %+v", event)
	}

	group := decodeIMessage(t, imessageWebhookMessage(map[string]any{
		"guid":                 "msg-2",
		"text":                 "￼看这个",
		"handle":               map[string]any{"address": "friend@example.com"},
		"threadOriginatorGuid": "sent-text-guid",
		"chats":                imessageGroupChats(),
	}))
	event, ok = imessageEventFromMessage(group, "")
	if !ok {
		t.Fatal("group message was dropped")
	}
	if event.Kind != EventKindGroup || event.ToMe || event.GroupID != "iMessage;+;chat123456" || event.GroupName != "周末爬山" {
		t.Fatalf("group chat mapped wrong: %+v", event)
	}
	if event.UserID != "friend@example.com" || event.RawMessage != "看这个" {
		t.Fatalf("group sender/text: %+v", event)
	}
	if event.Quoted == nil || event.Quoted.MessageID != "sent-text-guid" || event.Segments[0].Type != "reply" {
		t.Fatalf("reply relation lost: %+v", event)
	}
}

func TestIMessageEventSkipsOwnTapbacksAndSystemItems(t *testing.T) {
	cases := map[string]map[string]any{
		"from me":      {"isFromMe": true},
		"tapback":      {"associatedMessageGuid": "p:0/msg-1", "associatedMessageType": 2000, "text": "Loved “你好”"},
		"group rename": {"itemType": 2, "text": "改了群名"},
		"empty":        {"text": ""},
	}
	for name, overrides := range cases {
		if _, ok := imessageEventFromMessage(decodeIMessage(t, imessageWebhookMessage(overrides)), ""); ok {
			t.Fatalf("%s should not become a conversation event", name)
		}
	}
}

func TestIMessageProbeReadsServerInfoAndSurfacesAuthErrors(t *testing.T) {
	_, server := newFakeBlueBubbles(t, true)
	info, err := ProbeIMessageServer(context.Background(), server.Client(), server.URL+"/", "bb-secret")
	if err != nil {
		t.Fatal(err)
	}
	if info.DetectedIMessage != "diana@icloud.com" || !info.PrivateAPI || info.ServerVersion != "1.9.9" {
		t.Fatalf("server info: %+v", info)
	}
	_, err = ProbeIMessageServer(context.Background(), server.Client(), server.URL, "wrong")
	if err == nil || !strings.Contains(err.Error(), "Invalid password") {
		t.Fatalf("auth error should carry BlueBubbles' message, got %v", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Fatalf("password leaked into the error: %v", err)
	}
}

// 连上之后跑一个通道，返回收到的事件。
func startIMessageChannel(t *testing.T, cfg IMessageConfig) (*IMessageChannel, <-chan MessageEvent) {
	t.Helper()
	channel := NewIMessageChannel(cfg)
	events := make(chan MessageEvent, 16)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = channel.Connect(ctx, func(_ context.Context, event MessageEvent) error {
			events <- event
			return nil
		})
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	deadline := time.Now().Add(3 * time.Second)
	for !channel.Status().Connected {
		if time.Now().After(deadline) {
			t.Fatalf("channel never connected: %+v", channel.Status())
		}
		time.Sleep(10 * time.Millisecond)
	}
	return channel, events
}

func postIMessageWebhook(t *testing.T, query string, payload any) int {
	t.Helper()
	raw, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, IMessageCallbackPath+query, strings.NewReader(string(raw)))
	rec := httptest.NewRecorder()
	if !ServeCallback(PlatformIMessage, "bot-imessage", rec, req) {
		t.Fatal("no iMessage callback handler registered")
	}
	return rec.Code
}

func postIMessageNewMessage(t *testing.T, message map[string]any) {
	t.Helper()
	if code := postIMessageWebhook(t, "?token="+testIMessageWebhookToken, map[string]any{"type": "new-message", "data": message}); code != http.StatusOK {
		t.Fatalf("webhook status %d", code)
	}
}

func waitIMessageEvent(t *testing.T, events <-chan MessageEvent) MessageEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(3 * time.Second):
		t.Fatal("no event delivered")
	}
	return MessageEvent{}
}

func expectNoIMessageEvent(t *testing.T, events <-chan MessageEvent) {
	t.Helper()
	select {
	case extra := <-events:
		t.Fatalf("unexpected event delivered: %+v", extra)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestIMessageWebhookRequiresDedicatedTokenAndDeduplicates(t *testing.T) {
	isolateIMessageState(t)
	_, server := newFakeBlueBubbles(t, false)
	channel, events := startIMessageChannel(t, testIMessageConfig(server))
	if channel.Status().SelfID != "diana@icloud.com" {
		t.Fatalf("self id should come from server/info: %+v", channel.Status())
	}
	webhook := map[string]any{"type": "new-message", "data": imessageWebhookMessage(nil)}

	// 服务器密码写进 webhook 地址会落进访问日志，而它能读写整个账号，所以不认。
	for _, query := range []string{"", "?token=nope", "?password=bb-secret", "?token=bb-secret"} {
		if code := postIMessageWebhook(t, query, webhook); code != http.StatusUnauthorized {
			t.Fatalf("query %q: status %d, want 401", query, code)
		}
	}
	postIMessageNewMessage(t, imessageWebhookMessage(nil))
	event := waitIMessageEvent(t, events)
	if event.UserID != "+8613800000000" || event.RawMessage != "你好" {
		t.Fatalf("webhook event: %+v", event)
	}
	// 同一条消息重推（或轮询又拉到一次）不能回答两遍。
	postIMessageNewMessage(t, imessageWebhookMessage(nil))
	postIMessageWebhook(t, "?token="+testIMessageWebhookToken, map[string]any{"type": "updated-message", "data": imessageWebhookMessage(map[string]any{"guid": "msg-9"})})
	expectNoIMessageEvent(t, events)
}

func TestIMessageWebhookRejectsEverythingWithoutToken(t *testing.T) {
	isolateIMessageState(t)
	_, server := newFakeBlueBubbles(t, false)
	cfg := testIMessageConfig(server)
	cfg.WebhookToken = ""
	_, events := startIMessageChannel(t, cfg)
	for _, query := range []string{"", "?token=", "?token=bb-secret", "?password=bb-secret"} {
		if code := postIMessageWebhook(t, query, map[string]any{"type": "new-message", "data": imessageWebhookMessage(nil)}); code != http.StatusUnauthorized {
			t.Fatalf("query %q: status %d, want 401", query, code)
		}
	}
	expectNoIMessageEvent(t, events)
}

func TestIMessageWebhookDownloadsAttachments(t *testing.T) {
	isolateIMessageState(t)
	_, server := newFakeBlueBubbles(t, false)
	_, events := startIMessageChannel(t, testIMessageConfig(server))
	postIMessageNewMessage(t, imessageWebhookMessage(map[string]any{
		"text":        "￼",
		"attachments": []any{map[string]any{"guid": "att-1", "mimeType": "image/jpeg", "transferName": "IMG_0001.jpeg", "totalBytes": 13}},
	}))
	event := waitIMessageEvent(t, events)
	var image *MessageSegment
	for index := range event.Segments {
		if event.Segments[index].Type == "image" {
			image = &event.Segments[index]
		}
	}
	if image == nil {
		t.Fatalf("image segment missing: %+v", event.Segments)
	}
	body, err := os.ReadFile(image.Data["file"])
	if err != nil || !strings.HasSuffix(string(body), "fake-jpeg-att-1") {
		t.Fatalf("cached attachment: %q %v", body, err)
	}
	for _, value := range image.Data {
		if strings.Contains(value, "bb-secret") {
			t.Fatalf("server password leaked into the segment: %+v", image.Data)
		}
	}
}

// 同一会话里图片后面紧跟一句文字，文字不能因为图片还在下载就先交上去。
func TestIMessageSameChatStaysInOrderWhileAttachmentDownloads(t *testing.T) {
	isolateIMessageState(t)
	fake, server := newFakeBlueBubbles(t, false)
	gate := make(chan struct{})
	fake.downloadGates["att-slow"] = gate
	_, events := startIMessageChannel(t, testIMessageConfig(server))

	postIMessageNewMessage(t, imessageWebhookMessage(map[string]any{
		"guid": "img-1", "text": "￼",
		"attachments": []any{map[string]any{"guid": "att-slow", "mimeType": "image/jpeg", "transferName": "a.jpeg"}},
	}))
	postIMessageNewMessage(t, imessageWebhookMessage(map[string]any{"guid": "text-2", "text": "看上面这张"}))
	expectNoIMessageEvent(t, events)
	close(gate)
	if first := waitIMessageEvent(t, events); first.MessageID != "img-1" {
		t.Fatalf("first event = %s, want the image", first.MessageID)
	}
	if second := waitIMessageEvent(t, events); second.MessageID != "text-2" {
		t.Fatalf("second event = %s, want the text", second.MessageID)
	}
}

// 一个会话的大附件卡住时，别的会话照常处理。
func TestIMessageSlowChatDoesNotBlockOtherChats(t *testing.T) {
	isolateIMessageState(t)
	fake, server := newFakeBlueBubbles(t, false)
	gate := make(chan struct{})
	fake.downloadGates["att-huge"] = gate
	defer close(gate)
	_, events := startIMessageChannel(t, testIMessageConfig(server))

	postIMessageNewMessage(t, imessageWebhookMessage(map[string]any{
		"guid": "huge-1", "text": "￼",
		"attachments": []any{map[string]any{"guid": "att-huge", "mimeType": "video/mp4", "transferName": "big.mp4"}},
	}))
	postIMessageNewMessage(t, imessageWebhookMessage(map[string]any{
		"guid": "group-1", "text": "群里有人吗", "handle": map[string]any{"address": "friend@example.com"}, "chats": imessageGroupChats(),
	}))
	if event := waitIMessageEvent(t, events); event.MessageID != "group-1" {
		t.Fatalf("other chat was blocked, got %s", event.MessageID)
	}
}

func TestIMessageSendTextUsesRememberedChatAcrossRestart(t *testing.T) {
	isolateIMessageState(t)
	fake, server := newFakeBlueBubbles(t, false)
	first, events := startIMessageChannel(t, testIMessageConfig(server))
	// 这个号码之前是走 SMS 发来的，回复要回到同一个会话。
	postIMessageNewMessage(t, imessageWebhookMessage(map[string]any{"chats": []any{map[string]any{"guid": "SMS;-;+8613800000000"}}}))
	waitIMessageEvent(t, events)
	_ = first.Close()

	// 重启后是一个全新的通道实例，映射要从持久状态里读回来。
	restarted := NewIMessageChannel(testIMessageConfig(server))
	result, err := restarted.SendWithResult(context.Background(), OutgoingMessage{UserID: "+8613800000000", Text: "收到", ReplyMessageID: "msg-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result["message_id"] != "sent-text-guid" {
		t.Fatalf("result = %v", result)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	sent := fake.textSends[0]
	if sent["chatGuid"] != "SMS;-;+8613800000000" || sent["message"] != "收到" || sent["method"] != "apple-script" {
		t.Fatalf("text send body: %v", sent)
	}
	if temp, _ := sent["tempGuid"].(string); !strings.HasPrefix(temp, "diana-") {
		t.Fatalf("apple-script sends need a tempGuid: %v", sent)
	}
	// 没开 Private API 时带 selectedMessageGuid 会让整条消息发不出去。
	if _, ok := sent["selectedMessageGuid"]; ok {
		t.Fatalf("reply target must be dropped without the Private API: %v", sent)
	}
}

// 没见过的私聊对象，先去服务端查真实会话，查不到才拼 iMessage 会话。
func TestIMessageSendLooksUpUnknownDirectChat(t *testing.T) {
	isolateIMessageState(t)
	fake, server := newFakeBlueBubbles(t, false)
	fake.chats["SMS;-;+15550001111"] = true
	channel := NewIMessageChannel(testIMessageConfig(server))
	for _, user := range []string{"+15550001111", "new@example.com"} {
		if err := channel.Send(context.Background(), OutgoingMessage{UserID: user, Text: "嗨"}); err != nil {
			t.Fatal(err)
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.textSends[0]["chatGuid"] != "SMS;-;+15550001111" {
		t.Fatalf("known SMS chat not found: %v", fake.textSends[0])
	}
	if fake.textSends[1]["chatGuid"] != "iMessage;-;new@example.com" {
		t.Fatalf("fallback chat guid: %v", fake.textSends[1])
	}
	if channel.stateStore().directChat("+15550001111") != "SMS;-;+15550001111" {
		t.Fatal("looked-up chat should be remembered")
	}
}

func TestIMessageSendReplyAndGroupWithPrivateAPI(t *testing.T) {
	isolateIMessageState(t)
	fake, server := newFakeBlueBubbles(t, true)
	channel, _ := startIMessageChannel(t, testIMessageConfig(server))
	if err := channel.Send(context.Background(), OutgoingMessage{GroupID: "iMessage;+;chat123456", UserID: "+8613800000000", Text: "好的", ReplyMessageID: "msg-2"}); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	reply := fake.textSends[0]
	if reply["chatGuid"] != "iMessage;+;chat123456" || reply["selectedMessageGuid"] != "msg-2" || reply["method"] != "private-api" {
		t.Fatalf("reply body: %v", reply)
	}
}

func TestIMessageSendAttachmentUploadsLocalFile(t *testing.T) {
	isolateIMessageState(t)
	fake, server := newFakeBlueBubbles(t, false)
	channel, _ := startIMessageChannel(t, testIMessageConfig(server))
	path := filepath.Join(t.TempDir(), "cat.png")
	if err := os.WriteFile(path, []byte("png-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := channel.SendWithResult(context.Background(), OutgoingMessage{
		GroupID:   "iMessage;+;chat123456",
		ImageURLs: []string{path},
		Segments:  []MessageSegment{{Type: "file", Data: map[string]string{"file": "base64://aGVsbG8="}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["message_id"] != "sent-attachment-guid" {
		t.Fatalf("result = %v", result)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.textSends) != 0 {
		t.Fatalf("empty text must not be sent: %v", fake.textSends)
	}
	if len(fake.attachments) != 2 {
		t.Fatalf("attachments = %v", fake.attachments)
	}
	first := fake.attachments[0]
	if first["chatGuid"] != "iMessage;+;chat123456" || first["name"] != "cat.png" || first["content"] != "png-bytes" ||
		first["method"] != "apple-script" || !strings.HasPrefix(first["tempGuid"], "diana-") {
		t.Fatalf("attachment form: %v", first)
	}
	if fake.attachments[1]["content"] != "hello" {
		t.Fatalf("inline attachment: %v", fake.attachments[1])
	}
}

// 轮询的起点和游标都来自服务端时间：Mac 时钟比 Diana 慢一小时也不漏；同一毫秒的
// 两条都要拿到；重启后停机期间的消息补上，已经处理过的不再重复。
func TestIMessagePollingUsesServerCursorAcrossRestart(t *testing.T) {
	isolateIMessageState(t)
	fake, server := newFakeBlueBubbles(t, false)
	macNow := time.Now().Add(-time.Hour).UnixMilli()
	fake.addMessage(imessageWebhookMessage(map[string]any{"guid": "history", "dateCreated": macNow - 5000}))

	newPoller := func() (*IMessageChannel, chan MessageEvent) {
		channel := NewIMessageChannel(testIMessageConfig(server))
		events := make(chan MessageEvent, 16)
		channel.handler = func(_ context.Context, event MessageEvent) error {
			events <- event
			return nil
		}
		return channel, events
	}
	channel, events := newPoller()
	if err := channel.pollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 第一次启用只定起点，历史消息不回。
	expectNoIMessageEvent(t, events)
	if cursor := channel.stateStore().cursor(); cursor != macNow-5000 {
		t.Fatalf("cursor should start at the server's newest message, got %d", cursor)
	}

	fake.addMessage(imessageWebhookMessage(map[string]any{"guid": "same-ms-a", "text": "一", "dateCreated": macNow}))
	fake.addMessage(imessageWebhookMessage(map[string]any{"guid": "same-ms-b", "text": "二", "dateCreated": macNow}))
	if err := channel.pollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := []string{waitIMessageEvent(t, events).MessageID, waitIMessageEvent(t, events).MessageID}
	if got[0] != "same-ms-a" || got[1] != "same-ms-b" {
		t.Fatalf("same-millisecond messages = %v", got)
	}
	// >= 会把游标那一毫秒的两条再拉一遍，按 guid 去重。
	if err := channel.pollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	expectNoIMessageEvent(t, events)

	// 停机期间来的消息，重启后要补上。
	fake.addMessage(imessageWebhookMessage(map[string]any{"guid": "while-down", "text": "在吗", "dateCreated": macNow + 60000}))
	restarted, restartedEvents := newPoller()
	if err := restarted.pollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if event := waitIMessageEvent(t, restartedEvents); event.MessageID != "while-down" {
		t.Fatalf("missed message after restart: %+v", event)
	}
	expectNoIMessageEvent(t, restartedEvents)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	last := fake.queryBodies[len(fake.queryBodies)-1]
	if last["sort"] != "ASC" || last["after"].(float64) != float64(macNow) {
		t.Fatalf("poll query should resume from the persisted cursor: %v", last)
	}
}

func TestIMessagePollingFailuresMarkChannelDisconnected(t *testing.T) {
	isolateIMessageState(t)
	fake, server := newFakeBlueBubbles(t, false)
	channel := NewIMessageChannel(testIMessageConfig(server))
	channel.setStatus(true, "diana@icloud.com", "")
	fake.queryFail = true

	failures := 0
	for attempt := 1; attempt < imessagePollFailureLimit; attempt++ {
		failures = channel.notePollResult(failures, channel.pollOnce(context.Background()))
		if !channel.Status().Connected || !strings.Contains(channel.Status().LastError, "Database locked") {
			t.Fatalf("a single failure should only record the reason: %+v", channel.Status())
		}
	}
	failures = channel.notePollResult(failures, channel.pollOnce(context.Background()))
	status := channel.Status()
	if status.Connected || !strings.Contains(status.LastError, "连续失败") {
		t.Fatalf("repeated failures should mark the channel down: %+v", status)
	}

	fake.mu.Lock()
	fake.queryFail = false
	fake.mu.Unlock()
	if failures = channel.notePollResult(failures, channel.pollOnce(context.Background())); failures != 0 {
		t.Fatalf("failure count after recovery = %d", failures)
	}
	if status := channel.Status(); !status.Connected || status.LastError != "" {
		t.Fatalf("recovery should restore the status: %+v", status)
	}
	if channel.notePollResult(0, errors.New("x")) != 1 {
		t.Fatal("failure counter should restart from zero")
	}
}

// 首次保存就生成独立的 webhook token；手填的太短会被拒绝。
func TestIMessageConfigGeneratesWebhookToken(t *testing.T) {
	cfg := ConfigFromPayload(ConfigPayload{Platform: PlatformIMessage, IMessageServerURL: "http://mac.local:1234", IMessagePassword: "p"}, DefaultBotConfig())
	if len(cfg.IMessageWebhookToken) < imessageMinWebhookTokenLength || cfg.IMessageWebhookToken == cfg.IMessagePassword {
		t.Fatalf("webhook token = %q", cfg.IMessageWebhookToken)
	}
	again := ConfigFromPayload(ConfigPayload{Platform: PlatformIMessage, Name: "改名"}, cfg)
	if again.IMessageWebhookToken != cfg.IMessageWebhookToken {
		t.Fatal("saving again must keep the existing token")
	}
	if other := ConfigFromPayload(ConfigPayload{Platform: PlatformTelegram}, DefaultBotConfig()); other.IMessageWebhookToken != "" {
		t.Fatal("other platforms should not get an iMessage token")
	}
	weak := BotConfig{Platform: PlatformIMessage, Enabled: true, IMessageServerURL: "http://mac.local:1234", IMessagePassword: "p", IMessageWebhookToken: "short"}
	if err := weak.WithDefaults().Validate(); !errors.Is(err, ErrWeakIMessageWebhookToken) {
		t.Fatalf("Validate() = %v", err)
	}
}

// iMessage 的账号是手机号，既不是纯数字 QQ 号也不含字母；隐私代理必须照样换成别名。
func TestIdentityPrivacyAliasesPhoneAndEmailHandles(t *testing.T) {
	scope := newIdentityPrivacyScopeWithSalt("salt")
	scope.registerEvent(MessageEvent{UserID: "+8613800000000", GroupID: "iMessage;+;chat123456"})
	scope.registerEvent(MessageEvent{UserID: "friend@example.com"})
	text := scope.protectText("发送者 +8613800000000，另一位 friend@example.com，群 iMessage;+;chat123456")
	for _, real := range []string{"+8613800000000", "8613800000000", "friend@example.com", "chat123456"} {
		if strings.Contains(text, real) {
			t.Fatalf("%q leaked: %s", real, text)
		}
	}
	if restored := scope.restoreText(text); !strings.Contains(restored, "+8613800000000") || !strings.Contains(restored, "friend@example.com") {
		t.Fatalf("aliases should restore: %s", restored)
	}
}
