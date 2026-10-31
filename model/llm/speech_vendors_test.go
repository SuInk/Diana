// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// vendorServer 起一个假服务，handler 里断言请求形状并回包。
func vendorServer(t *testing.T, handler func(t *testing.T, w http.ResponseWriter, r *http.Request, body []byte)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		handler(t, w, r, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func decodeJSONBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("request body is not JSON: %v: %s", err, body)
	}
	return out
}

func synthesize(t *testing.T, cfg ProviderConfig, req SpeechRequest) *SpeechResponse {
	t.Helper()
	noMediaRetryWait(t)
	resp, err := SynthesizeSpeech(context.Background(), cfg, req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestResolveSpeechAPIDetectsVendorHosts(t *testing.T) {
	for base, want := range map[string]SpeechAPI{
		"https://api.breeze.blue/v1":                          SpeechAPIBreeze,
		"https://api.breeze.blue.evil.example/v1":             SpeechAPIOpenAI,
		"https://api.xiaomimimo.com/v1":                       SpeechAPIMiMo,
		"https://api.minimax.io/v1":                           SpeechAPIMiniMax,
		"https://openspeech.bytedance.com/api/v1/tts":         SpeechAPIVolcengine,
		"https://dashscope.aliyuncs.com/compatible-mode/v1":   SpeechAPIDashScope,
		"https://api.fish.audio/v1":                           SpeechAPIFishAudio,
		"https://eastus.tts.speech.microsoft.com":             SpeechAPIAzure,
		"https://generativelanguage.googleapis.com/v1beta":    SpeechAPIGemini,
		"https://translate.google.com.hk":                     SpeechAPIGoogleTranslate,
		"https://api.siliconflow.cn/v1":                       SpeechAPIOpenAI,
		"https://notminimax.io.example.com/v1":                SpeechAPIOpenAI,
		"https://speech.platform.bing.com/consumer/speech/v1": SpeechAPIEdge,
	} {
		if got := ResolveSpeechAPI("", ProviderConfig{BaseURL: base}); got != want {
			t.Errorf("%s -> %s, want %s", base, got, want)
		}
	}
	if got := ResolveSpeechAPI("gpt_sovits", ProviderConfig{BaseURL: "https://api.minimax.io"}); got != SpeechAPIGPTSoVITS {
		t.Fatalf("explicit api must win, got %s", got)
	}
}

func TestGPTSoVITSKeepsPluginPayloadAndNeedsNoKey(t *testing.T) {
	server := vendorServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body []byte) {
		if r.URL.Path != "/tts" || r.Header.Get("Authorization") != "" {
			t.Errorf("path=%s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		payload := decodeJSONBody(t, body)
		if payload["ref_audio_path"] != "/ref.wav" || payload["prompt_text"] != "参考" || payload["text_lang"] != "zh" || payload["speed_factor"] != 1.2 {
			t.Errorf("payload = %#v", payload)
		}
		_, _ = w.Write([]byte("RIFF0000WAVEfmt "))
	})
	// 地址填到 /tts 本身也要能用，不能拼成 /tts/tts。
	for _, base := range []string{server.URL, server.URL + "/tts"} {
		resp := synthesize(t, ProviderConfig{Provider: ProviderOpenAICompatible, BaseURL: base}, SpeechRequest{
			API: SpeechAPIGPTSoVITS, Model: "gpt-sovits", Input: "你好", Speed: 1.2,
			Options: map[string]string{"ref_audio_path": "/ref.wav", "prompt_text": "参考"},
		})
		if resp.Format != "wav" {
			t.Fatalf("format = %s", resp.Format)
		}
	}
}

func TestMiniMaxDecodesHexAudioAndReportsBaseResp(t *testing.T) {
	server := vendorServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body []byte) {
		if r.URL.Path != "/v1/t2a_v2" || r.URL.Query().Get("GroupId") != "g1" || r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("path=%s query=%s auth=%q", r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"))
		}
		payload := decodeJSONBody(t, body)
		voice := payload["voice_setting"].(map[string]any)
		if voice["voice_id"] == "bad" {
			_, _ = w.Write([]byte(`{"base_resp":{"status_code":1004,"status_msg":"invalid key"}}`))
			return
		}
		if voice["voice_id"] != "female-shaonv" || payload["model"] != "speech-02-turbo" {
			t.Errorf("payload = %#v", payload)
		}
		_, _ = w.Write([]byte(`{"data":{"audio":"` + hex.EncodeToString([]byte("ID3audio")) + `"},"base_resp":{"status_code":0}}`))
	})
	cfg := mediaTestConfig(server.URL)
	resp := synthesize(t, cfg, SpeechRequest{API: SpeechAPIMiniMax, Model: "speech-02-turbo", Voice: "female-shaonv", Input: "你好", Options: map[string]string{"group_id": "g1"}})
	if string(resp.Audio) != "ID3audio" || resp.Format != "mp3" {
		t.Fatalf("resp = %#v", resp)
	}
	_, err := SynthesizeSpeech(context.Background(), cfg, SpeechRequest{API: SpeechAPIMiniMax, Voice: "bad", Input: "x", Options: map[string]string{"group_id": "g1"}})
	if MediaErrorKindOf(err) != MediaErrorAuth || !strings.Contains(err.Error(), "invalid key") {
		t.Fatalf("err = %v", err)
	}
}

