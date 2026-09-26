// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// 这组用例用 httptest 模拟 iLink 服务端，不连腾讯。报文形状照着
// @tencent-weixin/openclaw-weixin 2.4.9 的 src/api/types.ts。

type weixinFakeServer struct {
	t      *testing.T
	server *httptest.Server

	mu sync.Mutex
	// updates 依次作为 getupdates 的响应；取完后挂住直到请求超时，模拟长轮询。
	updates  []string
	cursors  []string
	sends    []map[string]any
	uploads  []map[string]any
	headers  []http.Header
	cdnBody  []byte
	uploaded [][]byte
}

func newWeixinFakeServer(t *testing.T) *weixinFakeServer {
	t.Helper()
	fake := &weixinFakeServer{t: t}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *weixinFakeServer) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.headers = append(f.headers, r.Header.Clone())
	f.mu.Unlock()
	switch r.URL.Path {
	case "/ilink/bot/getupdates":
		f.mu.Lock()
		f.cursors = append(f.cursors, stringFromAny(body["get_updates_buf"]))
		var next string
		if len(f.updates) > 0 {
			next, f.updates = f.updates[0], f.updates[1:]
		}
		f.mu.Unlock()
		if next == "" {
			// 没有新消息时像真的长轮询一样挂着，直到客户端放弃。
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte(next))
	case "/ilink/bot/sendmessage":
		f.mu.Lock()
		f.sends = append(f.sends, body)
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"ret":0,"message_id":9007199254740993}`))
	case "/ilink/bot/getuploadurl":
		f.mu.Lock()
		f.uploads = append(f.uploads, body)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"upload_full_url": f.server.URL + "/cdn/upload?x=1"})
	case "/cdn/upload":
		f.mu.Lock()
		f.uploaded = append(f.uploaded, raw)
		f.mu.Unlock()
		w.Header().Set("x-encrypted-param", "download-param-1")
		w.WriteHeader(http.StatusOK)
	case "/cdn/download":
		f.mu.Lock()
		body := f.cdnBody
		f.mu.Unlock()
		_, _ = w.Write(body)
	case "/ilink/bot/msg/notifystart", "/ilink/bot/msg/notifystop":
		_, _ = w.Write([]byte(`{"ret":0}`))
	default:
		http.NotFound(w, r)
	}
}

func (f *weixinFakeServer) push(responses ...string) {
	f.mu.Lock()
	f.updates = append(f.updates, responses...)
	f.mu.Unlock()
}

func (f *weixinFakeServer) sent() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.sends...)
}

func newTestWeixinChannel(fake *weixinFakeServer, stateDir string) *WeixinChannel {
	channel := NewWeixinChannel(WeixinConfig{
		ProfileID: "wx-profile", BotToken: "bot-token", BotID: "bot@im.bot",
		BaseURL: fake.server.URL, CDNBaseURL: fake.server.URL + "/cdn", StateDir: stateDir,
	})
	channel.retryInitial = 10 * time.Millisecond
	channel.retryMax = 20 * time.Millisecond
	return channel
}

func waitWeixin(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func runWeixin(t *testing.T, channel *WeixinChannel, handler EventHandler) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = channel.Connect(ctx, handler)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Connect did not return after cancel")
		}
	})
	return cancel
}

func TestWeixinClientVersionMatchesOfficialEncoding(t *testing.T) {
	// 官方注释里的例子：1.0.11 -> 0x0001000B。
	if got := weixinClientVersion("1.0.11"); got != "65547" {
		t.Fatalf("1.0.11 encoded as %s, want 65547", got)
	}
	if got := weixinClientVersion("2.4.9"); got != strconv.Itoa(0x020409) {
		t.Fatalf("2.4.9 encoded as %s", got)
	}
}

func TestWeixinReceivesTextAndRepliesWithContextToken(t *testing.T) {
	fake := newWeixinFakeServer(t)
	// message_id 超过 2^53，按 float64 解码会丢精度。
	fake.push(`{"ret":0,"get_updates_buf":"cursor-1","msgs":[{"message_id":7234567890123456789,"from_user_id":"alice@im.wechat",` +
		`"to_user_id":"bot@im.bot","create_time_ms":1700000000123,"message_type":1,"context_token":"ctx-alice",` +
		`"item_list":[{"type":1,"text_item":{"text":" 你好 "}}]}]}`)
	dir := t.TempDir()
	channel := newTestWeixinChannel(fake, dir)
	events := make(chan MessageEvent, 2)
	runWeixin(t, channel, func(_ context.Context, event MessageEvent) error {
		events <- event
		return nil
	})

	var event MessageEvent
	select {
	case event = <-events:
	case <-time.After(3 * time.Second):
		t.Fatal("inbound message was not delivered")
	}
	if event.Kind != EventKindPrivate || event.UserID != "alice@im.wechat" || event.RawMessage != "你好" || !event.ToMe {
		t.Fatalf("unexpected event: %+v", event)
	}
	if event.MessageID != "7234567890123456789" {
		t.Fatalf("message id = %q, lost precision", event.MessageID)
	}
	if event.Time != 1700000000 || event.SelfID != "bot@im.bot" {
		t.Fatalf("time=%d self=%q", event.Time, event.SelfID)
	}
	waitWeixin(t, "online status", func() bool { return channel.Status().Connected })

	if err := channel.Send(context.Background(), OutgoingMessage{UserID: "alice@im.wechat", Text: "收到"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	sends := fake.sent()
	if len(sends) != 1 {
		t.Fatalf("sendmessage called %d times", len(sends))
	}
	msg, _ := sends[0]["msg"].(map[string]any)
	if msg["to_user_id"] != "alice@im.wechat" || msg["context_token"] != "ctx-alice" {
		t.Fatalf("reply lost its routing: %+v", msg)
	}
	if msg["message_type"] != float64(weixinMessageTypeBot) || msg["message_state"] != float64(weixinMessageStateEnd) {
		t.Fatalf("reply type/state = %v/%v", msg["message_type"], msg["message_state"])
	}
	if _, ok := msg["from_user_id"]; !ok {
		t.Fatal("from_user_id must be present (empty) like the official client sends it")
	}
	items, _ := msg["item_list"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["text_item"].(map[string]any)["text"] != "收到" {
		t.Fatalf("item_list = %+v", items)
	}
	if info, _ := sends[0]["base_info"].(map[string]any); !strings.HasPrefix(stringFromAny(info["bot_agent"]), "Diana/") {
		t.Fatalf("base_info = %+v, want Diana to identify itself", sends[0]["base_info"])
	}

	// 请求头照官方实现带齐。
	fake.mu.Lock()
	var header http.Header
	for _, h := range fake.headers {
		if h.Get("Authorization") != "" {
			header = h
			break
		}
	}
	fake.mu.Unlock()
	if header.Get("Authorization") != "Bearer bot-token" || header.Get("AuthorizationType") != "ilink_bot_token" ||
		header.Get("iLink-App-Id") != "bot" || header.Get("iLink-App-ClientVersion") == "" {
		t.Fatalf("missing protocol headers: %v", header)
	}
	uin, err := base64.StdEncoding.DecodeString(header.Get("X-WECHAT-UIN"))
	if err != nil {
		t.Fatalf("X-WECHAT-UIN is not base64: %v", err)
	}
	if _, err := strconv.ParseUint(string(uin), 10, 32); err != nil {
		t.Fatalf("X-WECHAT-UIN should wrap a decimal uint32, got %q", uin)
	}

	// 游标和 context_token 落盘，重启后接着用。
	waitWeixin(t, "cursor persisted", func() bool {
		raw, err := os.ReadFile(channel.statePath())
		return err == nil && strings.Contains(string(raw), "cursor-1") && strings.Contains(string(raw), "ctx-alice")
	})
}

func TestWeixinResumesCursorAfterRestart(t *testing.T) {
	fake := newWeixinFakeServer(t)
	dir := t.TempDir()
	first := newTestWeixinChannel(fake, dir)
	first.state = weixinState{Cursor: "saved-cursor", ContextTokens: map[string]string{"bob@im.wechat": "ctx-bob"}}
	first.saveState()

	second := newTestWeixinChannel(fake, dir)
	runWeixin(t, second, func(context.Context, MessageEvent) error { return nil })
	waitWeixin(t, "first poll", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.cursors) > 0
	})
	fake.mu.Lock()
	cursor := fake.cursors[0]
	fake.mu.Unlock()
	if cursor != "saved-cursor" {
		t.Fatalf("first poll after restart sent cursor %q, want the persisted one", cursor)
	}
	// 重启前记下的 context_token 也要还在，否则主动发给老联系人的消息会丢上下文。
	if err := second.Send(context.Background(), OutgoingMessage{UserID: "bob@im.wechat", Text: "hi"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	msg, _ := fake.sent()[0]["msg"].(map[string]any)
	if msg["context_token"] != "ctx-bob" {
		t.Fatalf("context_token = %v after restart", msg["context_token"])
	}
}

// errcode -14 是 token 失效：停一段时间、把原因写进状态，发送也要拒绝并说明。
func TestWeixinStaleTokenPausesAndSurfacesReason(t *testing.T) {
	fake := newWeixinFakeServer(t)
	fake.push(`{"ret":-14,"errcode":-14,"errmsg":"session timeout"}`,
		`{"ret":0,"msgs":[{"message_id":1,"from_user_id":"alice@im.wechat","item_list":[{"type":1,"text_item":{"text":"back"}}]}]}`)
	channel := newTestWeixinChannel(fake, t.TempDir())
	channel.pause = 300 * time.Millisecond
	events := make(chan MessageEvent, 1)
	runWeixin(t, channel, func(_ context.Context, event MessageEvent) error {
		events <- event
		return nil
	})

	waitWeixin(t, "paused status", func() bool {
		status := channel.Status()
		return !status.Connected && strings.Contains(status.LastError, "重新扫码")
	})
	err := channel.Send(context.Background(), OutgoingMessage{UserID: "alice@im.wechat", Text: "hi"})
	if err == nil || !strings.Contains(err.Error(), "登录已失效") {
		t.Fatalf("send during pause = %v, want a clear refusal", err)
	}
	if len(fake.sent()) != 0 {
		t.Fatal("a paused session still hit sendmessage")
	}
	// 暂停结束后自己恢复，不需要人工重启。
	select {
	case event := <-events:
		if event.RawMessage != "back" {
			t.Fatalf("unexpected event after resume: %+v", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("polling did not resume after the pause")
	}
	waitWeixin(t, "recovered status", func() bool { return channel.Status().Connected && channel.Status().LastError == "" })
}

func TestWeixinBacksOffOnServerErrorAndRecovers(t *testing.T) {
	var calls int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 读完请求体，服务端才会探测到客户端断开，挂起的长轮询才能随之结束。
		_, _ = io.Copy(io.Discard, r.Body)
		if r.URL.Path != "/ilink/bot/getupdates" {
			_, _ = w.Write([]byte(`{"ret":0}`))
			return
		}
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		switch {
		case n == 1:
			http.Error(w, "bad gateway", http.StatusBadGateway)
		case n == 2:
			_, _ = w.Write([]byte(`{"ret":-2,"errcode":-2,"errmsg":"frequency limit"}`))
		default:
			<-r.Context().Done()
		}
	}))
	t.Cleanup(server.Close)
	channel := NewWeixinChannel(WeixinConfig{BotToken: "t", BotID: "b", BaseURL: server.URL, StateDir: t.TempDir()})
	channel.retryInitial = 10 * time.Millisecond
	runWeixin(t, channel, func(context.Context, MessageEvent) error { return nil })

	waitWeixin(t, "risk-control reason in status", func() bool {
		return strings.Contains(channel.Status().LastError, "frequency limit")
	})
	waitWeixin(t, "third poll", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return calls >= 3
	})
}

func TestWeixinWithoutLoginWaitsWithHint(t *testing.T) {
	channel := NewWeixinChannel(WeixinConfig{ProfileID: "p"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- channel.Connect(ctx, func(context.Context, MessageEvent) error { return nil }) }()
	waitWeixin(t, "login hint", func() bool { return strings.Contains(channel.Status().LastError, "扫码") })
	select {
	case err := <-done:
		t.Fatalf("Connect returned %v; it should wait instead of making the supervisor retry", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	<-done
	if err := channel.Send(context.Background(), OutgoingMessage{UserID: "u", Text: "hi"}); err == nil {
		t.Fatal("send without a login unexpectedly succeeded")
	}
}

func TestWeixinSendRefusesGroups(t *testing.T) {
	channel := newTestWeixinChannel(newWeixinFakeServer(t), t.TempDir())
	if err := channel.Send(context.Background(), OutgoingMessage{GroupID: "g", Text: "hi"}); err == nil || !strings.Contains(err.Error(), "私聊") {
		t.Fatalf("group send = %v, want a private-only refusal", err)
	}
}

func TestWeixinEventFromMessageMapsItemTypes(t *testing.T) {
	var msg weixinMessage
	raw := `{"message_id":"12","from_user_id":"u@im.wechat","create_time_ms":1700000000000,"item_list":[
		{"type":3,"voice_item":{"text":"语音转写"}},
		{"type":4,"file_item":{"file_name":"报告.pdf"}},
		{"type":5,"video_item":{}},
		{"type":1,"text_item":{"text":"看这个"},"ref_msg":{"svr_id":99,"message_item":{"type":1,"text_item":{"text":"原话"}}}}
	]}`
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	event, ok := weixinEventFromMessage(msg, "bot")
	if !ok {
		t.Fatal("message was not mapped")
	}
	want := "语音转写\n[文件] 报告.pdf\n[视频]\n看这个"
	if event.RawMessage != want {
		t.Fatalf("raw = %q, want %q", event.RawMessage, want)
	}
	if event.Quoted == nil || event.Quoted.MessageID != "99" || event.Quoted.RawMessage != "原话" {
		t.Fatalf("quoted = %+v", event.Quoted)
	}

	imageOnly := weixinMessage{FromUserID: "u", ItemList: []weixinMessageItem{{Type: weixinItemImage, ImageItem: &weixinImageItem{}}}}
	if event, ok := weixinEventFromMessage(imageOnly, "bot"); !ok || event.RawMessage != "[图片]" {
		t.Fatalf("image-only message = %+v ok=%v", event, ok)
	}
	if _, ok := weixinEventFromMessage(weixinMessage{FromUserID: "u"}, "bot"); ok {
		t.Fatal("an empty message was mapped")
	}
}

// Bot 自己发出的消息和群消息都不该投递：前者会让机器人跟自己对话，后者 iLink 回不过去。
func TestWeixinDispatchSkipsOwnAndGroupMessages(t *testing.T) {
	fake := newWeixinFakeServer(t)
	fake.push(`{"ret":0,"msgs":[` +
		`{"message_id":1,"from_user_id":"bot@im.bot","message_type":2,"item_list":[{"type":1,"text_item":{"text":"echo"}}]},` +
		`{"message_id":2,"from_user_id":"alice@im.wechat","group_id":"g1","item_list":[{"type":1,"text_item":{"text":"群里"}}]},` +
		`{"message_id":3,"from_user_id":"alice@im.wechat","item_list":[{"type":1,"text_item":{"text":"私聊"}}]},` +
		`{"message_id":3,"from_user_id":"alice@im.wechat","item_list":[{"type":1,"text_item":{"text":"私聊"}}]}]}`)
	channel := newTestWeixinChannel(fake, t.TempDir())
	events := make(chan MessageEvent, 4)
	runWeixin(t, channel, func(_ context.Context, event MessageEvent) error {
		events <- event
		return nil
	})
	select {
	case event := <-events:
		if event.RawMessage != "私聊" {
			t.Fatalf("delivered %q, want only the private message", event.RawMessage)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("private message was not delivered")
	}
	select {
	case event := <-events:
		t.Fatalf("unexpected extra delivery: %+v", event)
	case <-time.After(200 * time.Millisecond):
	}
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestWeixinInboundImageIsDownloadedAndDecrypted(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	fake := newWeixinFakeServer(t)
	plain := testPNG(t)
	key := []byte("0123456789abcdef")
	cipher, err := weixinEncryptAESECB(plain, key)
	if err != nil {
		t.Fatal(err)
	}
	fake.cdnBody = cipher
	fake.push(`{"ret":0,"msgs":[{"message_id":5,"from_user_id":"alice@im.wechat","item_list":[{"type":2,"image_item":{` +
		`"aeskey":"` + hex.EncodeToString(key) + `","media":{"encrypt_query_param":"q","full_url":"` + fake.server.URL + `/cdn/download"}}}]}]}`)
	channel := newTestWeixinChannel(fake, t.TempDir())
	events := make(chan MessageEvent, 1)
	runWeixin(t, channel, func(_ context.Context, event MessageEvent) error {
		events <- event
		return nil
	})
	var event MessageEvent
	select {
	case event = <-events:
	case <-time.After(3 * time.Second):
		t.Fatal("image message was not delivered")
	}
	var path string
	for _, segment := range event.Segments {
		if segment.Type == "image" {
			path = segment.Data["file"]
		}
	}
	if path == "" {
		t.Fatalf("no image segment: %+v", event.Segments)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("cached image does not match the decrypted original (err=%v)", err)
	}
}

func TestWeixinSendImageUploadsEncryptedToCDN(t *testing.T) {
	fake := newWeixinFakeServer(t)
	channel := newTestWeixinChannel(fake, t.TempDir())
	plain := testPNG(t)
	source := "base64://" + base64.StdEncoding.EncodeToString(plain)

	if err := channel.Send(context.Background(), OutgoingMessage{UserID: "alice@im.wechat", Text: "图来了", ImageURLs: []string{source}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	fake.mu.Lock()
	uploads, uploaded := fake.uploads, fake.uploaded
	fake.mu.Unlock()
	if len(uploads) != 1 || len(uploaded) != 1 {
		t.Fatalf("uploads=%d cdn posts=%d", len(uploads), len(uploaded))
	}
	req := uploads[0]
	if req["media_type"] != float64(weixinUploadMediaImage) || req["to_user_id"] != "alice@im.wechat" || req["rawsize"] != float64(len(plain)) {
		t.Fatalf("getuploadurl request = %+v", req)
	}
	if req["rawfilemd5"] != weixinMD5Hex(plain) || req["filesize"] != float64(len(uploaded[0])) {
		t.Fatalf("size/md5 mismatch: %+v", req)
	}
	key, err := hex.DecodeString(stringFromAny(req["aeskey"]))
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := weixinDecryptAESECB(uploaded[0], key)
	if err != nil || !bytes.Equal(decrypted, plain) {
		t.Fatalf("CDN received something that does not decrypt to the image (err=%v)", err)
	}

	sends := fake.sent()
	if len(sends) != 2 {
		t.Fatalf("sendmessage called %d times, want text then image", len(sends))
	}
	msg := sends[1]["msg"].(map[string]any)
	item := msg["item_list"].([]any)[0].(map[string]any)
	if item["type"] != float64(weixinItemImage) {
		t.Fatalf("second item type = %v", item["type"])
	}
	media := item["image_item"].(map[string]any)["media"].(map[string]any)
	if media["encrypt_query_param"] != "download-param-1" || media["encrypt_type"] != float64(1) {
		t.Fatalf("image media = %+v", media)
	}
	// 官方把十六进制密钥串再 base64；收端按「32 位十六进制」那条路还原。
	if media["aes_key"] != base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(key))) {
		t.Fatalf("aes_key = %v", media["aes_key"])
	}
	if roundTrip, err := weixinMediaKey("", stringFromAny(media["aes_key"])); err != nil || !bytes.Equal(roundTrip, key) {
		t.Fatalf("aes_key does not round-trip through the inbound decoder: %v", err)
	}
}

func TestWeixinMediaKeyFormats(t *testing.T) {
	key := []byte("0123456789abcdef")
	if got, err := weixinMediaKey(hex.EncodeToString(key), ""); err != nil || !bytes.Equal(got, key) {
		t.Fatalf("hex key: %v", err)
	}
	if got, err := weixinMediaKey("", base64.StdEncoding.EncodeToString(key)); err != nil || !bytes.Equal(got, key) {
		t.Fatalf("raw base64 key: %v", err)
	}
	if got, err := weixinMediaKey("", ""); err != nil || got != nil {
		t.Fatalf("no key should mean plaintext, got %v %v", got, err)
	}
	if _, err := weixinMediaKey("", base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Fatal("a key of the wrong length was accepted")
	}
}

func TestWeixinIDKeepsLargeNumbers(t *testing.T) {
	var out struct {
		A weixinID `json:"a"`
		B weixinID `json:"b"`
	}
	if err := json.Unmarshal([]byte(`{"a":18446744073709551615,"b":"42"}`), &out); err != nil {
		t.Fatal(err)
	}
	if out.A != "18446744073709551615" || out.B != "42" {
		t.Fatalf("got %q %q", out.A, out.B)
	}
}
