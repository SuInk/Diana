// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"testing"
)

// weComEncrypt 是测试侧的加密实现，用来造出和企业微信一致的密文。
func weComEncrypt(t *testing.T, plaintext []byte, encodingAESKey, receiveID string) string {
	t.Helper()
	key, err := weComAESKey(encodingAESKey)
	if err != nil {
		t.Fatalf("aes key: %v", err)
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("rand: %v", err)
	}
	length := make([]byte, 4)
	binary.BigEndian.PutUint32(length, uint32(len(plaintext)))

	raw := append([]byte{}, nonce...)
	raw = append(raw, length...)
	raw = append(raw, plaintext...)
	raw = append(raw, receiveID...)

	padding := aes.BlockSize - len(raw)%aes.BlockSize
	for i := 0; i < padding; i++ {
		raw = append(raw, byte(padding))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	out := make([]byte, len(raw))
	cipher.NewCBCEncrypter(block, key[:aes.BlockSize]).CryptBlocks(out, raw)
	return base64.StdEncoding.EncodeToString(out)
}

const testEncodingAESKey = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"

func TestWeComDecryptRoundTrip(t *testing.T) {
	message := `<xml><ToUserName>corp</ToUserName><FromUserName>lisi</FromUserName><MsgType>text</MsgType><Content>你好</Content></xml>`
	encrypted := weComEncrypt(t, []byte(message), testEncodingAESKey, "wwcorpid")

	plaintext, receiveID, err := weComDecrypt(encrypted, testEncodingAESKey)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(plaintext) != message {
		t.Fatalf("plaintext = %q, want %q", plaintext, message)
	}
	if receiveID != "wwcorpid" {
		t.Fatalf("receiveID = %q, want wwcorpid", receiveID)
	}
}

// 密钥不对时必须报错，不能返回一段乱码让上层当成正常报文去解析。
func TestWeComDecryptRejectsWrongKey(t *testing.T) {
	encrypted := weComEncrypt(t, []byte("<xml></xml>"), testEncodingAESKey, "wwcorpid")
	other := "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"
	if _, _, err := weComDecrypt(encrypted, other); err == nil {
		t.Fatal("decrypt with a wrong key unexpectedly succeeded")
	}
}

func TestWeComAESKeyRejectsBadLength(t *testing.T) {
	if _, err := weComAESKey("tooshort"); err == nil {
		t.Fatal("short EncodingAESKey unexpectedly accepted")
	}
	if _, err := weComAESKey(""); err == nil {
		t.Fatal("empty EncodingAESKey unexpectedly accepted")
	}
}

func TestWeComSignatureValid(t *testing.T) {
	token, timestamp, nonce, encrypted := "tok", "1700000000", "abc", "cipher-text"
	signature := weComSignature(token, timestamp, nonce, encrypted)

	if !weComSignatureValid(token, timestamp, nonce, encrypted, signature) {
		t.Fatal("valid signature was rejected")
	}
	if weComSignatureValid(token, timestamp, nonce, encrypted, "deadbeef") {
		t.Fatal("forged signature was accepted")
	}
	// 没有 Token 就没有验签依据。这条链路公网可达，必须拒绝而不是放行。
	if weComSignatureValid("", timestamp, nonce, encrypted, signature) {
		t.Fatal("callback without a configured token was accepted")
	}
}

func TestWeComEventFromCallbackMapsGroupAndPrivate(t *testing.T) {
	private := []byte(`<xml><ToUserName>corp</ToUserName><FromUserName>zhangsan</FromUserName>` +
		`<CreateTime>1700000000</CreateTime><MsgType>text</MsgType><Content>在吗</Content>` +
		`<MsgId>123</MsgId><AgentID>1000002</AgentID></xml>`)
	event, ok := weComEventFromCallback(private, "1000002")
	if !ok {
		t.Fatal("private message was not mapped")
	}
	if event.Kind != EventKindPrivate {
		t.Fatalf("kind = %q, want private", event.Kind)
	}
	if event.UserID != "zhangsan" || event.RawMessage != "在吗" {
		t.Fatalf("unexpected mapping: user=%q text=%q", event.UserID, event.RawMessage)
	}
	if !event.ToMe {
		t.Fatal("application messages are always addressed to the bot")
	}

	group := []byte(`<xml><FromUserName>lisi</FromUserName><CreateTime>1700000000</CreateTime>` +
		`<MsgType>text</MsgType><Content>hi</Content><MsgId>124</MsgId><ChatId>chat-1</ChatId></xml>`)
	event, ok = weComEventFromCallback(group, "1000002")
	if !ok {
		t.Fatal("group message was not mapped")
	}
	if event.Kind != EventKindGroup || event.GroupID != "chat-1" {
		t.Fatalf("kind = %q group = %q, want group/chat-1", event.Kind, event.GroupID)
	}
}

// 非文本消息（图片、事件推送）不该被当成对话投递给模型。
func TestWeComEventFromCallbackSkipsNonText(t *testing.T) {
	payload := []byte(`<xml><FromUserName>lisi</FromUserName><MsgType>image</MsgType><MsgId>9</MsgId></xml>`)
	if _, ok := weComEventFromCallback(payload, "1000002"); ok {
		t.Fatal("image message was unexpectedly mapped to a chat event")
	}
	event := []byte(`<xml><FromUserName>lisi</FromUserName><MsgType>event</MsgType><Event>subscribe</Event></xml>`)
	if _, ok := weComEventFromCallback(event, "1000002"); ok {
		t.Fatal("event push was unexpectedly mapped to a chat event")
	}
}
