// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/version"
)

// 这里是腾讯微信 iLink Bot 接口（ilinkai.weixin.qq.com）的客户端。
//
// 协议没有公开文档，字段、请求头和状态值全部照着腾讯官方发布的
// @tencent-weixin/openclaw-weixin（MIT，本实现对照 2.4.9）的源码实现：
// src/api/api.ts、src/api/types.ts、src/auth/login-qr.ts、src/cdn/*。
// 对不上时以那份源码为准，别凭感觉改字段名。

const (
	weixinDefaultAPIBase = "https://ilinkai.weixin.qq.com"
	weixinDefaultCDNBase = "https://novac2c.cdn.weixin.qq.com/c2c"
	// weixinAppID 对应官方包 package.json 里的 ilink_appid。
	weixinAppID = "bot"
	// weixinBotType 是扫码接口的 bot_type，官方渠道构建固定为 3。
	weixinBotType = "3"
	// weixinProtocolVersion 是实现所对照的官方包版本。iLink-App-ClientVersion
	// 按它编码，声明的是「协议兼容到哪一版」，身份另由 bot_agent 表明是 Diana。
	weixinProtocolVersion = "2.4.9"

	weixinLongPollTimeout = 35 * time.Second
	weixinAPITimeout      = 15 * time.Second
	weixinLightTimeout    = 10 * time.Second

	// weixinStaleTokenErrCode 是服务端判定 bot token 失效时返回的错误码。
	weixinStaleTokenErrCode = -14

	weixinMessageTypeUser = 1
	weixinMessageTypeBot  = 2
	weixinMessageStateEnd = 2

	weixinItemText  = 1
	weixinItemImage = 2
	weixinItemVoice = 3
	weixinItemFile  = 4
	weixinItemVideo = 5

	weixinUploadMediaImage = 1

	weixinMediaMaxBytes = 20 << 20
)

// weixinID 兼容数字和字符串两种写法。message_id 在线上是 uint64，
// 按 float64 解码会丢精度，所以一律保留原文。
type weixinID string

func (id *weixinID) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		*id = ""
		return nil
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return err
		}
		*id = weixinID(strings.TrimSpace(text))
		return nil
	}
	*id = weixinID(string(raw))
	return nil
}

type weixinCDNMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param,omitempty"`
	AESKey            string `json:"aes_key,omitempty"`
	EncryptType       int    `json:"encrypt_type,omitempty"`
	FullURL           string `json:"full_url,omitempty"`
}

type weixinImageItem struct {
	Media      *weixinCDNMedia `json:"media,omitempty"`
	ThumbMedia *weixinCDNMedia `json:"thumb_media,omitempty"`
	// AESKey 是十六进制的原始密钥，入站解密时优先于 media.aes_key。
	AESKey  string `json:"aeskey,omitempty"`
	URL     string `json:"url,omitempty"`
	MidSize int64  `json:"mid_size,omitempty"`
}

type weixinMessageItem struct {
	Type     int      `json:"type,omitempty"`
	MsgID    weixinID `json:"msg_id,omitempty"`
	TextItem *struct {
		Text string `json:"text,omitempty"`
	} `json:"text_item,omitempty"`
	ImageItem *weixinImageItem `json:"image_item,omitempty"`
	VoiceItem *struct {
		// Text 是服务端给的语音转文字结果。
		Text string `json:"text,omitempty"`
	} `json:"voice_item,omitempty"`
	FileItem *struct {
		FileName string `json:"file_name,omitempty"`
	} `json:"file_item,omitempty"`
	RefMsg *struct {
		Title       string             `json:"title,omitempty"`
		SvrID       weixinID           `json:"svr_id,omitempty"`
		MessageItem *weixinMessageItem `json:"message_item,omitempty"`
	} `json:"ref_msg,omitempty"`
}

