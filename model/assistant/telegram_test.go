package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTelegramAPI 是一个最小的 Bot API 桩，按方法名返回预置结果并记录调用。
type fakeTelegramAPI struct {
	mu      sync.Mutex
	calls   []telegramCall
	replies map[string]any
	server  *httptest.Server
}

type telegramCall struct {
	Method string
	Params map[string]any
}

func newFakeTelegramAPI(t *testing.T, replies map[string]any) *fakeTelegramAPI {
	t.Helper()
	api := &fakeTelegramAPI{replies: replies}
	api.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 路径形如 /bot<token>/<method>
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		method := parts[len(parts)-1]
		params := map[string]any{}
		if r.Header.Get("Content-Type") == "application/json" {
			_ = json.NewDecoder(r.Body).Decode(&params)
		}
		api.mu.Lock()
		api.calls = append(api.calls, telegramCall{Method: method, Params: params})
		result, ok := api.replies[method]
		api.mu.Unlock()
		if !ok {
			result = map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}))
	t.Cleanup(api.server.Close)
	return api
}

func (a *fakeTelegramAPI) channel() *TelegramChannel {
	return NewTelegramChannel(TelegramConfig{BotToken: "test-token", APIBaseURL: a.server.URL})
}

func (a *fakeTelegramAPI) callsOf(method string) []telegramCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []telegramCall{}
	for _, call := range a.calls {
		if call.Method == method {
			out = append(out, call)
		}
	}
	return out
}

func TestTelegramSendsTextToChat(t *testing.T) {
	api := newFakeTelegramAPI(t, nil)
	ch := api.channel()

	if err := ch.Send(context.Background(), OutgoingMessage{GroupID: "-100123", Text: "你好"}); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	calls := api.callsOf("sendMessage")
	if len(calls) != 1 {
		t.Fatalf("期望 1 次 sendMessage，实际 %d", len(calls))
	}
	if calls[0].Params["chat_id"] != "-100123" {
		t.Fatalf("chat_id 错误：%v", calls[0].Params["chat_id"])
	}
	if calls[0].Params["text"] != "你好" {
		t.Fatalf("text 错误：%v", calls[0].Params["text"])
	}
}

// 私聊没有 GroupID，要回落到 UserID 当 chat_id。
func TestTelegramFallsBackToUserIDForPrivateChat(t *testing.T) {
	api := newFakeTelegramAPI(t, nil)
	if err := api.channel().Send(context.Background(), OutgoingMessage{UserID: "555", Text: "hi"}); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	calls := api.callsOf("sendMessage")
	if len(calls) != 1 || calls[0].Params["chat_id"] != "555" {
		t.Fatalf("私聊应使用 user id 作为 chat_id，实际 %+v", calls)
	}
}

// 被回复的消息可能已删除，必须允许降级为普通发送，否则整条回复会丢。
func TestTelegramReplyAllowsMissingTarget(t *testing.T) {
	api := newFakeTelegramAPI(t, nil)
	err := api.channel().Send(context.Background(), OutgoingMessage{GroupID: "1", Text: "x", ReplyMessageID: "42"})
	if err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	params := api.callsOf("sendMessage")[0].Params
	if params["reply_to_message_id"] != "42" {
		t.Fatalf("reply_to_message_id 错误：%v", params["reply_to_message_id"])
	}
	if params["allow_sending_without_reply"] != true {
		t.Fatal("被回复消息不存在时应仍然发送")
	}
}

func TestTelegramSendsMediaByURL(t *testing.T) {
	api := newFakeTelegramAPI(t, nil)
	err := api.channel().Send(context.Background(), OutgoingMessage{
		GroupID:   "1",
		ImageURLs: []string{"https://example.com/a.jpg"},
		VideoURLs: []string{"https://example.com/b.mp4"},
	})
	if err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	if photos := api.callsOf("sendPhoto"); len(photos) != 1 || photos[0].Params["photo"] != "https://example.com/a.jpg" {
		t.Fatalf("图片发送错误：%+v", photos)
	}
	if videos := api.callsOf("sendVideo"); len(videos) != 1 || videos[0].Params["video"] != "https://example.com/b.mp4" {
		t.Fatalf("视频发送错误：%+v", videos)
	}
}

// 空文本不该产生一次空的 sendMessage 调用。
func TestTelegramSkipsEmptyText(t *testing.T) {
	api := newFakeTelegramAPI(t, nil)
	if err := api.channel().Send(context.Background(), OutgoingMessage{GroupID: "1", Text: "   "}); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	if calls := api.callsOf("sendMessage"); len(calls) != 0 {
		t.Fatalf("空文本不该发送，实际 %d 次", len(calls))
	}
}