func TestVolcengineV1AndV3(t *testing.T) {
	server := vendorServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body []byte) {
		payload := decodeJSONBody(t, body)
		switch r.URL.Path {
		case "/api/v1/tts":
			app := payload["app"].(map[string]any)
			audio := payload["audio"].(map[string]any)
			if r.Header.Get("Authorization") != "Bearer; tok" || app["appid"] != "app1" || app["token"] != "tok" || app["cluster"] != "volcano_tts" || audio["voice_type"] != "BV001" {
				t.Errorf("v1 auth=%q payload=%#v", r.Header.Get("Authorization"), payload)
			}
			_, _ = w.Write([]byte(`{"code":3000,"message":"Success","data":"` + base64.StdEncoding.EncodeToString([]byte("v1audio")) + `"}`))
		case "/api/v3/tts/unidirectional":
			if r.Header.Get("X-Api-App-Id") != "app1" || r.Header.Get("X-Api-Access-Key") != "tok" || r.Header.Get("X-Api-Resource-Id") != "seed-tts-2.0" {
				t.Errorf("v3 headers = %#v", r.Header)
			}
			for _, part := range []string{"v3", "audio"} {
				_, _ = w.Write([]byte(`{"code":0,"data":"` + base64.StdEncoding.EncodeToString([]byte(part)) + `"}` + "\n"))
			}
			_, _ = w.Write([]byte(`{"code":20000000,"message":"ok","data":null}` + "\n"))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	v1 := synthesize(t, ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "tok", BaseURL: server.URL + "/api/v1/tts"}, SpeechRequest{
		API: SpeechAPIVolcengine, Voice: "BV001", Input: "你好", Options: map[string]string{"app_id": "app1"},
	})
	if string(v1.Audio) != "v1audio" {
		t.Fatalf("v1 audio = %q", v1.Audio)
	}
	v3 := synthesize(t, ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "tok", BaseURL: server.URL + "/api/v3/tts/unidirectional"}, SpeechRequest{
		API: SpeechAPIVolcengine, Voice: "zh_female", Input: "你好", Options: map[string]string{"app_id": "app1", "resource_id": "seed-tts-2.0"},
	})
	if string(v3.Audio) != "v3audio" {
		t.Fatalf("v3 audio = %q", v3.Audio)
	}
}

