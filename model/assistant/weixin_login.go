// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

// 扫码登录流程，对照官方 src/auth/login-qr.ts：
//
//  1. POST ilink/bot/get_bot_qrcode?bot_type=3 拿到 qrcode（轮询凭据）和
//     qrcode_img_content（要编码成二维码给手机扫的链接）
//  2. GET ilink/bot/get_qrcode_status?qrcode=... 长轮询状态，直到 confirmed
//  3. confirmed 时带回 bot_token、ilink_bot_id、baseurl、ilink_user_id
//
// 这里不在后台自己跑轮询：WebUI 每次调 Poll 就代发一轮长轮询，页面关掉流程就停，
// 不会有没人看的二维码在后台一直续期。

const (
	// weixinQRPollTimeout 比官方的 35 秒短，留出余量给前面可能挡着的反向代理。
	weixinQRPollTimeout = 25 * time.Second
	// weixinLoginTTL 是一次登录会话的寿命，和官方的默认等待时长一致。
	weixinLoginTTL = 8 * time.Minute
	// weixinQRMaxRefresh 是二维码过期后自动换新的次数上限。
	weixinQRMaxRefresh = 3
)

// 扫码状态，前端按这些值切换界面。
const (
	WeixinLoginWaiting       = "wait"
	WeixinLoginScanned       = "scaned"
	WeixinLoginNeedCode      = "need_verifycode"
	WeixinLoginConfirmed     = "confirmed"
	WeixinLoginAlreadyBound  = "binded_redirect"
	WeixinLoginExpired       = "expired"
	WeixinLoginFailed        = "failed"
	weixinLoginRedirect      = "scaned_but_redirect"
	weixinLoginVerifyBlocked = "verify_code_blocked"
)

// WeixinCredentials 是扫码成功后要存进机器人配置的凭据。
type WeixinCredentials struct {
	BotToken string
	BotID    string
	BaseURL  string
	// UserID 是扫码人自己的 ilink_user_id，可以直接当主人 ID 用。
	UserID string
}

// WeixinLoginStatus 是一次 Start / Poll 的结果。
type WeixinLoginStatus struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	Message   string `json:"message,omitempty"`
	// QRCodeImage 是 PNG 的 data URL；只在二维码新生成或刷新时带上。
	QRCodeImage string `json:"qrcode_image,omitempty"`
	// QRCodeURL 是二维码里编码的链接，手机上打不开相机时可以直接点。
	QRCodeURL   string             `json:"qrcode_url,omitempty"`
	ExpiresAt   time.Time          `json:"expires_at,omitempty"`
	Credentials *WeixinCredentials `json:"-"`
}

type weixinLoginSession struct {
	id        string
	profileID string
	qrcode    string
	qrContent string
	pollBase  string
	startedAt time.Time
	refreshes int
	busy      bool
}

// WeixinLoginManager 管理进行中的扫码会话。
type WeixinLoginManager struct {
	mu       sync.Mutex
	sessions map[string]*weixinLoginSession
	apiBase  string
	client   *http.Client
	now      func() time.Time
}

// NewWeixinLoginManager 创建扫码登录管理器。
func NewWeixinLoginManager() *WeixinLoginManager {
	return &WeixinLoginManager{
		sessions: map[string]*weixinLoginSession{},
		apiBase:  weixinDefaultAPIBase,
		client:   &http.Client{},
		now:      time.Now,
	}
}

// SetAPIBase 改扫码接口地址，只给测试用。
func (m *WeixinLoginManager) SetAPIBase(base string) {
	m.mu.Lock()
	m.apiBase = base
	m.mu.Unlock()
}

func (m *WeixinLoginManager) purgeLocked() {
	for id, session := range m.sessions {
		if m.now().Sub(session.startedAt) > weixinLoginTTL {
			delete(m.sessions, id)
		}
	}
}

