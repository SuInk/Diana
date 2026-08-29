// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCallbackRegistryRoutesAndReplaces(t *testing.T) {
	t.Cleanup(func() {
		UnregisterCallbackHandler(PlatformFeishu, "profile-a")
		UnregisterCallbackHandler(PlatformFeishu, "profile-b")
	})

	served := ""
	RegisterCallbackHandler(PlatformFeishu, "profile-a", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served = "a"
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	if !ServeCallback(PlatformFeishu, "profile-a", rec, httptest.NewRequest(http.MethodPost, "/", nil)) {
		t.Fatal("registered handler was not found")
	}
	if served != "a" {
		t.Fatalf("served = %q, want a", served)
	}

	// 没配多机器人时用不带配置档的短地址也要能路由到。
	rec = httptest.NewRecorder()
	if !ServeCallback(PlatformFeishu, "", rec, httptest.NewRequest(http.MethodPost, "/", nil)) {
		t.Fatal("the bare platform address did not route to the only handler")
	}

	// 配置改动后工厂会重建通道，新实例必须顶掉旧的，不然回调还打在已停用的连接上。
	RegisterCallbackHandler(PlatformFeishu, "profile-b", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served = "b"
		w.WriteHeader(http.StatusOK)
	}))
	rec = httptest.NewRecorder()
	ServeCallback(PlatformFeishu, "", rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if served != "b" {
		t.Fatalf("served = %q after re-registration, want b", served)
	}

	UnregisterCallbackHandler(PlatformFeishu, "profile-b")
	UnregisterCallbackHandler(PlatformFeishu, "profile-a")
	rec = httptest.NewRecorder()
	if ServeCallback(PlatformFeishu, "profile-a", rec, httptest.NewRequest(http.MethodPost, "/", nil)) {
		t.Fatal("an unregistered platform still served a callback")
	}
}