func TestTelegramRequiresChatID(t *testing.T) {
	api := newFakeTelegramAPI(t, nil)
	if err := api.channel().Send(context.Background(), OutgoingMessage{Text: "x"}); err == nil {
		t.Fatal("缺少 chat id 时应报错")
	}
}

func TestTelegramSurfacesAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": false, "error_code": 401, "description": "Unauthorized",
		})
	}))
	defer server.Close()

	ch := NewTelegramChannel(TelegramConfig{BotToken: "bad", APIBaseURL: server.URL})
	_, err := ch.CallAPI(context.Background(), "getMe", nil)
	if err == nil || !strings.Contains(err.Error(), "Unauthorized") {
		t.Fatalf("应透出 Bot API 的错误描述，实际：%v", err)
	}
}

func TestTelegramConnectRequiresToken(t *testing.T) {
	ch := NewTelegramChannel(TelegramConfig{})
	err := ch.Connect(context.Background(), func(context.Context, MessageEvent) error { return nil })
	if err == nil {
		t.Fatal("没有 token 时 Connect 应失败")
	}
	if ch.Status().Connected {
		t.Fatal("失败后不该标记为已连接")
	}
}

// —— 事件映射 ——

func TestTelegramGroupMessageMapping(t *testing.T) {
	msg := &telegramMessage{
		MessageID: 7,
		Date:      1700000000,
		Text:      "@diana_bot 你好",
		From:      &telegramUser{ID: 42, FirstName: "小", LastName: "明"},
		Chat:      &telegramChat{ID: -100999, Type: "supergroup", Title: "测试群"},
		Entities:  []telegramEntity{{Type: "mention", Offset: 0, Length: 10}},
	}
	event := telegramMessageToEvent(msg, "8888", "diana_bot")

	if event.Kind != EventKindGroup {
		t.Fatalf("应识别为群聊，实际 %q", event.Kind)
	}
	if event.GroupID != "-100999" {
		t.Fatalf("GroupID 错误：%q", event.GroupID)
	}
	if event.UserID != "42" {
		t.Fatalf("UserID 错误：%q", event.UserID)
	}
	if event.SenderName != "小 明" {
		t.Fatalf("SenderName 错误：%q", event.SenderName)
	}
	if event.MessageID != "7" {
		t.Fatalf("MessageID 错误：%q", event.MessageID)
	}
	if !event.ToMe {
		t.Fatal("带 mention 实体应视为 @机器人")
	}
	// Telegram 没有 QQ 群等级，等级门槛会按「读不到即放行」处理。
	if event.SenderLevel != 0 {
		t.Fatalf("Telegram 不应有群等级，实际 %d", event.SenderLevel)
	}
}

func TestTelegramPrivateMessageAlwaysToMe(t *testing.T) {
	msg := &telegramMessage{
		MessageID: 1,
		Text:      "在吗",
		From:      &telegramUser{ID: 5, Username: "someone"},
		Chat:      &telegramChat{ID: 5, Type: "private"},
	}
	event := telegramMessageToEvent(msg, "8888", "diana_bot")
	if event.Kind != EventKindPrivate {
		t.Fatalf("应识别为私聊，实际 %q", event.Kind)
	}
	if event.GroupID != "" {
		t.Fatalf("私聊不该有 GroupID，实际 %q", event.GroupID)
	}
	if !event.ToMe {
		t.Fatal("私聊天然是对机器人说的")
	}
	if event.SenderName != "someone" {
		t.Fatalf("没有姓名时应回落 username，实际 %q", event.SenderName)
	}
}

func TestTelegramUsesCaptionWhenTextEmpty(t *testing.T) {
	msg := &telegramMessage{
		MessageID: 2,
		Caption:   "图片说明",
		From:      &telegramUser{ID: 5},
		Chat:      &telegramChat{ID: 5, Type: "private"},
	}
	event := telegramMessageToEvent(msg, "", "diana_bot")
	if event.RawMessage != "图片说明" {
		t.Fatalf("纯图片消息应取 caption，实际 %q", event.RawMessage)
	}
}

