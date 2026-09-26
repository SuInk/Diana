// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeWeixinQRServer 按顺序吐出 get_qrcode_status 的状态，模拟扫码流程。
type fakeWeixinQRServer struct {
	server *httptest.Server
	mu     sync.Mutex
	// statuses 依次作为状态接口的响应，取完后一直回 wait。
	statuses    []string
	qrCalls     int
	tokenLists  [][]string
	verifyCodes []string
	polledQR    []string
}

func newFakeWeixinQRServer(t *testing.T, statuses ...string) *fakeWeixinQRServer {
	t.Helper()
	fake := &fakeWeixinQRServer{statuses: statuses}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		fake.mu.Lock()
		defer fake.mu.Unlock()
		switch r.URL.Path {
		case "/ilink/bot/get_bot_qrcode":
			if r.Method != http.MethodPost || r.URL.Query().Get("bot_type") != "3" {
				t.Errorf("get_bot_qrcode %s bot_type=%q", r.Method, r.URL.Query().Get("bot_type"))
			}
			var body struct {
				Tokens []string `json:"local_token_list"`
			}
			_ = json.Unmarshal(raw, &body)
			fake.tokenLists = append(fake.tokenLists, body.Tokens)
			fake.qrCalls++
			_ = json.NewEncoder(w).Encode(map[string]string{
				"qrcode":             "qr-" + string(rune('0'+fake.qrCalls)),
				"qrcode_img_content": "https://liteapp.weixin.qq.com/q/demo" + string(rune('0'+fake.qrCalls)),
			})
		case "/ilink/bot/get_qrcode_status":
			if r.Method != http.MethodGet || r.Header.Get("iLink-App-Id") != "bot" {
				t.Errorf("get_qrcode_status %s app-id=%q", r.Method, r.Header.Get("iLink-App-Id"))
			}
			fake.polledQR = append(fake.polledQR, r.URL.Query().Get("qrcode"))
			fake.verifyCodes = append(fake.verifyCodes, r.URL.Query().Get("verify_code"))
			next := `{"status":"wait"}`
			if len(fake.statuses) > 0 {
				next, fake.statuses = fake.statuses[0], fake.statuses[1:]
			}
			_, _ = w.Write([]byte(next))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func newTestWeixinLogin(fake *fakeWeixinQRServer) *WeixinLoginManager {
	m := NewWeixinLoginManager()
	m.SetAPIBase(fake.server.URL)
	return m
}

func TestWeixinLoginHappyPathWithVerifyCode(t *testing.T) {
	fake := newFakeWeixinQRServer(t,
		`{"status":"wait"}`,
		`{"status":"scaned"}`,
		`{"status":"need_verifycode"}`,
		`{"status":"confirmed","bot_token":"new-token","ilink_bot_id":"bot@im.bot","baseurl":"https://ilinkai.weixin.qq.com","ilink_user_id":"me@im.wechat"}`,
	)
	m := newTestWeixinLogin(fake)
	ctx := context.Background()

	start, err := m.Start(ctx, "profile-1", []string{"old-token", ""})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !strings.HasPrefix(start.QRCodeImage, "data:image/png;base64,") || start.QRCodeURL == "" || start.SessionID == "" {
		t.Fatalf("start = %+v, want a rendered QR code", start)
	}
	// 本地已有的 token 要带上，服务端靠它认出「这个号已经连过」。
	if got := fake.tokenLists[0]; len(got) != 1 || got[0] != "old-token" {
		t.Fatalf("local_token_list = %v", got)
	}

	want := []string{WeixinLoginWaiting, WeixinLoginScanned, WeixinLoginNeedCode}
	for _, status := range want {
		got, err := m.Poll(ctx, "profile-1", start.SessionID, "")
		if err != nil || got.Status != status {
			t.Fatalf("poll = %+v %v, want %s", got, err, status)
		}
		if got.Credentials != nil {
			t.Fatal("credentials leaked before confirmation")
		}
	}
	done, err := m.Poll(ctx, "profile-1", start.SessionID, "42")
	if err != nil || done.Status != WeixinLoginConfirmed || done.Credentials == nil {
		t.Fatalf("final poll = %+v %v", done, err)
	}
	if c := done.Credentials; c.BotToken != "new-token" || c.BotID != "bot@im.bot" || c.UserID != "me@im.wechat" {
		t.Fatalf("credentials = %+v", c)
	}
	if fake.verifyCodes[len(fake.verifyCodes)-1] != "42" {
		t.Fatalf("verify code was not forwarded: %v", fake.verifyCodes)
	}
	// 会话用完即删，同一个 ID 不能再被拿来轮询。
	if again, _ := m.Poll(ctx, "profile-1", start.SessionID, ""); again.Status != WeixinLoginExpired {
		t.Fatalf("reused session answered %q", again.Status)
	}
}

// 二维码过期自动换新，换满次数就结束，不无限续期。
func TestWeixinLoginRefreshesExpiredQRCodeThenGivesUp(t *testing.T) {
	fake := newFakeWeixinQRServer(t, `{"status":"expired"}`, `{"status":"expired"}`, `{"status":"expired"}`)
	m := newTestWeixinLogin(fake)
	ctx := context.Background()
	start, err := m.Start(ctx, "p", nil)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := m.Poll(ctx, "p", start.SessionID, "")
	if first.Status != WeixinLoginWaiting || first.QRCodeImage == "" || first.QRCodeURL == start.QRCodeURL {
		t.Fatalf("expired QR was not replaced: %+v", first)
	}
	// 换码后轮询要用新的 qrcode。
	_, _ = m.Poll(ctx, "p", start.SessionID, "")
	if fake.polledQR[1] != "qr-2" {
		t.Fatalf("polled %v, want the refreshed qrcode", fake.polledQR)
	}
	last, _ := m.Poll(ctx, "p", start.SessionID, "")
	if last.Status != WeixinLoginFailed {
		t.Fatalf("after repeated expiry status = %q, want failed", last.Status)
	}
}

func TestWeixinLoginRejectsForeignSessionAndMissingCredentials(t *testing.T) {
	fake := newFakeWeixinQRServer(t, `{"status":"confirmed","ilink_bot_id":"bot"}`)
	m := newTestWeixinLogin(fake)
	ctx := context.Background()
	start, err := m.Start(ctx, "p1", nil)
	if err != nil {
		t.Fatal(err)
	}
	// 别的机器人拿不到这台的会话。
	if got, _ := m.Poll(ctx, "p2", start.SessionID, ""); got.Status != WeixinLoginExpired {
		t.Fatalf("foreign profile polled someone else's session: %+v", got)
	}
	got, _ := m.Poll(ctx, "p1", start.SessionID, "")
	if got.Status != WeixinLoginFailed || got.Credentials != nil {
		t.Fatalf("confirmation without a bot token = %+v, want failed", got)
	}
	if _, err := m.Start(ctx, "", nil); err == nil {
		t.Fatal("login without a profile id was accepted")
	}
}

func TestWeixinLoginAlreadyBound(t *testing.T) {
	fake := newFakeWeixinQRServer(t, `{"status":"binded_redirect"}`)
	m := newTestWeixinLogin(fake)
	start, err := m.Start(context.Background(), "p", []string{"tok"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := m.Poll(context.Background(), "p", start.SessionID, "")
	if got.Status != WeixinLoginAlreadyBound || got.Credentials != nil {
		t.Fatalf("binded_redirect = %+v", got)
	}
}