// Start 为某台机器人发起扫码。existingTokens 是本地已有的 bot token，
// 服务端用它认出「这个微信号已经连过这台」，从而回 binded_redirect 而不是重复发号。
func (m *WeixinLoginManager) Start(ctx context.Context, profileID string, existingTokens []string) (WeixinLoginStatus, error) {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return WeixinLoginStatus{}, fmt.Errorf("weixin: 缺少机器人 ID")
	}
	m.mu.Lock()
	m.purgeLocked()
	// 同一台机器人只留一个会话，重复点「扫码」就换一张新码。
	for id, session := range m.sessions {
		if session.profileID == profileID {
			delete(m.sessions, id)
		}
	}
	base := m.apiBase
	m.mu.Unlock()

	qr, content, err := m.fetchQRCode(ctx, base, existingTokens)
	if err != nil {
		return WeixinLoginStatus{}, err
	}
	image, err := weixinQRCodeDataURL(content)
	if err != nil {
		return WeixinLoginStatus{}, err
	}
	session := &weixinLoginSession{
		id:        weixinLoginSessionID(),
		profileID: profileID,
		qrcode:    qr,
		qrContent: content,
		pollBase:  base,
		startedAt: m.now(),
	}
	m.mu.Lock()
	m.sessions[session.id] = session
	m.mu.Unlock()
	return WeixinLoginStatus{
		SessionID:   session.id,
		Status:      WeixinLoginWaiting,
		Message:     "请用手机微信扫描二维码",
		QRCodeImage: image,
		QRCodeURL:   content,
		ExpiresAt:   session.startedAt.Add(weixinLoginTTL),
	}, nil
}

// Poll 代发一轮状态长轮询。verifyCode 是手机上显示、用户抄到页面上的配对数字。
func (m *WeixinLoginManager) Poll(ctx context.Context, profileID, sessionID, verifyCode string) (WeixinLoginStatus, error) {
	m.mu.Lock()
	m.purgeLocked()
	session, ok := m.sessions[strings.TrimSpace(sessionID)]
	if !ok || session.profileID != strings.TrimSpace(profileID) {
		m.mu.Unlock()
		return WeixinLoginStatus{SessionID: sessionID, Status: WeixinLoginExpired, Message: "二维码已失效，请重新生成"}, nil
	}
	if session.busy {
		// 两个标签页同时轮询同一个会话时，让后来的那个先等着，别并发打状态接口。
		m.mu.Unlock()
		return WeixinLoginStatus{SessionID: sessionID, Status: WeixinLoginWaiting}, nil
	}
	session.busy = true
	qr, pollBase := session.qrcode, session.pollBase
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		session.busy = false
		m.mu.Unlock()
	}()

	status, err := m.pollStatus(ctx, pollBase, qr, verifyCode)
	if err != nil {
		// 网关超时、网络抖动都按「还在等」处理，和官方一致；前端会接着轮询。
		return WeixinLoginStatus{SessionID: session.id, Status: WeixinLoginWaiting}, nil
	}
	out := WeixinLoginStatus{SessionID: session.id, Status: status.Status, ExpiresAt: session.startedAt.Add(weixinLoginTTL)}
	switch status.Status {
	case WeixinLoginWaiting, "":
		out.Status = WeixinLoginWaiting
	case WeixinLoginScanned:
		out.Message = "已扫码，请在手机上确认"
	case WeixinLoginNeedCode:
		out.Message = "请输入手机微信上显示的数字"
		if strings.TrimSpace(verifyCode) != "" {
			out.Message = "数字不匹配，请重新输入"
		}
	case weixinLoginRedirect:
		// 服务端让换机房继续轮询，对用户来说仍然是「已扫码」。
		if host := strings.TrimSpace(status.RedirectHost); host != "" {
			m.mu.Lock()
			session.pollBase = "https://" + host
			m.mu.Unlock()
		}
		out.Status = WeixinLoginScanned
		out.Message = "已扫码，请在手机上确认"
	case WeixinLoginExpired, weixinLoginVerifyBlocked:
		return m.refresh(ctx, session, status.Status == weixinLoginVerifyBlocked)
	case WeixinLoginAlreadyBound:
		m.drop(session.id)
		out.Message = "这个微信号已经连接过本机器人，沿用现有登录"
	case WeixinLoginConfirmed:
		m.drop(session.id)
		if strings.TrimSpace(status.BotID) == "" || strings.TrimSpace(status.BotToken) == "" {
			out.Status = WeixinLoginFailed
			out.Message = "登录失败：服务端没有返回 bot 凭据"
			return out, nil
		}
		out.Message = "登录成功"
		out.Credentials = &WeixinCredentials{
			BotToken: strings.TrimSpace(status.BotToken),
			BotID:    strings.TrimSpace(status.BotID),
			BaseURL:  strings.TrimSpace(status.BaseURL),
			UserID:   strings.TrimSpace(status.UserID),
		}
	default:
		out.Status = WeixinLoginWaiting
	}
	return out, nil
}

