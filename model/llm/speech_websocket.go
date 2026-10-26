// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// 只有 WebSocket 接口的两家：Edge 朗读和百炼的 CosyVoice / Sambert。

const (
	edgeSpeechURL = "wss://speech.platform.bing.com/consumer/speech/synthesize/readaloud/edge/v1"
	// edgeTrustedClientToken 是 Edge 浏览器内置的朗读令牌，edge-tts 等项目都用它。
	edgeTrustedClientToken = "6A5AA1D4EAFF4E9FB37E23D68491D6F4"
	// edgeChromiumVersion 要和 Sec-MS-GEC-Version 对得上；服务端拒绝时可以用
	// edge_version 选项换成新版本号，不用等发版。
	edgeChromiumVersion = "143.0.3650.75"
	dashScopeSpeechPath = "/api-ws/v1/inference"
	// windowsEpochOffset 是 1601-01-01 到 1970-01-01 的秒数，Sec-MS-GEC 按 Windows 时间算。
	windowsEpochOffset = 11644473600
)

func speechWebsocketDialer(timeout time.Duration) *websocket.Dialer {
	return &websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: timeout}
}

// websocketURL 把配置档里的 http(s) 地址换成 ws(s)；测试用本地地址代替官方服务。
func websocketURL(base, fallback, path string) string {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil || parsed.Host == "" {
		return fallback
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	}
	if path != "" {
		parsed.Path = path
	}
	parsed.RawQuery = ""
	return parsed.String()
}

