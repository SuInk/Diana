// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBlueBubbles 模拟 BlueBubbles Server 的 REST 接口，字段形状照 bluebubbles-server
// 的 MessageSerializer / GeneralInterface.getServerMetadata。
type fakeBlueBubbles struct {
	t          *testing.T
	password   string
	privateAPI bool

	mu          sync.Mutex
	textSends   []map[string]any
	attachments []map[string]string
	queries     int
	pollBatch   []map[string]any
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
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/attachment/att-1/download":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("\xff\xd8\xff\xe0fake-jpeg"))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/message/query":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["sort"] != "ASC" || body["after"] == nil {
				f.t.Errorf("unexpected query body: %v", body)
			}
			f.mu.Lock()
			f.queries++
			batch := f.pollBatch
			f.pollBatch = nil
			f.mu.Unlock()
			if batch == nil {
				batch = []map[string]any{}
			}
			ok(batch)
		default:
			http.NotFound(w, r)
		}
	})
}

func newFakeBlueBubbles(t *testing.T, privateAPI bool) (*fakeBlueBubbles, *httptest.Server) {
	t.Helper()
	fake := &fakeBlueBubbles{t: t, password: "bb-secret", privateAPI: privateAPI}
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

func decodeIMessage(t *testing.T, payload map[string]any) imessageMessage {
	t.Helper()
	raw, _ := json.Marshal(payload)
	var message imessageMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatal(err)
	}
	return message
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
		"chats": []any{map[string]any{
			"guid": "iMessage;+;chat123456", "chatIdentifier": "chat123456", "displayName": "周末爬山", "style": 43,
		}},
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
	events := make(chan MessageEvent, 8)
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

func TestIMessageWebhookRequiresTokenAndDeduplicates(t *testing.T) {
	_, server := newFakeBlueBubbles(t, false)
	channel, events := startIMessageChannel(t, IMessageConfig{
		ProfileID: "bot-imessage", ServerURL: server.URL, Password: "bb-secret", WebhookToken: "hook-token",
	})
	if channel.Status().SelfID != "diana@icloud.com" {
		t.Fatalf("self id should come from server/info: %+v", channel.Status())
	}
	webhook := map[string]any{"type": "new-message", "data": imessageWebhookMessage(nil)}

	// webhook 地址是公网的，BlueBubbles 又不签名：不带 token 或只带服务器密码都得拒。
	for _, query := range []string{"", "?token=nope", "?password=bb-secret"} {
		if code := postIMessageWebhook(t, query, webhook); code != http.StatusUnauthorized {
			t.Fatalf("query %q: status %d, want 401", query, code)
		}
	}
	if code := postIMessageWebhook(t, "?token=hook-token", webhook); code != http.StatusOK {
		t.Fatalf("valid webhook status %d", code)
	}
	event := waitIMessageEvent(t, events)
	if event.UserID != "+8613800000000" || event.RawMessage != "你好" {
		t.Fatalf("webhook event: %+v", event)
	}
	// 同一条消息重推（或轮询又拉到一次）不能回答两遍。
	postIMessageWebhook(t, "?token=hook-token", webhook)
	postIMessageWebhook(t, "?token=hook-token", map[string]any{"type": "updated-message", "data": imessageWebhookMessage(map[string]any{"guid": "msg-9"})})
	select {
	case extra := <-events:
		t.Fatalf("duplicate or non-message event delivered: %+v", extra)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestIMessageWebhookFallsBackToPasswordWithoutDedicatedToken(t *testing.T) {
	_, server := newFakeBlueBubbles(t, false)
	_, events := startIMessageChannel(t, IMessageConfig{ProfileID: "bot-imessage", ServerURL: server.URL, Password: "bb-secret"})
	if code := postIMessageWebhook(t, "?token=bb-secret", map[string]any{"type": "new-message", "data": imessageWebhookMessage(nil)}); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	waitIMessageEvent(t, events)
}

func TestIMessageWebhookDownloadsAttachments(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	_, server := newFakeBlueBubbles(t, false)
	_, events := startIMessageChannel(t, IMessageConfig{ProfileID: "bot-imessage", ServerURL: server.URL, Password: "bb-secret"})
	message := imessageWebhookMessage(map[string]any{
		"text":        "￼",
		"attachments": []any{map[string]any{"guid": "att-1", "mimeType": "image/jpeg", "transferName": "IMG_0001.jpeg", "totalBytes": 13}},
	})
	postIMessageWebhook(t, "?token=bb-secret", map[string]any{"type": "new-message", "data": message})
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
	if err != nil || !strings.HasSuffix(string(body), "fake-jpeg") {
		t.Fatalf("cached attachment: %q %v", body, err)
	}
	for _, value := range image.Data {
		if strings.Contains(value, "bb-secret") {
			t.Fatalf("server password leaked into the segment: %+v", image.Data)
		}
	}
}

func TestIMessageSendTextUsesCachedChatAndAppleScript(t *testing.T) {
	fake, server := newFakeBlueBubbles(t, false)
	channel, events := startIMessageChannel(t, IMessageConfig{ProfileID: "bot-imessage", ServerURL: server.URL, Password: "bb-secret"})
	// 这个号码之前是走 SMS 发来的，回复要回到同一个会话。
	sms := imessageWebhookMessage(map[string]any{"chats": []any{map[string]any{"guid": "SMS;-;+8613800000000"}}})
	postIMessageWebhook(t, "?token=bb-secret", map[string]any{"type": "new-message", "data": sms})
	waitIMessageEvent(t, events)

	result, err := channel.SendWithResult(context.Background(), OutgoingMessage{UserID: "+8613800000000", Text: "收到", ReplyMessageID: "msg-1"})
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

func TestIMessageSendReplyAndGroupWithPrivateAPI(t *testing.T) {
	fake, server := newFakeBlueBubbles(t, true)
	channel, _ := startIMessageChannel(t, IMessageConfig{ProfileID: "bot-imessage", ServerURL: server.URL, Password: "bb-secret"})
	if err := channel.Send(context.Background(), OutgoingMessage{GroupID: "iMessage;+;chat123456", UserID: "+8613800000000", Text: "好的", ReplyMessageID: "msg-2"}); err != nil {
		t.Fatal(err)
	}
	if err := channel.Send(context.Background(), OutgoingMessage{UserID: "someone@example.com", Text: "嗨"}); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	reply := fake.textSends[0]
	if reply["chatGuid"] != "iMessage;+;chat123456" || reply["selectedMessageGuid"] != "msg-2" || reply["method"] != "private-api" {
		t.Fatalf("reply body: %v", reply)
	}
	if fake.textSends[1]["chatGuid"] != "iMessage;-;someone@example.com" {
		t.Fatalf("unknown direct chat guid: %v", fake.textSends[1])
	}
}

func TestIMessageSendAttachmentUploadsLocalFile(t *testing.T) {
	fake, server := newFakeBlueBubbles(t, false)
	channel, _ := startIMessageChannel(t, IMessageConfig{ProfileID: "bot-imessage", ServerURL: server.URL, Password: "bb-secret"})
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

func TestIMessagePollingDeliversNewMessages(t *testing.T) {
	fake, server := newFakeBlueBubbles(t, false)
	fake.pollBatch = []map[string]any{imessageWebhookMessage(map[string]any{"guid": "polled-1", "dateCreated": time.Now().UnixMilli() + 1000})}
	channel := NewIMessageChannel(IMessageConfig{ProfileID: "bot-imessage", ServerURL: server.URL, Password: "bb-secret", PollSeconds: 5})
	events := make(chan MessageEvent, 2)
	channel.handler = func(_ context.Context, event MessageEvent) error {
		events <- event
		return nil
	}
	messages, err := channel.queryMessagesAfter(context.Background(), 1)
	if err != nil || len(messages) != 1 {
		t.Fatalf("query: %v %v", messages, err)
	}
	channel.dispatch(context.Background(), messages[0])
	if event := waitIMessageEvent(t, events); event.MessageID != "polled-1" {
		t.Fatalf("polled event: %+v", event)
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