// 飞书在配置回调地址时先发一次 challenge，必须原样回显，否则地址保存不上。
func TestFeishuServeCallbackAnswersURLVerification(t *testing.T) {
	channel := NewFeishuChannel(FeishuConfig{AppID: "cli_x", AppSecret: "s", VerificationToken: "vtok"})
	body := `{"type":"url_verification","challenge":"chal-1","token":"vtok"}`
	req := httptest.NewRequest(http.MethodPost, FeishuCallbackPath, strings.NewReader(body))
	rec := httptest.NewRecorder()

	channel.ServeCallback(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var payload struct {
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Challenge != "chal-1" {
		t.Fatalf("challenge = %q, want chal-1", payload.Challenge)
	}
}

// Verification Token 对不上的请求必须拒绝——回调地址是公开的，不能当凭据用。
func TestFeishuServeCallbackRejectsWrongVerificationToken(t *testing.T) {
	channel := NewFeishuChannel(FeishuConfig{AppID: "cli_x", AppSecret: "s", VerificationToken: "vtok"})
	body := `{"type":"url_verification","challenge":"chal-1","token":"attacker"}`
	req := httptest.NewRequest(http.MethodPost, FeishuCallbackPath, strings.NewReader(body))
	rec := httptest.NewRecorder()

	channel.ServeCallback(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestFeishuServeCallbackDeliversMessage(t *testing.T) {
	channel := NewFeishuChannel(FeishuConfig{AppID: "cli_x", AppSecret: "s"})
	delivered := make(chan MessageEvent, 1)
	channel.handler = func(_ context.Context, event MessageEvent) error {
		delivered <- event
		return nil
	}

	body := `{
	  "header": {"event_id":"evt-9","event_type":"im.message.receive_v1"},
	  "event": {"sender":{"sender_id":{"open_id":"ou_a"}},
	    "message":{"message_id":"om_9","chat_id":"oc_1","chat_type":"p2p","message_type":"text",
	      "create_time":"1700000000000","content":"{\"text\":\"你好\"}"}}
	}`
	req := httptest.NewRequest(http.MethodPost, FeishuCallbackPath, strings.NewReader(body))
	rec := httptest.NewRecorder()
	channel.ServeCallback(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	select {
	case event := <-delivered:
		if event.RawMessage != "你好" {
			t.Fatalf("text = %q, want 你好", event.RawMessage)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the message was never delivered to the handler")
	}

	// 同一个 event_id 重推时不能再投递一次，否则机器人会把同一条消息答两遍。
	req = httptest.NewRequest(http.MethodPost, FeishuCallbackPath, strings.NewReader(body))
	channel.ServeCallback(httptest.NewRecorder(), req)
	select {
	case event := <-delivered:
		t.Fatalf("a redelivered event was processed again: %+v", event)
	case <-time.After(200 * time.Millisecond):
	}
}

// 企业微信配置回调地址时用 GET 带 echostr，要返回解密后的明文。
func TestWeComServeCallbackAnswersURLVerification(t *testing.T) {
	const token = "wecom-token"
	channel := NewWeComChannel(WeComConfig{
		CorpID: "wwcorp", AgentID: "1000002", Secret: "s",
		Token: token, EncodingAESKey: testEncodingAESKey,
	})
	echo := weComEncrypt(t, []byte("echo-plain"), testEncodingAESKey, "wwcorp")
	query := url.Values{
		"msg_signature": {weComSignature(token, "1700000000", "nonce", echo)},
		"timestamp":     {"1700000000"},
		"nonce":         {"nonce"},
		"echostr":       {echo},
	}
	req := httptest.NewRequest(http.MethodGet, WeComCallbackPath+"?"+query.Encode(), nil)
	rec := httptest.NewRecorder()

	channel.ServeCallback(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "echo-plain" {
		t.Fatalf("body = %q, want the decrypted echostr verbatim", rec.Body.String())
	}
}

func TestWeComServeCallbackRejectsForgedSignature(t *testing.T) {
	channel := NewWeComChannel(WeComConfig{
		CorpID: "wwcorp", AgentID: "1000002", Secret: "s",
		Token: "wecom-token", EncodingAESKey: testEncodingAESKey,
	})
	echo := weComEncrypt(t, []byte("echo-plain"), testEncodingAESKey, "wwcorp")
	query := url.Values{
		"msg_signature": {"deadbeef"},
		"timestamp":     {"1700000000"},
		"nonce":         {"nonce"},
		"echostr":       {echo},
	}
	req := httptest.NewRequest(http.MethodGet, WeComCallbackPath+"?"+query.Encode(), nil)
	rec := httptest.NewRecorder()

	channel.ServeCallback(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a forged signature", rec.Code)
	}
}

// 报文尾部的 receiveid 不是本企业时必须拒绝：要么发错了地方，要么是伪造的。
func TestWeComServeCallbackRejectsForeignCorpID(t *testing.T) {
	const token = "wecom-token"
	channel := NewWeComChannel(WeComConfig{
		CorpID: "wwcorp", AgentID: "1000002", Secret: "s",
		Token: token, EncodingAESKey: testEncodingAESKey,
	})
	message := `<xml><FromUserName>lisi</FromUserName><MsgType>text</MsgType><Content>hi</Content><MsgId>1</MsgId></xml>`
	encrypted := weComEncrypt(t, []byte(message), testEncodingAESKey, "someone-else")
	envelope := `<xml><ToUserName>wwcorp</ToUserName><Encrypt>` + encrypted + `</Encrypt></xml>`
	query := url.Values{
		"msg_signature": {weComSignature(token, "1700000000", "nonce", encrypted)},
		"timestamp":     {"1700000000"},
		"nonce":         {"nonce"},
	}
	req := httptest.NewRequest(http.MethodPost, WeComCallbackPath+"?"+query.Encode(), strings.NewReader(envelope))
	rec := httptest.NewRecorder()

	channel.ServeCallback(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a mismatched corp id", rec.Code)
	}
}
