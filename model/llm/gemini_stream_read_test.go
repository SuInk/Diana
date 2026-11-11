package llm

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const geminiPartialChunk = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"你好"}]}}]}` + "\n\n"

// captureStdLog 接住标准库 log 的输出：genai 的那行「Error <err>」就打在这里。
func captureStdLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	writer, flags := log.Writer(), log.Flags()
	log.SetOutput(&buffer)
	t.Cleanup(func() {
		log.SetOutput(writer)
		log.SetFlags(flags)
	})
	return &buffer
}

// TestGeminiStreamReadErrorCarriesContext 验证流读到一半连接断掉时，原因带着操作名
// 交给调用方，而不是由 SDK 打一行没头没尾的 Error。
func TestGeminiStreamReadErrorCarriesContext(t *testing.T) {
	logs := captureStdLog(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// 声明的长度比实际写的多，客户端读完这段就会拿到 unexpected EOF。
		w.Header().Set("Content-Length", "100000")
		_, _ = w.Write([]byte(geminiPartialChunk))
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer server.Close()
	client, err := newGeminiClient(ProviderConfig{APIKey: "test-key", BaseURL: server.URL, Model: "gemini-test"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	events, err := client.Stream(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "你好"}}})
	if err != nil {
		t.Fatal(err)
	}
	var streamErr string
	for event := range events {
		if event.Type == ChatEventError {
			streamErr = event.Error
		}
	}
	if !strings.Contains(streamErr, "gemini stream read failed") || !strings.Contains(streamErr, "model=gemini-test") {
		t.Fatalf("stream error = %q", streamErr)
	}
	if strings.Contains(logs.String(), "Error ") {
		t.Fatalf("SDK still logged a bare error: %q", logs.String())
	}
}

// TestGeminiStreamCancelDoesNotLogError 验证调用方中途放弃时不再留下
// 「Error context canceled」。
func TestGeminiStreamCancelDoesNotLogError(t *testing.T) {
	logs := captureStdLog(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(geminiPartialChunk))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client, err := newGeminiClient(ProviderConfig{APIKey: "test-key", BaseURL: server.URL, Model: "gemini-test"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := client.Stream(ctx, GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "你好"}}})
	if err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if event.Type == ChatEventTextDelta {
			cancel()
		}
	}
	if strings.Contains(logs.String(), "Error ") {
		t.Fatalf("cancellation logged as error: %q", logs.String())
	}
}

func TestGeminiStreamTimeoutCoversBodyConsumption(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(geminiPartialChunk))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			return
		case <-time.After(30 * time.Millisecond):
		}
		_, _ = w.Write([]byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"世界"}]},"finishReason":"STOP"}]}` + "\n\n"))
	}))
	defer server.Close()
	client, err := newGeminiClient(ProviderConfig{APIKey: "test-key", BaseURL: server.URL, Model: "gemini-test", Timeout: time.Second}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	events, err := client.Stream(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	var done bool
	for event := range events {
		if event.Type == ChatEventError {
			t.Fatalf("stream failed before body completed: %s", event.Error)
		}
		if event.Type == ChatEventTextDelta {
			text += event.Text
		}
		if event.Type == ChatEventDone {
			done = true
		}
	}
	if !done || text != "你好世界" {
		t.Fatalf("done=%v text=%q", done, text)
	}
}

// TestGeminiStreamSkipsSSECommentLines 验证中转网关夹在流里的 `: ping` 心跳、
// event:/id: 字段和拆成多行的 data 都按 SSE 规范处理，不再让 genai 报
// 「invalid stream chunk」。
func TestGeminiStreamSkipsSSECommentLines(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(": ping\n\n"))
		_, _ = w.Write([]byte("event: message\nid: 1\n" + geminiPartialChunk))
		_, _ = w.Write([]byte(":ping\r\n\r\n"))
		// 按规范一份 data 可以拆成多行，接收方用换行拼回去。
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\n" +
			"data: \"parts\":[{\"text\":\"呀\"}]},\"finishReason\":\"STOP\"}],\n" +
			"data: \"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":2}}\n\n"))
		_, _ = w.Write([]byte(": ping\n\n"))
	}))
	defer server.Close()
	client, err := newGeminiClient(ProviderConfig{APIKey: "test-key", BaseURL: server.URL, Model: "gemini-test"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	events, err := client.Stream(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "你好"}}})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	done := false
	for event := range events {
		switch event.Type {
		case ChatEventError:
			t.Fatalf("stream error = %q", event.Error)
		case ChatEventDone:
			done = true
		}
		text.WriteString(event.Text)
	}
	if !done || text.String() != "你好呀" {
		t.Fatalf("done=%v text=%q", done, text.String())
	}
}

// TestGeminiStreamPassesThroughRawErrorLine 验证流里不带 data: 的错误 JSON 原样交给
// SDK，仍报成上游错误，而不是被整理成 data 或吞掉。
func TestGeminiStreamPassesThroughRawErrorLine(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(": ping\n\n"))
		_, _ = w.Write([]byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"quota exceeded"}}` + "\n\n"))
	}))
	defer server.Close()
	client, err := newGeminiClient(ProviderConfig{APIKey: "test-key", BaseURL: server.URL, Model: "gemini-test"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	events, err := client.Stream(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "你好"}}})
	if err != nil {
		t.Fatal(err)
	}
	var streamErr string
	for event := range events {
		if event.Type == ChatEventError {
			streamErr = event.Error
		}
	}
	if !strings.Contains(streamErr, "quota exceeded") || strings.Contains(streamErr, "invalid stream chunk") {
		t.Fatalf("stream error = %q", streamErr)
	}
}