func TestTelegramIgnoresUnknownChatType(t *testing.T) {
	msg := &telegramMessage{Chat: &telegramChat{ID: 1, Type: "unknown"}}
	if event := telegramMessageToEvent(msg, "", "diana_bot"); event.Kind != "" {
		t.Fatalf("未知会话类型应被忽略，实际 %q", event.Kind)
	}
	if event := telegramMessageToEvent(nil, "", "diana_bot"); event.Kind != "" {
		t.Fatal("nil 消息应被忽略")
	}
}

// getUpdates 的 offset 必须推进，否则同一条消息会被反复投递。
func TestTelegramAdvancesUpdateOffset(t *testing.T) {
	api := newFakeTelegramAPI(t, map[string]any{
		"getUpdates": []map[string]any{
			{"update_id": 100, "message": map[string]any{
				"message_id": 1, "text": "a",
				"chat": map[string]any{"id": 5, "type": "private"},
				"from": map[string]any{"id": 5},
			}},
			{"update_id": 101, "message": map[string]any{
				"message_id": 2, "text": "b",
				"chat": map[string]any{"id": 5, "type": "private"},
				"from": map[string]any{"id": 5},
			}},
		},
	})
	ch := api.channel()

	if _, err := ch.fetchUpdates(context.Background()); err != nil {
		t.Fatalf("首次拉取失败：%v", err)
	}
	if _, err := ch.fetchUpdates(context.Background()); err != nil {
		t.Fatalf("二次拉取失败：%v", err)
	}

	calls := api.callsOf("getUpdates")
	if len(calls) != 2 {
		t.Fatalf("期望 2 次 getUpdates，实际 %d", len(calls))
	}
	if _, ok := calls[0].Params["offset"]; ok {
		t.Fatal("首次拉取不该带 offset")
	}
	if got := calls[1].Params["offset"]; got != float64(102) {
		t.Fatalf("offset 应推进到 102，实际 %v", got)
	}
}

func TestTelegramDispatchesUpdatesToHandler(t *testing.T) {
	api := newFakeTelegramAPI(t, map[string]any{
		"getUpdates": []map[string]any{
			{"update_id": 1, "message": map[string]any{
				"message_id": 9, "text": "hello",
				"chat": map[string]any{"id": 77, "type": "private"},
				"from": map[string]any{"id": 77, "first_name": "A"},
			}},
		},
	})
	ch := api.channel()

	var got []MessageEvent
	ch.handler = func(_ context.Context, event MessageEvent) error {
		got = append(got, event)
		return nil
	}
	updates, err := ch.fetchUpdates(context.Background())
	if err != nil {
		t.Fatalf("拉取失败：%v", err)
	}
	for _, update := range updates {
		ch.dispatch(context.Background(), update)
	}
	if len(got) != 1 || got[0].RawMessage != "hello" || got[0].UserID != "77" {
		t.Fatalf("事件投递错误：%+v", got)
	}
}

func TestTelegramLocalPathDetection(t *testing.T) {
	if telegramLocalPath("https://example.com/a.jpg") != "" {
		t.Fatal("远程 URL 不该当成本地文件")
	}
	if got := telegramLocalPath("/tmp/a.mp4"); got != "/tmp/a.mp4" {
		t.Fatalf("绝对路径应识别为本地文件，实际 %q", got)
	}
	if got := telegramLocalPath("file:///tmp/b.mp4"); got != "/tmp/b.mp4" {
		t.Fatalf("file:// 应剥掉前缀，实际 %q", got)
	}
}

func TestTelegramStatusReflectsConfig(t *testing.T) {
	ch := NewTelegramChannel(TelegramConfig{BotToken: "t"})
	if !strings.Contains(ch.Status().Endpoint, telegramDefaultAPIBase) {
		t.Fatalf("未配置时应用官方地址，实际 %q", ch.Status().Endpoint)
	}
	ch.SetConfig(TelegramConfig{BotToken: "t", APIBaseURL: "https://tg.example.com"})
	if !strings.Contains(ch.Status().Endpoint, "tg.example.com") {
		t.Fatalf("自建地址未生效：%q", ch.Status().Endpoint)
	}
	_ = ch.Close()
	if ch.Status().Connected {
		t.Fatal("Close 后不该是已连接")
	}
}