func TestMiMoSpeechAndTranscription(t *testing.T) {
	server := vendorServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body []byte) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		payload := decodeJSONBody(t, body)
		messages := payload["messages"].([]any)
		if payload["model"] == "mimo-v2.5-asr" {
			content := messages[0].(map[string]any)["content"].([]any)[0].(map[string]any)
			data := content["input_audio"].(map[string]any)["data"].(string)
			if !strings.HasPrefix(data, "data:audio/wav;base64,") {
				t.Errorf("audio data = %q", data)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":" 你好世界 "}}]}`))
			return
		}
		last := messages[len(messages)-1].(map[string]any)
		audio := payload["audio"].(map[string]any)
		if last["role"] != "assistant" || last["content"] != "<style>开心</style>你好" || audio["voice"] != "冰糖" || audio["format"] != "wav" {
			t.Errorf("payload = %#v", payload)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"audio":{"data":"` + base64.StdEncoding.EncodeToString([]byte("RIFFxxxxWAVE")) + `"}}}]}`))
	})
	cfg := mediaTestConfig(server.URL)
	resp := synthesize(t, cfg, SpeechRequest{API: SpeechAPIMiMo, Voice: "冰糖", Input: "你好", Instructions: "开心"})
	if string(resp.Audio) != "RIFFxxxxWAVE" || resp.Format != "wav" {
		t.Fatalf("resp = %#v", resp)
	}
	text, err := TranscribeAudio(context.Background(), cfg, TranscriptionRequest{API: SpeechAPIMiMo, Audio: []byte("RIFF")})
	if err != nil || text.Text != "你好世界" || text.Model != "mimo-v2.5-asr" {
		t.Fatalf("text=%#v err=%v", text, err)
	}
}

func TestGeminiSpeechWrapsPCMInWAV(t *testing.T) {
	server := vendorServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body []byte) {
		if r.URL.Path != "/v1beta/models/gemini-2.5-flash-preview-tts:generateContent" || r.Header.Get("x-goog-api-key") != "sk-test" {
			t.Errorf("path=%s key=%q", r.URL.Path, r.Header.Get("x-goog-api-key"))
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"audio/L16;codec=pcm;rate=24000","data":"` + base64.StdEncoding.EncodeToString([]byte{1, 2, 3, 4}) + `"}}]}}]}`))
	})
	resp := synthesize(t, ProviderConfig{Provider: ProviderGemini, APIKey: "sk-test", BaseURL: server.URL}, SpeechRequest{API: SpeechAPIGemini, Input: "hi"})
	if !isWAV(resp.Audio) || binary.LittleEndian.Uint32(resp.Audio[24:28]) != 24000 || len(resp.Audio) != 48 {
		t.Fatalf("audio = %v", resp.Audio)
	}
}

func TestAzureSendsSSMLWithSubscriptionKey(t *testing.T) {
	server := vendorServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body []byte) {
		ssml := string(body)
		if r.URL.Path != "/cognitiveservices/v1" || r.Header.Get("Ocp-Apim-Subscription-Key") != "sk-test" || r.Header.Get("X-Microsoft-OutputFormat") != "audio-24khz-48kbitrate-mono-mp3" {
			t.Errorf("path=%s headers=%#v", r.URL.Path, r.Header)
		}
		if !strings.Contains(ssml, "xml:lang='zh-CN'") || !strings.Contains(ssml, "rate='+20%'") || !strings.Contains(ssml, "a &lt; b") || !strings.Contains(ssml, "style='cheerful'") {
			t.Errorf("ssml = %s", ssml)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("ID3"))
	})
	synthesize(t, ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "sk-test", BaseURL: server.URL}, SpeechRequest{API: SpeechAPIAzure, Input: "a < b", Speed: 1.2, Instructions: "cheerful"})
}

func TestFishAudioSendsModelHeader(t *testing.T) {
	server := vendorServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body []byte) {
		payload := decodeJSONBody(t, body)
		if r.URL.Path != "/v1/tts" || r.Header.Get("model") != "s1" || payload["reference_id"] != "abc" {
			t.Errorf("path=%s model=%q payload=%#v", r.URL.Path, r.Header.Get("model"), payload)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("ID3"))
	})
	synthesize(t, mediaTestConfig(server.URL), SpeechRequest{API: SpeechAPIFishAudio, Voice: "abc", Input: "hi"})
}