type weixinMessage struct {
	Seq          int64               `json:"seq,omitempty"`
	MessageID    weixinID            `json:"message_id,omitempty"`
	FromUserID   string              `json:"from_user_id"`
	ToUserID     string              `json:"to_user_id,omitempty"`
	ClientID     string              `json:"client_id,omitempty"`
	CreateTimeMs int64               `json:"create_time_ms,omitempty"`
	SessionID    string              `json:"session_id,omitempty"`
	GroupID      string              `json:"group_id,omitempty"`
	MessageType  int                 `json:"message_type,omitempty"`
	MessageState int                 `json:"message_state,omitempty"`
	ItemList     []weixinMessageItem `json:"item_list,omitempty"`
	ContextToken string              `json:"context_token,omitempty"`
}

type weixinGetUpdatesResponse struct {
	Ret                  int             `json:"ret"`
	ErrCode              int             `json:"errcode"`
	ErrMsg               string          `json:"errmsg"`
	Msgs                 []weixinMessage `json:"msgs"`
	GetUpdatesBuf        string          `json:"get_updates_buf"`
	LongPollingTimeoutMs int64           `json:"longpolling_timeout_ms"`
}

type weixinBaseInfo struct {
	ChannelVersion string `json:"channel_version,omitempty"`
	BotAgent       string `json:"bot_agent,omitempty"`
}

// weixinAPIError 是业务层失败（ret/errcode 非零），区别于网络层失败。
type weixinAPIError struct {
	Op      string
	Ret     int
	ErrCode int
	ErrMsg  string
}

func (e *weixinAPIError) Error() string {
	return fmt.Sprintf("weixin: %s 被拒绝: ret=%d errcode=%d %s", e.Op, e.Ret, e.ErrCode, strings.TrimSpace(e.ErrMsg))
}

// staleToken 判断是不是 bot token 失效。官方实现 ret 和 errcode 两处都认。
func (e *weixinAPIError) staleToken() bool {
	return e.Ret == weixinStaleTokenErrCode || e.ErrCode == weixinStaleTokenErrCode
}

func isWeixinStaleToken(err error) bool {
	var apiErr *weixinAPIError
	return errors.As(err, &apiErr) && apiErr.staleToken()
}

// weixinAPI 是一个带着 token 的 iLink 接口客户端。
type weixinAPI struct {
	base   string
	token  string
	client *http.Client
}

func weixinClientVersion(v string) string {
	parts := strings.Split(v, ".")
	var out uint32
	for i := 0; i < 3; i++ {
		n := 0
		if i < len(parts) {
			n, _ = strconv.Atoi(parts[i])
		}
		out = out<<8 | uint32(n&0xff)
	}
	return strconv.FormatUint(uint64(out), 10)
}

func weixinBaseInfoPayload() weixinBaseInfo {
	return weixinBaseInfo{
		ChannelVersion: weixinProtocolVersion,
		BotAgent:       "Diana/" + weixinBotAgentVersion(version.Source()),
	}
}

// weixinBotAgentVersion 把版本号收敛到 bot_agent 语法允许的字符集里，
// 否则服务端会把整个 token 丢掉，退回默认值。
func weixinBotAgentVersion(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	var b strings.Builder
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("_.+-", r) {
			b.WriteRune(r)
		}
		if b.Len() >= 32 {
			break
		}
	}
	if b.Len() == 0 {
		return "dev"
	}
	return b.String()
}

// weixinRandomUIN 生成 X-WECHAT-UIN：随机 uint32 的十进制串再做 base64。
func weixinRandomUIN() string {
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	return base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(binary.BigEndian.Uint32(buf[:])), 10)))
}

func (a weixinAPI) endpoint(path string) string {
	base := strings.TrimRight(strings.TrimSpace(a.base), "/")
	if base == "" {
		base = weixinDefaultAPIBase
	}
	return base + "/" + strings.TrimLeft(path, "/")
}

func (a weixinAPI) httpClient() *http.Client {
	if a.client != nil {
		return a.client
	}
	return http.DefaultClient
}

func (a weixinAPI) commonHeaders(req *http.Request) {
	req.Header.Set("iLink-App-Id", weixinAppID)
	req.Header.Set("iLink-App-ClientVersion", weixinClientVersion(weixinProtocolVersion))
}

