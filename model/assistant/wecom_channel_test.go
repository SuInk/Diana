// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 这组用例用 httptest 顶替 qyapi.weixin.qq.com，把企业微信通道的收发、换 token
// 和错误路径跑一遍。能力边界见 platform_capability_matrix_test.go 里的声明。

type weComFakeAPI struct {
	server     *httptest.Server
	tokenCalls atomic.Int32
	mu         sync.Mutex
	sends      []weComFakeSend
	// sendErrCode 非零时发送接口返回这个错误码。
	sendErrCode atomic.Int32
	tokenErr    atomic.Bool
}

type weComFakeSend struct {
	Path  string
	Token string
	Body  map[string]any
}

func newWeComFakeAPI(t *testing.T) *weComFakeAPI {
	t.Helper()
	fake := &weComFakeAPI{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/gettoken":
			n := fake.tokenCalls.Add(1)
			if r.URL.Query().Get("corpid") != "wwcorp" || r.URL.Query().Get("corpsecret") != "secret" {
				t.Errorf("gettoken got corpid=%q secret=%q", r.URL.Query().Get("corpid"), r.URL.Query().Get("corpsecret"))
			}
			if fake.tokenErr.Load() {
				_, _ = w.Write([]byte(`{"errcode":40013,"errmsg":"invalid corpid"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "access_token": "tok-" + string(rune('0'+n)), "expires_in": 7200})
		case "/cgi-bin/message/send", "/cgi-bin/appchat/send":
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			fake.mu.Lock()
			fake.sends = append(fake.sends, weComFakeSend{Path: r.URL.Path, Token: r.URL.Query().Get("access_token"), Body: body})
			fake.mu.Unlock()
			if code := fake.sendErrCode.Load(); code != 0 {
				_ = json.NewEncoder(w).Encode(map[string]any{"errcode": code, "errmsg": "access_token expired"})
				return
			}
			_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok","msgid":"m1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *weComFakeAPI) lastSend(t *testing.T) weComFakeSend {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sends) == 0 {
		t.Fatal("no message was sent")
	}
	return f.sends[len(f.sends)-1]
}

func newTestWeComChannel(fake *weComFakeAPI) *WeComChannel {
	channel := NewWeComChannel(WeComConfig{
		ProfileID: "wecom-test", CorpID: "wwcorp", AgentID: "1000002", Secret: "secret",
		Token: "cb-token", EncodingAESKey: testEncodingAESKey,
	})
	channel.apiBase = fake.server.URL
	return channel
}

func TestWeComSendPrivateTextUsesMessageSendAndCachesToken(t *testing.T) {
	fake := newWeComFakeAPI(t)
	channel := newTestWeComChannel(fake)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if err := channel.Send(ctx, OutgoingMessage{UserID: "zhangsan", Text: "你好"}); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	// token 按 expires_in 缓存；每条消息都去换会很快撞上 gettoken 的单独限流。
	if got := fake.tokenCalls.Load(); got != 1 {
		t.Fatalf("gettoken called %d times, want 1", got)
	}
	sent := fake.lastSend(t)
	if sent.Path != "/cgi-bin/message/send" || sent.Token != "tok-1" {
		t.Fatalf("sent to %s with token %q", sent.Path, sent.Token)
	}
	if sent.Body["touser"] != "zhangsan" || sent.Body["msgtype"] != "text" || sent.Body["agentid"] != float64(1000002) {
		t.Fatalf("unexpected body: %+v", sent.Body)
	}
	if text, _ := sent.Body["text"].(map[string]any); text["content"] != "你好" {
		t.Fatalf("content = %+v", sent.Body["text"])
	}
}

// 群消息走 appchat/send 并用 chatid；带 Markdown 时改发 markdown 消息并按企业微信
// 认的子集降级（不认斜体）。
func TestWeComSendGroupMarkdownUsesAppChat(t *testing.T) {
	fake := newWeComFakeAPI(t)
	channel := newTestWeComChannel(fake)

	if err := channel.Send(context.Background(), OutgoingMessage{GroupID: "chat-1", Text: "**重点** 和 *斜体*"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	sent := fake.lastSend(t)
	if sent.Path != "/cgi-bin/appchat/send" || sent.Body["chatid"] != "chat-1" {
		t.Fatalf("group message went to %s chatid=%v", sent.Path, sent.Body["chatid"])
	}
	if sent.Body["msgtype"] != "markdown" {
		t.Fatalf("msgtype = %v, want markdown", sent.Body["msgtype"])
	}
	content, _ := sent.Body["markdown"].(map[string]any)["content"].(string)
	if !strings.Contains(content, "**重点**") || strings.Contains(content, "和 *斜体*") {
		t.Fatalf("markdown was not downgraded to the WeCom subset: %q", content)
	}
	if _, ok := sent.Body["touser"]; ok {
		t.Fatal("group message must not carry touser")
	}
}

// 42001 是 token 过期：要丢掉缓存，下一次发送重新换，而不是拿着死 token 一直失败。
func TestWeComSendInvalidatesTokenOnExpiry(t *testing.T) {
	fake := newWeComFakeAPI(t)
	channel := newTestWeComChannel(fake)
	ctx := context.Background()

	fake.sendErrCode.Store(42001)
	err := channel.Send(ctx, OutgoingMessage{UserID: "zhangsan", Text: "hi"})
	if err == nil || !strings.Contains(err.Error(), "42001") {
		t.Fatalf("send error = %v, want errcode 42001 surfaced", err)
	}
	fake.sendErrCode.Store(0)
	if err := channel.Send(ctx, OutgoingMessage{UserID: "zhangsan", Text: "hi"}); err != nil {
		t.Fatalf("send after refresh: %v", err)
	}
	if got := fake.tokenCalls.Load(); got != 2 {
		t.Fatalf("gettoken called %d times, want 2 (refresh after 42001)", got)
	}
	if sent := fake.lastSend(t); sent.Token != "tok-2" {
		t.Fatalf("retry used token %q, want the refreshed tok-2", sent.Token)
	}
}

func TestWeComSendErrorPaths(t *testing.T) {
	fake := newWeComFakeAPI(t)
	ctx := context.Background()

	t.Run("empty text sends nothing", func(t *testing.T) {
		channel := newTestWeComChannel(fake)
		if err := channel.Send(ctx, OutgoingMessage{UserID: "u", Text: "   "}); err != nil {
			t.Fatalf("send: %v", err)
		}
		if fake.tokenCalls.Load() != 0 {
			t.Fatal("an empty message should not even fetch a token")
		}
	})
	t.Run("missing receiver", func(t *testing.T) {
		channel := newTestWeComChannel(fake)
		if err := channel.Send(ctx, OutgoingMessage{Text: "hi"}); err == nil || !strings.Contains(err.Error(), "接收人") {
			t.Fatalf("err = %v, want missing receiver", err)
		}
	})
	t.Run("non numeric agent id", func(t *testing.T) {
		channel := newTestWeComChannel(fake)
		channel.SetConfig(WeComConfig{CorpID: "wwcorp", AgentID: "abc", Secret: "secret"})
		if err := channel.Send(ctx, OutgoingMessage{UserID: "u", Text: "hi"}); err == nil || !strings.Contains(err.Error(), "AgentId") {
			t.Fatalf("err = %v, want AgentId error", err)
		}
	})
	t.Run("token rejected", func(t *testing.T) {
		fake.tokenErr.Store(true)
		defer fake.tokenErr.Store(false)
		channel := newTestWeComChannel(fake)
		if err := channel.Send(ctx, OutgoingMessage{UserID: "u", Text: "hi"}); err == nil || !strings.Contains(err.Error(), "40013") {
			t.Fatalf("err = %v, want gettoken errcode surfaced", err)
		}
	})
}

// wecomCallbackRequest 造一条带合法签名的加密回调。
func wecomCallbackRequest(t *testing.T, message, receiveID string) *http.Request {
	t.Helper()
	encrypted := weComEncrypt(t, []byte(message), testEncodingAESKey, receiveID)
	envelope := `<xml><ToUserName>wwcorp</ToUserName><AgentID>1000002</AgentID><Encrypt>` + encrypted + `</Encrypt></xml>`
	query := url.Values{
		"msg_signature": {weComSignature("cb-token", "1700000000", "nonce", encrypted)},
		"timestamp":     {"1700000000"},
		"nonce":         {"nonce"},
	}
	return httptest.NewRequest(http.MethodPost, WeComCallbackPath+"?"+query.Encode(), strings.NewReader(envelope))
}

func TestWeComConnectReceivesCallbackAndDedupes(t *testing.T) {
	fake := newWeComFakeAPI(t)
	channel := newTestWeComChannel(fake)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	delivered := make(chan MessageEvent, 4)
	done := make(chan error, 1)
	go func() {
		done <- channel.Connect(ctx, func(_ context.Context, event MessageEvent) error {
			delivered <- event
			return nil
		})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !channel.Status().Connected {
		if time.Now().After(deadline) {
			t.Fatalf("channel never came online: %+v", channel.Status())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if channel.Status().SelfID != "1000002" {
		t.Fatalf("self id = %q, want the agent id", channel.Status().SelfID)
	}

	message := `<xml><ToUserName>wwcorp</ToUserName><FromUserName>zhangsan</FromUserName><CreateTime>1700000000</CreateTime>` +
		`<MsgType>text</MsgType><Content>在吗</Content><MsgId>42</MsgId><AgentID>1000002</AgentID></xml>`
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		channel.ServeCallback(rec, wecomCallbackRequest(t, message, "wwcorp"))
		// 空 200 就是「已收到」；回复走主动发送，赶不上被动回复的 5 秒窗口。
		if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
			t.Fatalf("callback answered %d %q, want an empty 200", rec.Code, rec.Body.String())
		}
	}
	select {
	case event := <-delivered:
		if event.UserID != "zhangsan" || event.RawMessage != "在吗" || event.MessageID != "42" || event.Kind != EventKindPrivate {
			t.Fatalf("unexpected event: %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("callback was not delivered to the handler")
	}
	// 企业微信没收到及时响应会重推同一个 MsgId，不能处理两遍。
	select {
	case event := <-delivered:
		t.Fatalf("a redelivered callback was processed again: %+v", event)
	case <-time.After(200 * time.Millisecond):
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Connect did not return after cancel")
	}
	if channel.Status().Connected {
		t.Fatal("status still connected after shutdown")
	}
}

func TestWeComConnectRequiresCredentials(t *testing.T) {
	channel := NewWeComChannel(WeComConfig{CorpID: "wwcorp"})
	err := channel.Connect(context.Background(), func(context.Context, MessageEvent) error { return nil })
	if err == nil {
		t.Fatal("Connect without a secret unexpectedly succeeded")
	}
	if channel.Status().LastError == "" {
		t.Fatal("missing credentials should be visible in the channel status")
	}
}

// 换不到 token 时 Connect 要带着原因失败，交给外层退避重连，而不是假装在线。
func TestWeComConnectFailsWhenTokenRejected(t *testing.T) {
	fake := newWeComFakeAPI(t)
	fake.tokenErr.Store(true)
	channel := newTestWeComChannel(fake)
	err := channel.Connect(context.Background(), func(context.Context, MessageEvent) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "40013") {
		t.Fatalf("Connect error = %v, want the gettoken rejection", err)
	}
	if channel.Status().Connected || !strings.Contains(channel.Status().LastError, "40013") {
		t.Fatalf("status = %+v, want offline with the rejection reason", channel.Status())
	}
}

func TestWeComServeCallbackRejectsMalformedRequests(t *testing.T) {
	channel := newTestWeComChannel(newWeComFakeAPI(t))

	rec := httptest.NewRecorder()
	channel.ServeCallback(rec, httptest.NewRequest(http.MethodPut, WeComCallbackPath, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT answered %d, want 405", rec.Code)
	}

	rec = httptest.NewRecorder()
	channel.ServeCallback(rec, httptest.NewRequest(http.MethodPost, WeComCallbackPath, strings.NewReader("not xml")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("garbage body answered %d, want 400", rec.Code)
	}

	// 签名对得上但密文被改过：解密必须失败，不能把乱码当报文解析。
	garbage := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	query := url.Values{
		"msg_signature": {weComSignature("cb-token", "1", "n", garbage)},
		"timestamp":     {"1"},
		"nonce":         {"n"},
	}
	body := `<xml><Encrypt>` + garbage + `</Encrypt></xml>`
	rec = httptest.NewRecorder()
	channel.ServeCallback(rec, httptest.NewRequest(http.MethodPost, WeComCallbackPath+"?"+query.Encode(), strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("undecryptable payload answered %d, want 400", rec.Code)
	}
}

func TestWeComDecryptRejectsMalformedCiphertext(t *testing.T) {
	if _, _, err := weComDecrypt("%%%not-base64", testEncodingAESKey); err == nil {
		t.Fatal("non-base64 ciphertext unexpectedly decrypted")
	}
	short := base64.StdEncoding.EncodeToString([]byte("short"))
	if _, _, err := weComDecrypt(short, testEncodingAESKey); err == nil {
		t.Fatal("ciphertext that is not a whole number of blocks unexpectedly decrypted")
	}
}

func TestWeComEventFromCallbackSkipsEmptyContent(t *testing.T) {
	blank := []byte(`<xml><FromUserName>lisi</FromUserName><MsgType>text</MsgType><Content>   </Content></xml>`)
	if _, ok := weComEventFromCallback(blank, "1"); ok {
		t.Fatal("blank text was mapped to a chat event")
	}
	anonymous := []byte(`<xml><MsgType>text</MsgType><Content>hi</Content></xml>`)
	if _, ok := weComEventFromCallback(anonymous, "1"); ok {
		t.Fatal("a message without a sender was mapped to a chat event")
	}
	if _, ok := weComEventFromCallback([]byte("<xml"), "1"); ok {
		t.Fatal("malformed XML was mapped to a chat event")
	}
}

// 企业微信的 AgentID 是字符串形式的数字，CallAPI 把 access_token 拼进查询串。
func TestWeComCallAPIAttachesAccessToken(t *testing.T) {
	var gotToken, gotUser string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi-bin/gettoken" {
			_, _ = w.Write([]byte(`{"errcode":0,"access_token":"api-token","expires_in":7200}`))
			return
		}
		gotToken = r.URL.Query().Get("access_token")
		gotUser = r.URL.Query().Get("userid")
		_, _ = w.Write([]byte(`{"errcode":0,"name":"张三"}`))
	}))
	defer server.Close()
	channel := NewWeComChannel(WeComConfig{CorpID: "wwcorp", AgentID: "1", Secret: "secret"})
	channel.apiBase = server.URL

	out, err := channel.CallAPI(context.Background(), "GET /cgi-bin/user/get", map[string]any{"userid": "zhangsan"})
	if err != nil {
		t.Fatalf("CallAPI: %v", err)
	}
	if gotToken != "api-token" || gotUser != "zhangsan" || out["name"] != "张三" {
		t.Fatalf("token=%q user=%q out=%+v", gotToken, gotUser, out)
	}
}
