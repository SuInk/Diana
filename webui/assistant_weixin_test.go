// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

// 扫码成功后凭据要落进这台机器人的配置；之后的普通保存不能把它冲掉，解绑才清。
func TestWeixinLoginPersistsCredentialsAndSurvivesSave(t *testing.T) {
	ilink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		switch r.URL.Path {
		case "/ilink/bot/get_bot_qrcode":
			_, _ = w.Write([]byte(`{"qrcode":"qr-1","qrcode_img_content":"https://liteapp.weixin.qq.com/q/x"}`))
		case "/ilink/bot/get_qrcode_status":
			_, _ = w.Write([]byte(`{"status":"confirmed","bot_token":"wx-token","ilink_bot_id":"bot@im.bot","ilink_user_id":"me@im.wechat"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ilink.Close()

	config := assistant.DefaultBotConfig()
	config.Platform = assistant.PlatformWeixin
	config.Enabled = false
	runtime := assistant.NewRuntime(config, fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := NewBotHandlerWithFactory(ctx, runtime, func(assistant.BotConfig) assistant.Channel { return fakeChannel{} })
	h.weixinLoginManager().SetAPIBase(ilink.URL)
	router := botTestRouter(h)
	profileID := h.profiles.Profiles().Profiles[0].ID

	post := func(path string, payload any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(payload)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw)))
		return rec
	}

	rec := post("/api/assistant/weixin/login", map[string]any{"profile_id": profileID})
	if rec.Code != http.StatusOK {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	var start assistant.WeixinLoginStatus
	_ = json.Unmarshal(rec.Body.Bytes(), &start)
	if start.QRCodeImage == "" {
		t.Fatalf("start returned no QR code: %s", rec.Body.String())
	}

	rec = post("/api/assistant/weixin/login/poll", map[string]any{"profile_id": profileID, "session_id": start.SessionID})
	if rec.Code != http.StatusOK {
		t.Fatalf("poll: %d %s", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("wx-token")) {
		t.Fatal("the bot token leaked into the poll response")
	}
	saved, _ := h.profiles.Profiles().ConfigForProfile(profileID)
	if saved.WeixinBotToken != "wx-token" || saved.WeixinBotID != "bot@im.bot" || saved.OwnerID != "me@im.wechat" {
		t.Fatalf("credentials not persisted: token=%q id=%q owner=%q", saved.WeixinBotToken, saved.WeixinBotID, saved.OwnerID)
	}

	// 前端保存别的设置时不带 token，也不能因此清掉。
	payload := assistant.PayloadFromConfig(saved)
	payload.Name = "改个名字"
	if rec = post("/api/assistant/config", payload); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	saved, _ = h.profiles.Profiles().ConfigForProfile(profileID)
	if saved.WeixinBotToken != "wx-token" {
		t.Fatal("an ordinary config save wiped the weixin login")
	}

	if rec = post("/api/assistant/weixin/logout", map[string]any{"profile_id": profileID}); rec.Code != http.StatusOK {
		t.Fatalf("logout: %d %s", rec.Code, rec.Body.String())
	}
	saved, _ = h.profiles.Profiles().ConfigForProfile(profileID)
	if saved.WeixinBotToken != "" || saved.WeixinBotID != "" {
		t.Fatal("logout left the credentials in place")
	}
}

func TestWeixinLoginRejectsNonWeixinProfile(t *testing.T) {
	config := assistant.DefaultBotConfig()
	config.Enabled = false
	runtime := assistant.NewRuntime(config, fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	h := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel { return fakeChannel{} })
	router := botTestRouter(h)
	raw, _ := json.Marshal(map[string]any{"profile_id": h.profiles.Profiles().Profiles[0].ID})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/assistant/weixin/login", bytes.NewReader(raw)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("QR login on a OneBot profile answered %d", rec.Code)
	}
}