// post 发一次 JSON POST。timeout 是这一次调用自己的超时，不依赖 client 的全局超时，
// 因为长轮询和普通调用需要的时长差了一个量级。
func (a weixinAPI) post(ctx context.Context, path string, payload any, timeout time.Duration) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, a.endpoint(path), bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("weixin: invalid request URL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AuthorizationType", "ilink_bot_token")
	req.Header.Set("X-WECHAT-UIN", weixinRandomUIN())
	a.commonHeaders(req)
	if token := strings.TrimSpace(a.token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return a.do(req, path)
}

func (a weixinAPI) get(ctx context.Context, path string, timeout time.Duration) ([]byte, error) {
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, a.endpoint(path), nil)
	if err != nil {
		return nil, fmt.Errorf("weixin: invalid request URL")
	}
	a.commonHeaders(req)
	return a.do(req, path)
}

func (a weixinAPI) do(req *http.Request, path string) ([]byte, error) {
	resp, err := a.httpClient().Do(req)
	if err != nil {
		// 报错里只留路径，token 在请求头里，不会跟着 URL 漏出去。
		op, _, _ := strings.Cut(path, "?")
		return nil, fmt.Errorf("weixin: %s: %w", op, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, platformHTTPResponseLimit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		op, _, _ := strings.Cut(path, "?")
		return raw, fmt.Errorf("weixin: %s: http %d: %s", op, resp.StatusCode, strings.TrimSpace(truncateForError(string(raw))))
	}
	return raw, nil
}

// getUpdates 长轮询收消息。客户端超时是长轮询的正常结局，按「没有新消息」处理，
// 游标原样带回，调用方直接发下一轮。
func (a weixinAPI) getUpdates(ctx context.Context, cursor string, timeout time.Duration) (weixinGetUpdatesResponse, error) {
	raw, err := a.post(ctx, "ilink/bot/getupdates", map[string]any{
		"get_updates_buf": cursor,
		"base_info":       weixinBaseInfoPayload(),
	}, timeout)
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return weixinGetUpdatesResponse{GetUpdatesBuf: cursor}, nil
		}
		return weixinGetUpdatesResponse{}, err
	}
	var resp weixinGetUpdatesResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return weixinGetUpdatesResponse{}, fmt.Errorf("weixin: 解析 getupdates 响应失败: %w", err)
	}
	if resp.Ret != 0 || resp.ErrCode != 0 {
		return resp, &weixinAPIError{Op: "getupdates", Ret: resp.Ret, ErrCode: resp.ErrCode, ErrMsg: resp.ErrMsg}
	}
	return resp, nil
}

// sendMessage 发一条消息，返回服务端分配的 message_id。
func (a weixinAPI) sendMessage(ctx context.Context, msg weixinMessage) (string, error) {
	raw, err := a.post(ctx, "ilink/bot/sendmessage", map[string]any{
		"msg":       msg,
		"base_info": weixinBaseInfoPayload(),
	}, weixinAPITimeout)
	if err != nil {
		return "", err
	}
	var resp struct {
		Ret       int      `json:"ret"`
		ErrCode   int      `json:"errcode"`
		ErrMsg    string   `json:"errmsg"`
		MessageID weixinID `json:"message_id"`
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &resp); err != nil {
			return "", fmt.Errorf("weixin: 解析 sendmessage 响应失败: %w", err)
		}
	}
	if resp.Ret != 0 || resp.ErrCode != 0 {
		return "", &weixinAPIError{Op: "sendmessage", Ret: resp.Ret, ErrCode: resp.ErrCode, ErrMsg: resp.ErrMsg}
	}
	return string(resp.MessageID), nil
}

// notify 调 notifystart / notifystop。只是告诉服务端客户端上下线，失败不影响收发。
func (a weixinAPI) notify(ctx context.Context, action string) {
	_, _ = a.post(ctx, "ilink/bot/msg/"+action, map[string]any{"base_info": weixinBaseInfoPayload()}, weixinLightTimeout)
}