func TestDashScopeQwenTTSDownloadsWithoutCredentials(t *testing.T) {
	var server *httptest.Server
	server = vendorServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body []byte) {
		switch r.URL.Path {
		case "/api/v1/services/aigc/multimodal-generation/generation":
			payload := decodeJSONBody(t, body)
			if payload["input"].(map[string]any)["voice"] != "Cherry" {
				t.Errorf("payload = %#v", payload)
			}
			_, _ = w.Write([]byte(`{"output":{"audio":{"url":"` + server.URL + `/oss/a.wav?sig=1"}}}`))
		case "/oss/a.wav":
			if r.Header.Get("Authorization") != "" || r.Header.Get("X-Team") != "" {
				t.Errorf("download leaked credentials: %#v", r.Header)
			}
			_, _ = w.Write([]byte("RIFFxxxxWAVE"))
		}
	})
	// 对话配置档常填 compatible-mode/v1，语音接口要回到服务根。
	resp := synthesize(t, ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "sk", BaseURL: server.URL + "/compatible-mode/v1", Headers: map[string]string{"X-Team": "diana"}}, SpeechRequest{
		API: SpeechAPIDashScope, Model: "qwen-tts", Input: "你好",
	})
	if string(resp.Audio) != "RIFFxxxxWAVE" {
		t.Fatalf("audio = %q", resp.Audio)
	}
}

func TestDashScopeCosyVoiceWebsocket(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api-ws/v1/inference" || r.Header.Get("Authorization") != "bearer sk" {
			t.Errorf("path=%s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var run map[string]any
		_ = conn.ReadJSON(&run)
		parameters := run["payload"].(map[string]any)["parameters"].(map[string]any)
		if run["header"].(map[string]any)["action"] != "run-task" || parameters["voice"] != "longxiaochun" {
			t.Errorf("run = %#v", run)
		}
		_ = conn.WriteJSON(map[string]any{"header": map[string]any{"event": "task-started"}})
		var cont, finish map[string]any
		_ = conn.ReadJSON(&cont)
		_ = conn.ReadJSON(&finish)
		if cont["payload"].(map[string]any)["input"].(map[string]any)["text"] != "你好" {
			t.Errorf("continue = %#v", cont)
		}
		_ = conn.WriteMessage(websocket.BinaryMessage, []byte("ID3"))
		_ = conn.WriteMessage(websocket.BinaryMessage, []byte("more"))
		_ = conn.WriteJSON(map[string]any{"header": map[string]any{"event": "task-finished"}})
	}))
	defer server.Close()
	resp := synthesize(t, ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "sk", BaseURL: server.URL}, SpeechRequest{
		API: SpeechAPIDashScope, Model: "cosyvoice-v2", Voice: "longxiaochun", Input: "你好",
	})
	if string(resp.Audio) != "ID3more" {
		t.Fatalf("audio = %q", resp.Audio)
	}
}

func TestEdgeSpeechCollectsAudioFrames(t *testing.T) {
	// Edge 请求带着浏览器扩展的 Origin，测试服务不校验它。
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("TrustedClientToken") != edgeTrustedClientToken || len(query.Get("Sec-MS-GEC")) != 64 {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, config, _ := conn.ReadMessage()
		_, ssml, _ := conn.ReadMessage()
		if !strings.Contains(string(config), "Path:speech.config") || !strings.Contains(string(ssml), "Microsoft Server Speech Text to Speech Voice (zh-CN, YunxiNeural)") {
			t.Errorf("config=%s ssml=%s", config, ssml)
		}
		for _, chunk := range []string{"ID3", "data"} {
			header := []byte("X-RequestId:1\r\nContent-Type:audio/mpeg\r\nPath:audio\r\n")
			frame := make([]byte, 2, 2+len(header)+len(chunk))
			binary.BigEndian.PutUint16(frame, uint16(len(header)))
			frame = append(append(frame, header...), chunk...)
			_ = conn.WriteMessage(websocket.BinaryMessage, frame)
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte("X-RequestId:1\r\nPath:turn.end\r\n\r\n{}"))
	}))
	defer server.Close()
	resp := synthesize(t, ProviderConfig{BaseURL: server.URL}, SpeechRequest{API: SpeechAPIEdge, Voice: "zh-CN-YunxiNeural", Input: "你好", Timeout: 5 * time.Second})
	if string(resp.Audio) != "ID3data" || resp.Format != "mp3" {
		t.Fatalf("resp = %#v", resp)
	}
}