// refresh 换一张新码。次数用完就结束会话，别无限续下去。
func (m *WeixinLoginManager) refresh(ctx context.Context, session *weixinLoginSession, blocked bool) (WeixinLoginStatus, error) {
	m.mu.Lock()
	session.refreshes++
	exhausted := session.refreshes >= weixinQRMaxRefresh
	base := m.apiBase
	m.mu.Unlock()
	if exhausted {
		m.drop(session.id)
		message := "二维码多次过期，请稍后重新发起"
		if blocked {
			message = "数字多次输错，请稍后重新发起"
		}
		return WeixinLoginStatus{SessionID: session.id, Status: WeixinLoginFailed, Message: message}, nil
	}
	qr, content, err := m.fetchQRCode(ctx, base, nil)
	if err != nil {
		m.drop(session.id)
		return WeixinLoginStatus{SessionID: session.id, Status: WeixinLoginFailed, Message: "刷新二维码失败：" + err.Error()}, nil
	}
	image, err := weixinQRCodeDataURL(content)
	if err != nil {
		return WeixinLoginStatus{}, err
	}
	m.mu.Lock()
	session.qrcode, session.qrContent, session.pollBase = qr, content, base
	session.startedAt = m.now()
	expires := session.startedAt.Add(weixinLoginTTL)
	m.mu.Unlock()
	message := "二维码已过期，已换一张新的，请重新扫描"
	if blocked {
		message = "数字多次输错，已换一张新二维码，请重新扫描"
	}
	return WeixinLoginStatus{SessionID: session.id, Status: WeixinLoginWaiting, Message: message, QRCodeImage: image, QRCodeURL: content, ExpiresAt: expires}, nil
}

func (m *WeixinLoginManager) drop(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

func (m *WeixinLoginManager) fetchQRCode(ctx context.Context, base string, tokens []string) (string, string, error) {
	list := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if token = strings.TrimSpace(token); token != "" {
			list = append(list, token)
		}
	}
	api := weixinAPI{base: base, client: m.client}
	raw, err := api.post(ctx, "ilink/bot/get_bot_qrcode?bot_type="+url.QueryEscape(weixinBotType), map[string]any{"local_token_list": list}, weixinAPITimeout)
	if err != nil {
		return "", "", fmt.Errorf("weixin: 获取登录二维码失败: %w", err)
	}
	var resp struct {
		QRCode  string `json:"qrcode"`
		Content string `json:"qrcode_img_content"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", "", fmt.Errorf("weixin: 解析二维码响应失败: %w", err)
	}
	if strings.TrimSpace(resp.QRCode) == "" || strings.TrimSpace(resp.Content) == "" {
		return "", "", fmt.Errorf("weixin: 服务端没有返回二维码")
	}
	return resp.QRCode, resp.Content, nil
}

type weixinQRStatusResponse struct {
	Status       string `json:"status"`
	BotToken     string `json:"bot_token"`
	BotID        string `json:"ilink_bot_id"`
	BaseURL      string `json:"baseurl"`
	UserID       string `json:"ilink_user_id"`
	RedirectHost string `json:"redirect_host"`
}

func (m *WeixinLoginManager) pollStatus(ctx context.Context, base, qr, verifyCode string) (weixinQRStatusResponse, error) {
	path := "ilink/bot/get_qrcode_status?qrcode=" + url.QueryEscape(qr)
	if code := strings.TrimSpace(verifyCode); code != "" {
		path += "&verify_code=" + url.QueryEscape(code)
	}
	raw, err := weixinAPI{base: base, client: m.client}.get(ctx, path, weixinQRPollTimeout)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return weixinQRStatusResponse{Status: WeixinLoginWaiting}, nil
		}
		return weixinQRStatusResponse{}, err
	}
	var status weixinQRStatusResponse
	if err := json.Unmarshal(raw, &status); err != nil {
		return weixinQRStatusResponse{}, err
	}
	return status, nil
}

func weixinQRCodeDataURL(content string) (string, error) {
	png, err := qrcode.Encode(content, qrcode.Medium, 256)
	if err != nil {
		return "", fmt.Errorf("weixin: 生成二维码图片失败: %w", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

func weixinLoginSessionID() string {
	var buf [12]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}