type weixinUploadURLResponse struct {
	Ret           int    `json:"ret"`
	ErrCode       int    `json:"errcode"`
	ErrMsg        string `json:"errmsg"`
	UploadParam   string `json:"upload_param"`
	UploadFullURL string `json:"upload_full_url"`
}

// weixinUploaded 是一次 CDN 上传的结果，发图消息时原样填进 image_item。
type weixinUploaded struct {
	DownloadParam  string
	AESKey         []byte
	CiphertextSize int64
}

// uploadImage 走官方的三步上传：getuploadurl 拿上传地址 → 本地 AES-128-ECB 加密
// 后 POST 到 CDN → 从响应头 x-encrypted-param 取回下载参数。
func (a weixinAPI) uploadImage(ctx context.Context, cdnBase, toUserID string, plaintext []byte) (weixinUploaded, error) {
	key := make([]byte, 16)
	fileKey := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return weixinUploaded{}, err
	}
	if _, err := rand.Read(fileKey); err != nil {
		return weixinUploaded{}, err
	}
	ciphertext, err := weixinEncryptAESECB(plaintext, key)
	if err != nil {
		return weixinUploaded{}, err
	}
	fileKeyHex := hex.EncodeToString(fileKey)
	raw, err := a.post(ctx, "ilink/bot/getuploadurl", map[string]any{
		"filekey":       fileKeyHex,
		"media_type":    weixinUploadMediaImage,
		"to_user_id":    toUserID,
		"rawsize":       len(plaintext),
		"rawfilemd5":    weixinMD5Hex(plaintext),
		"filesize":      len(ciphertext),
		"no_need_thumb": true,
		"aeskey":        hex.EncodeToString(key),
		"base_info":     weixinBaseInfoPayload(),
	}, weixinAPITimeout)
	if err != nil {
		return weixinUploaded{}, err
	}
	var upload weixinUploadURLResponse
	if err := json.Unmarshal(raw, &upload); err != nil {
		return weixinUploaded{}, fmt.Errorf("weixin: 解析 getuploadurl 响应失败: %w", err)
	}
	if upload.Ret != 0 || upload.ErrCode != 0 {
		return weixinUploaded{}, &weixinAPIError{Op: "getuploadurl", Ret: upload.Ret, ErrCode: upload.ErrCode, ErrMsg: upload.ErrMsg}
	}
	target := strings.TrimSpace(upload.UploadFullURL)
	if target == "" {
		if upload.UploadParam == "" {
			return weixinUploaded{}, fmt.Errorf("weixin: getuploadurl 没有返回上传地址")
		}
		target = strings.TrimRight(firstNonEmpty(cdnBase, weixinDefaultCDNBase), "/") +
			"/upload?encrypted_query_param=" + url.QueryEscape(upload.UploadParam) + "&filekey=" + url.QueryEscape(fileKeyHex)
	}
	downloadParam, err := a.postCDN(ctx, target, ciphertext)
	if err != nil {
		return weixinUploaded{}, err
	}
	return weixinUploaded{DownloadParam: downloadParam, AESKey: key, CiphertextSize: int64(len(ciphertext))}, nil
}

