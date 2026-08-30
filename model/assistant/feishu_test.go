// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// feishuEncrypt 是测试侧的加密实现，用来造出和飞书一致的密文。
func feishuEncrypt(t *testing.T, plaintext []byte, encryptKey string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(encryptKey))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		t.Fatalf("rand: %v", err)
	}
	padding := aes.BlockSize - len(plaintext)%aes.BlockSize
	padded := append([]byte{}, plaintext...)
	for i := 0; i < padding; i++ {
		padded = append(padded, byte(padding))
	}
	encrypted := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(encrypted, padded)
	return base64.StdEncoding.EncodeToString(append(iv, encrypted...))
}

func TestFeishuDecodeCallbackPlaintext(t *testing.T) {
	body := []byte(`{"type":"url_verification","challenge":"abc"}`)
	out, err := feishuDecodeCallback(body, "")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(out) != string(body) {
		t.Fatalf("plaintext callback was altered: %q", out)
	}
}

func TestFeishuDecodeCallbackEncrypted(t *testing.T) {
	const key = "encrypt-key-1"
	inner := []byte(`{"header":{"event_type":"im.message.receive_v1"}}`)
	envelope, err := json.Marshal(map[string]string{"encrypt": feishuEncrypt(t, inner, key)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out, err := feishuDecodeCallback(envelope, key)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(out) != string(inner) {
		t.Fatalf("decrypted = %q, want %q", out, inner)
	}
}

// 配了 Encrypt Key 却收到明文，说明来源可疑或后台没同步开加密——必须报错，
// 否则任何人拿到回调地址就能伪造事件。
func TestFeishuDecodeCallbackRejectsPlaintextWhenKeyConfigured(t *testing.T) {
	body := []byte(`{"header":{"event_type":"im.message.receive_v1"}}`)
	if _, err := feishuDecodeCallback(body, "encrypt-key-1"); err == nil {
		t.Fatal("plaintext callback was accepted while an encrypt key is configured")
	}
}

func TestFeishuDecodeCallbackRejectsEncryptedWithoutKey(t *testing.T) {
	envelope := []byte(`{"encrypt":"c29tZS1jaXBoZXItdGV4dA=="}`)
	if _, err := feishuDecodeCallback(envelope, ""); err == nil {
		t.Fatal("encrypted callback was accepted without an encrypt key")
	}
}

func TestFeishuSignatureIsStable(t *testing.T) {
	first := feishuSignature("1700000000", "nonce", "key", "body")
	second := feishuSignature("1700000000", "nonce", "key", "body")
	if first != second {
		t.Fatal("signature is not deterministic")
	}
	if first == feishuSignature("1700000001", "nonce", "key", "body") {
		t.Fatal("signature ignores the timestamp")
	}
}

func TestFeishuEventFromCallbackGroupMessage(t *testing.T) {
	payload := []byte(`{
	  "header": {"event_id":"evt-1","event_type":"im.message.receive_v1","create_time":"1700000000000"},
	  "event": {
	    "sender": {"sender_id": {"open_id":"ou_sender"}, "sender_type":"user"},
	    "message": {
	      "message_id":"om_1","chat_id":"oc_chat","chat_type":"group","message_type":"text",
	      "create_time":"1700000000000",
	      "content":"{\"text\":\"@_user_1 帮我查下天气\"}",
	      "mentions":[{"key":"@_user_1","name":"Diana","id":{"open_id":"ou_bot"}}]
	    }
	  }
	}`)
	event, ok := feishuEventFromCallback(payload, "cli_app")
	if !ok {
		t.Fatal("group message was not mapped")
	}
	if event.Kind != EventKindGroup || event.GroupID != "oc_chat" {
		t.Fatalf("kind = %q group = %q, want group/oc_chat", event.Kind, event.GroupID)
	}
	if event.UserID != "ou_sender" {
		t.Fatalf("user = %q, want ou_sender", event.UserID)
	}
	// 占位符必须换成人名，否则模型看到的是一串没有意义的记号。
	if event.RawMessage != "@Diana 帮我查下天气" {
		t.Fatalf("text = %q, mention placeholder was not resolved", event.RawMessage)
	}
	if !event.ToMe {
		t.Fatal("a message mentioning the bot should be marked as addressed to it")
	}
	if event.Time != 1700000000 {
		t.Fatalf("time = %d, want 1700000000 (milliseconds should fold to seconds)", event.Time)
	}
}

func TestFeishuEventFromCallbackPrivateMessage(t *testing.T) {
	payload := []byte(`{
	  "header": {"event_id":"evt-2","event_type":"im.message.receive_v1"},
	  "event": {
	    "sender": {"sender_id": {"open_id":"ou_sender"}},
	    "message": {"message_id":"om_2","chat_id":"oc_p2p","chat_type":"p2p","message_type":"text",
	      "create_time":"1700000000000","content":"{\"text\":\"你好\"}"}
	  }
	}`)
	event, ok := feishuEventFromCallback(payload, "cli_app")
	if !ok {
		t.Fatal("private message was not mapped")
	}
	if event.Kind != EventKindPrivate {
		t.Fatalf("kind = %q, want private", event.Kind)
	}
	if !event.ToMe {
		t.Fatal("private messages are always addressed to the bot")
	}
}

// 富媒体消息给的是 image_key，没有可直接使用的文本，不该投递。
func TestFeishuEventFromCallbackSkipsNonText(t *testing.T) {
	payload := []byte(`{
	  "header": {"event_type":"im.message.receive_v1"},
	  "event": {"sender":{"sender_id":{"open_id":"ou_x"}},
	    "message":{"message_id":"om_3","chat_type":"p2p","message_type":"image","content":"{\"image_key\":\"img\"}"}}
	}`)
	if _, ok := feishuEventFromCallback(payload, "cli_app"); ok {
		t.Fatal("image message was unexpectedly mapped to a chat event")
	}
}

func TestFeishuReceiveIDTypeFollowsPrefix(t *testing.T) {
	cases := map[string]string{
		"oc_chat":       "chat_id",
		"ou_user":       "open_id",
		"on_union":      "union_id",
		"a@example.com": "email",
	}
	for id, want := range cases {
		if got := feishuReceiveIDType(id, false); got != want {
			t.Fatalf("feishuReceiveIDType(%q) = %q, want %q", id, got, want)
		}
	}
}

// 飞书在没收到 200 时会重推同一个事件，重复投递会让机器人把同一条消息答两遍。
func TestEventDeduperRejectsRepeats(t *testing.T) {
	deduper := newEventDeduper(time.Minute)
	if !deduper.Accept("evt-1") {
		t.Fatal("first delivery was rejected")
	}
	if deduper.Accept("evt-1") {
		t.Fatal("repeated delivery of the same event was accepted")
	}
	if !deduper.Accept("evt-2") {
		t.Fatal("a different event was rejected")
	}
	// 没有事件 ID 时无法去重，只能放行，不然会静默丢消息。
	if !deduper.Accept("") {
		t.Fatal("an event without an id should be allowed through")
	}
}