func TestEdgeSecMSGECRoundsToFiveMinutes(t *testing.T) {
	base := time.Date(2026, 10, 26, 8, 0, 10, 0, time.UTC)
	if edgeSecMSGEC(base) != edgeSecMSGEC(base.Add(4*time.Minute)) || edgeSecMSGEC(base) == edgeSecMSGEC(base.Add(5*time.Minute)) {
		t.Fatal("token must change only on five-minute boundaries")
	}
}

func TestGoogleTranslateSplitsLongText(t *testing.T) {
	calls := 0
	server := vendorServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body []byte) {
		calls++
		if !strings.Contains(string(body), "jQ1olc") {
			t.Errorf("body = %s", body)
		}
		audio := base64.StdEncoding.EncodeToString([]byte{byte('a' + calls - 1)})
		_, _ = w.Write([]byte(")]}'\n\n[[\"wrb.fr\",\"jQ1olc\",\"[\\\"" + audio + "\\\"]\",null]]"))
	})
	text := strings.Repeat("一二三四五六七八九十，", 15)
	resp := synthesize(t, ProviderConfig{BaseURL: server.URL}, SpeechRequest{API: SpeechAPIGoogleTranslate, Input: text})
	if calls < 2 || string(resp.Audio) != string([]byte("abcdefghij")[:calls]) {
		t.Fatalf("calls=%d audio=%q", calls, resp.Audio)
	}
}

func TestCustomHTTPTemplates(t *testing.T) {
	var server *httptest.Server
	server = vendorServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body []byte) {
		switch r.URL.Path {
		case "/tts_to_audio/":
			payload := decodeJSONBody(t, body)
			if payload["text"] != "他说\"你好\"" || payload["speaker_wav"] != "alice" {
				t.Errorf("payload = %#v", payload)
			}
			w.Header().Set("Content-Type", "audio/wav")
			_, _ = w.Write([]byte("RIFFxxxxWAVE"))
		case "/voice/vits":
			if r.URL.Query().Get("text") != "hi there" || r.URL.Query().Get("id") != "3" {
				t.Errorf("query = %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"data":{"file":"/out/a.mp3"}}`))
		case "/out/a.mp3":
			_, _ = w.Write([]byte("ID3"))
		}
	})
	cfg := ProviderConfig{BaseURL: server.URL}
	xtts := synthesize(t, cfg, SpeechRequest{API: SpeechAPICustomHTTP, Voice: "alice", Input: "他说\"你好\"", Options: map[string]string{
		"path": "tts_to_audio/", "body": `{"text":"{{text}}","speaker_wav":"{{voice}}","language":"{{lang}}"}`,
	}})
	if xtts.Format != "wav" {
		t.Fatalf("format = %s", xtts.Format)
	}
	vits := synthesize(t, cfg, SpeechRequest{API: SpeechAPICustomHTTP, Voice: "3", Input: "hi there", Format: "mp3", Options: map[string]string{
		"method": "GET", "path": "voice/vits", "body_type": "query", "body": "text={{text}}&id={{voice}}", "response": "url:data.file",
	}})
	if string(vits.Audio) != "ID3" {
		t.Fatalf("audio = %q", vits.Audio)
	}
}

func TestVendorSpeechRequiresKeyOnlyForCloudServices(t *testing.T) {
	_, err := SynthesizeSpeech(context.Background(), ProviderConfig{}, SpeechRequest{API: SpeechAPIMiniMax, Voice: "v", Input: "x"})
	if err != ErrMissingAPIKey {
		t.Fatalf("err = %v", err)
	}
}