func (a weixinAPI) postCDN(ctx context.Context, target string, ciphertext []byte) (string, error) {
	var lastErr error
	// 官方实现对 5xx 和网络错误重试三次，4xx 直接放弃。
	for attempt := 0; attempt < 3; attempt++ {
		callCtx, cancel := context.WithTimeout(ctx, weixinAPITimeout)
		req, err := http.NewRequestWithContext(callCtx, http.MethodPost, target, bytes.NewReader(ciphertext))
		if err != nil {
			cancel()
			return "", fmt.Errorf("weixin: invalid CDN URL")
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := a.httpClient().Do(req)
		if err != nil {
			cancel()
			lastErr = fmt.Errorf("weixin: CDN 上传失败: %w", err)
			if ctx.Err() != nil {
				return "", lastErr
			}
			continue
		}
		param := resp.Header.Get("x-encrypted-param")
		message := resp.Header.Get("x-error-message")
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		cancel()
		switch {
		case resp.StatusCode >= 400 && resp.StatusCode < 500:
			return "", fmt.Errorf("weixin: CDN 拒绝上传: http %d %s", resp.StatusCode, message)
		case resp.StatusCode != http.StatusOK:
			lastErr = fmt.Errorf("weixin: CDN 上传失败: http %d %s", resp.StatusCode, message)
		case param == "":
			lastErr = fmt.Errorf("weixin: CDN 响应缺少 x-encrypted-param")
		default:
			return param, nil
		}
	}
	return "", lastErr
}

// downloadMedia 取回入站媒体并解密。
func (a weixinAPI) downloadMedia(ctx context.Context, cdnBase string, media *weixinCDNMedia, hexKey string) ([]byte, error) {
	if media == nil || (media.EncryptQueryParam == "" && media.FullURL == "") {
		return nil, fmt.Errorf("weixin: 媒体缺少下载参数")
	}
	target := strings.TrimSpace(media.FullURL)
	if target == "" {
		target = strings.TrimRight(firstNonEmpty(cdnBase, weixinDefaultCDNBase), "/") + "/download?encrypted_query_param=" + url.QueryEscape(media.EncryptQueryParam)
	}
	callCtx, cancel := context.WithTimeout(ctx, weixinAPITimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("weixin: invalid CDN URL")
	}
	resp, err := a.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("weixin: CDN 下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("weixin: CDN 下载失败: http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, weixinMediaMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > weixinMediaMaxBytes {
		return nil, fmt.Errorf("weixin: 媒体超过 %d MiB", weixinMediaMaxBytes>>20)
	}
	key, err := weixinMediaKey(hexKey, media.AESKey)
	if err != nil {
		return nil, err
	}
	if key == nil {
		// 两处都没给密钥时官方按明文处理。
		return body, nil
	}
	return weixinDecryptAESECB(body, key)
}

// weixinMediaKey 还原 16 字节 AES 密钥。
//
// 线上见过两种写法：image_item.aeskey 是十六进制原文；media.aes_key 是 base64，
// 解出来要么直接是 16 字节，要么是 32 个十六进制字符（文件、语音、视频）。
func weixinMediaKey(hexKey, base64Key string) ([]byte, error) {
	if hexKey = strings.TrimSpace(hexKey); hexKey != "" {
		key, err := hex.DecodeString(hexKey)
		if err != nil || len(key) != 16 {
			return nil, fmt.Errorf("weixin: image aeskey 非法")
		}
		return key, nil
	}
	if base64Key = strings.TrimSpace(base64Key); base64Key == "" {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(base64Key)
	if err != nil {
		return nil, fmt.Errorf("weixin: aes_key 不是合法 base64")
	}
	if len(decoded) == 16 {
		return decoded, nil
	}
	if len(decoded) == 32 {
		if key, err := hex.DecodeString(string(decoded)); err == nil {
			return key, nil
		}
	}
	return nil, fmt.Errorf("weixin: aes_key 长度非法 (%d)", len(decoded))
}

func weixinEncryptAESECB(plaintext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padding := aes.BlockSize - len(plaintext)%aes.BlockSize
	padded := make([]byte, len(plaintext), len(plaintext)+padding)
	copy(padded, plaintext)
	padded = append(padded, bytes.Repeat([]byte{byte(padding)}, padding)...)
	out := make([]byte, len(padded))
	for i := 0; i < len(padded); i += aes.BlockSize {
		block.Encrypt(out[i:i+aes.BlockSize], padded[i:i+aes.BlockSize])
	}
	return out, nil
}

func weixinMD5Hex(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}

func weixinDecryptAESECB(ciphertext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("weixin: 密文长度非法")
	}
	out := make([]byte, len(ciphertext))
	for i := 0; i < len(ciphertext); i += aes.BlockSize {
		block.Decrypt(out[i:i+aes.BlockSize], ciphertext[i:i+aes.BlockSize])
	}
	return stripPKCS7(out)
}