func TestTelegramCloseStopsPolling(t *testing.T) {
	api := newFakeTelegramAPI(t, map[string]any{
		"getMe":      map[string]any{"id": 4242},
		"getUpdates": []map[string]any{},
	})
	ch := api.channel()

	done := make(chan error, 1)
	go func() {
		done <- ch.Connect(context.Background(), func(context.Context, MessageEvent) error { return nil })
	}()

	// 等 getMe 完成，说明已经进入轮询。
	deadline := time.After(3 * time.Second)
	for len(api.callsOf("getMe")) == 0 {
		select {
		case <-deadline:
			t.Fatal("等待 getMe 超时")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if ch.Status().SelfID != "4242" {
		t.Fatalf("应从 getMe 取得 self id，实际 %q", ch.Status().SelfID)
	}

	_ = ch.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close 后轮询未退出")
	}
}

// —— @提及 精确匹配 ——

// 只要看到「有 @ 就算」的话，群里任何带邮箱或 @别人的消息都会误触发。
func TestTelegramMentionMustMatchBotUsername(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		entities []telegramEntity
		want     bool
	}{
		{"@本机器人", "@diana_bot 在吗", []telegramEntity{{Type: "mention", Offset: 0, Length: 10}}, true},
		{"@别人", "@someone_else 在吗", []telegramEntity{{Type: "mention", Offset: 0, Length: 13}}, false},
		{"纯邮箱不算", "联系 a@b.com", nil, false},
		{"文本里有@但无实体", "价格 @ 100", nil, false},
		{"大小写不敏感", "@Diana_Bot hi", []telegramEntity{{Type: "mention", Offset: 0, Length: 10}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := telegramMentionsBot(tc.text, tc.entities, "8888", "diana_bot")
			if got != tc.want {
				t.Fatalf("%q 期望 %v，实际 %v", tc.text, tc.want, got)
			}
		})
	}
}

// entity 的 offset/length 以 UTF-16 码元计，中文前缀不能按字节切。
func TestTelegramMentionHandlesUTF16Offsets(t *testing.T) {
	text := "你好呀 @diana_bot"
	// "你好呀 " 是 4 个 UTF-16 码元，@diana_bot 是 10 个。
	entities := []telegramEntity{{Type: "mention", Offset: 4, Length: 10}}
	if !telegramMentionsBot(text, entities, "8888", "diana_bot") {
		t.Fatal("中文前缀后的 @提及 应能正确识别")
	}
}

func TestTelegramTextMentionMatchesBySelfID(t *testing.T) {
	entities := []telegramEntity{{Type: "text_mention", Offset: 0, Length: 2, User: &telegramUser{ID: 8888}}}
	if !telegramMentionsBot("机器人 hi", entities, "8888", "") {
		t.Fatal("text_mention 应按用户 ID 匹配")
	}
	other := []telegramEntity{{Type: "text_mention", Offset: 0, Length: 2, User: &telegramUser{ID: 9999}}}
	if telegramMentionsBot("别人 hi", other, "8888", "") {
		t.Fatal("text_mention 指向别人时不该触发")
	}
}

// —— 入群通知 ——

// 不映射 new_chat_members 的话，欢迎语在 Telegram 上永远不触发。
func TestTelegramMapsNewChatMemberToNotice(t *testing.T) {
	msg := &telegramMessage{
		MessageID:      9,
		Date:           1700000000,
		Chat:           &telegramChat{ID: -100777, Type: "supergroup"},
		From:           &telegramUser{ID: 1, FirstName: "邀请人"},
		NewChatMembers: []telegramUser{{ID: 4242, FirstName: "新", LastName: "人"}},
	}
	event := telegramMessageToEvent(msg, "8888", "diana_bot")

	if event.Kind != EventKindNotice {
		t.Fatalf("应映射为 notice，实际 %q", event.Kind)
	}
	if event.SubType != "group_increase" {
		t.Fatalf("SubType 应为 group_increase，实际 %q", event.SubType)
	}
	if event.GroupID != "-100777" {
		t.Fatalf("GroupID 错误：%q", event.GroupID)
	}
	// UserID 必须是新加入的人，不是发出通知的人。
	if event.UserID != "4242" {
		t.Fatalf("UserID 应为新成员，实际 %q", event.UserID)
	}
	if event.SenderName != "新 人" {
		t.Fatalf("SenderName 错误：%q", event.SenderName)
	}
}

func TestTelegramDisplayNameFallsBackToUsername(t *testing.T) {
	if got := telegramDisplayName(&telegramUser{Username: "nick"}); got != "nick" {
		t.Fatalf("无姓名时应回落 username，实际 %q", got)
	}
	if got := telegramDisplayName(nil); got != "" {
		t.Fatalf("nil 用户应返回空串，实际 %q", got)
	}
}