// edgeSecMSGEC 是 Edge 朗读接口要求的时间令牌：Windows 时间按五分钟取整，
// 换成 100 纳秒单位后和客户端令牌一起做 SHA-256。
func edgeSecMSGEC(now time.Time) string {
	seconds := now.Unix() + windowsEpochOffset
	seconds -= seconds % 300
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d%s", seconds*10000000, edgeTrustedClientToken)))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// synthesizeEdge 用 Edge 浏览器的「大声朗读」合成，免费、不要 Key，只出 MP3。
func synthesizeEdge(ctx context.Context, cfg ProviderConfig, req SpeechRequest, _ []ClientOption) ([]byte, string, error) {
	version := firstNonEmptyString(req.option("edge_version"), edgeChromiumVersion)
	major, _, _ := strings.Cut(version, ".")
	endpoint := websocketURL(cfg.BaseURL, edgeSpeechURL, "")
	if strings.TrimSpace(cfg.BaseURL) == "" || strings.Contains(cfg.BaseURL, "speech.platform.bing.com") {
		endpoint = edgeSpeechURL
	}
	header := http.Header{
		"Pragma":          {"no-cache"},
		"Cache-Control":   {"no-cache"},
		"Origin":          {"chrome-extension://jdiccldimpdaibmpdkjnbmckianbfold"},
		"User-Agent":      {fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s.0.0.0 Safari/537.36 Edg/%s.0.0.0", major, major)},
		"Accept-Language": {"en-US,en;q=0.9"},
		"Cookie":          {"muid=" + strings.ToUpper(randomHex(16)) + ";"},
	}
	ctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	var skew time.Duration
	var conn *websocket.Conn
	for attempt := 0; ; attempt++ {
		query := url.Values{
			"TrustedClientToken": {edgeTrustedClientToken},
			"ConnectionId":       {strings.ReplaceAll(uuid.NewString(), "-", "")},
			"Sec-MS-GEC":         {edgeSecMSGEC(time.Now().Add(skew))},
			"Sec-MS-GEC-Version": {"1-" + version},
		}
		var resp *http.Response
		var err error
		conn, resp, err = speechWebsocketDialer(req.Timeout).DialContext(ctx, endpoint+"?"+query.Encode(), header)
		if err == nil {
			break
		}
		// 本机时钟偏了令牌就对不上，服务端回 403 时带着它的时间，校准后再试一次。
		if resp != nil && resp.StatusCode == http.StatusForbidden && attempt == 0 {
			if serverTime, parseErr := http.ParseTime(resp.Header.Get("Date")); parseErr == nil {
				skew = time.Until(serverTime)
				continue
			}
		}
		return nil, "", websocketDialError(ctx, err, resp)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	timestamp := time.Now().UTC().Format("Mon Jan 02 2006 15:04:05 GMT+0000 (Coordinated Universal Time)")
	config := "X-Timestamp:" + timestamp + "\r\nContent-Type:application/json; charset=utf-8\r\nPath:speech.config\r\n\r\n" +
		`{"context":{"synthesis":{"audio":{"metadataoptions":{"sentenceBoundaryEnabled":"false","wordBoundaryEnabled":"false"},"outputFormat":"audio-24khz-48kbitrate-mono-mp3"}}}}` + "\r\n"
	if err := conn.WriteMessage(websocket.TextMessage, []byte(config)); err != nil {
		return nil, "", classifyMediaTransportError(ctx, "speech", err)
	}
	voice := firstNonEmptyString(req.Voice, defaultEdgeVoice)
	ssml := fmt.Sprintf("<speak version='1.0' xmlns='http://www.w3.org/2001/10/synthesis' xml:lang='%s'><voice name='%s'><prosody pitch='%s' rate='%s' volume='%s'>%s</prosody></voice></speak>",
		languageOfVoice(voice, "zh-CN"), xmlEscape(edgeVoiceName(voice)),
		firstNonEmptyString(req.option("pitch"), "+0Hz"), ratePercent(req.Speed), firstNonEmptyString(req.option("volume"), "+0%"), xmlEscape(req.Input))
	request := "X-RequestId:" + strings.ReplaceAll(uuid.NewString(), "-", "") + "\r\nContent-Type:application/ssml+xml\r\nX-Timestamp:" + timestamp + "Z\r\nPath:ssml\r\n\r\n" + ssml
	if err := conn.WriteMessage(websocket.TextMessage, []byte(request)); err != nil {
		return nil, "", classifyMediaTransportError(ctx, "speech", err)
	}
	var audio bytes.Buffer
	for {
		kind, message, err := conn.ReadMessage()
		if err != nil {
			return nil, "", classifyMediaTransportError(ctx, "speech", err)
		}
		switch kind {
		case websocket.TextMessage:
			if bytes.Contains(message, []byte("Path:turn.end")) {
				if audio.Len() == 0 {
					return nil, "", badSpeechResponse(0, "Edge 朗读没有返回音频，音色名可能不存在："+voice)
				}
				return audio.Bytes(), "mp3", nil
			}
		case websocket.BinaryMessage:
			// 二进制帧开头两字节是头部长度，头部之后才是音频。
			if len(message) < 2 {
				continue
			}
			headerLength := int(binary.BigEndian.Uint16(message[:2]))
			if 2+headerLength > len(message) {
				continue
			}
			if bytes.Contains(message[2:2+headerLength], []byte("Path:audio")) {
				audio.Write(message[2+headerLength:])
			}
		}
		if audio.Len() > maxSpeechAudioBytes {
			return nil, "", badSpeechResponse(0, "Edge 朗读返回的音频超过上限")
		}
	}
}

// edgeVoiceName 把 zh-CN-XiaoxiaoNeural 写成服务端要的长名字。
func edgeVoiceName(voice string) string {
	if strings.Contains(voice, "Microsoft Server Speech") {
		return voice
	}
	parts := strings.Split(voice, "-")
	if len(parts) < 3 {
		return voice
	}
	region := strings.Join(parts[:2], "-")
	name := strings.Join(parts[2:], "-")
	return fmt.Sprintf("Microsoft Server Speech Text to Speech Voice (%s, %s)", region, name)
}

func websocketDialError(ctx context.Context, err error, resp *http.Response) error {
	if resp == nil {
		return classifyMediaTransportError(ctx, "speech", err)
	}
	return &MediaAPIError{Op: "speech", Kind: classifyMediaStatus(resp.StatusCode, ""), StatusCode: resp.StatusCode, Err: err}
}

// synthesizeDashScopeStream 走百炼的 WebSocket 语音合成（CosyVoice、Sambert）：
// run-task 等 task-started，再把整段文字 continue-task 发过去，finish-task 后收齐
// 二进制音频直到 task-finished。
func synthesizeDashScopeStream(ctx context.Context, client *mediaHTTP, cfg ProviderConfig, req SpeechRequest) ([]byte, string, error) {
	if strings.TrimSpace(req.Voice) == "" {
		return nil, "", errors.New("llm: dashscope cosyvoice speech requires a voice")
	}
	format := formatOr(req.Format, "mp3", "mp3", "wav", "pcm", "opus")
	endpoint := websocketURL(client.baseURL, "wss://dashscope.aliyuncs.com"+dashScopeSpeechPath, dashScopeSpeechPath)
	ctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	header := http.Header{"Authorization": {"bearer " + strings.TrimSpace(cfg.APIKey)}}
	for name, value := range cfg.NormalizedHeaders() {
		header.Set(name, value)
	}
	conn, resp, err := speechWebsocketDialer(req.Timeout).DialContext(ctx, endpoint, header)
	if err != nil {
		return nil, "", websocketDialError(ctx, err, resp)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	taskID := uuid.NewString()
	send := func(action string, payload map[string]any) error {
		message := map[string]any{"header": map[string]any{"action": action, "task_id": taskID, "streaming": "duplex"}, "payload": payload}
		if err := conn.WriteJSON(message); err != nil {
			return classifyMediaTransportError(ctx, "speech", err)
		}
		return nil
	}
	rate := 1.0
	if req.Speed > 0 {
		rate = req.Speed
	}
	parameters := map[string]any{"text_type": "PlainText", "voice": req.Voice, "format": format, "sample_rate": 22050, "volume": 50, "rate": rate, "pitch": 1}
	if err := send("run-task", map[string]any{"task_group": "audio", "task": "tts", "function": "SpeechSynthesizer", "model": req.Model, "parameters": parameters, "input": map[string]any{}}); err != nil {
		return nil, "", err
	}
	var audio bytes.Buffer
	started := false
	for {
		kind, message, err := conn.ReadMessage()
		if err != nil {
			return nil, "", classifyMediaTransportError(ctx, "speech", err)
		}
		if kind == websocket.BinaryMessage {
			audio.Write(message)
			if audio.Len() > maxSpeechAudioBytes {
				return nil, "", badSpeechResponse(0, "百炼返回的音频超过上限")
			}
			continue
		}
		var event struct {
			Header struct {
				Event        string `json:"event"`
				ErrorCode    string `json:"error_code"`
				ErrorMessage string `json:"error_message"`
			} `json:"header"`
		}
		if json.Unmarshal(message, &event) != nil {
			continue
		}
		switch event.Header.Event {
		case "task-started":
			if started {
				continue
			}
			started = true
			if err := send("continue-task", map[string]any{"input": map[string]any{"text": req.Input}}); err != nil {
				return nil, "", err
			}
			if err := send("finish-task", map[string]any{"input": map[string]any{}}); err != nil {
				return nil, "", err
			}
		case "task-finished":
			if format == "opus" {
				format = "ogg"
			}
			return audio.Bytes(), format, nil
		case "task-failed":
			detail := "百炼 " + event.Header.ErrorCode + ": " + event.Header.ErrorMessage
			kind := classifyMediaStatus(http.StatusBadRequest, detail)
			if strings.Contains(strings.ToLower(event.Header.ErrorCode), "throttl") {
				kind = MediaErrorRateLimit
			} else if strings.Contains(strings.ToLower(event.Header.ErrorCode), "apikey") || strings.Contains(strings.ToLower(event.Header.ErrorCode), "access") {
				kind = MediaErrorAuth
			}
			return nil, "", &MediaAPIError{Op: "speech", Kind: kind, Detail: detail}
		}
	}
}
