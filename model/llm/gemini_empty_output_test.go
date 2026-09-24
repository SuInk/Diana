// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 实测 antigravity 网关回来的形状：工具参数写到一半撞上补出来的 9216 上限，
// 残缺调用被整段丢掉，只剩空 text part 和 MAX_TOKENS。
func TestGeminiEmptyOutputReportsFinishReason(t *testing.T) {
	for _, tc := range []struct {
		name, body    string
		maxOutput     int64
		wantTruncated bool
		wantParts     []string
	}{
		{
			name:          "max tokens with implicit limit",
			body:          `{"candidates":[{"finishReason":"MAX_TOKENS","content":{"role":"model","parts":[{"text":""}]}}],"usageMetadata":{"promptTokenCount":116611,"totalTokenCount":116611}}`,
			wantTruncated: true,
			wantParts:     []string{"finish_reason=MAX_TOKENS", "max_output_tokens=65536", "input_tokens:116611"},
		},
		{
			name:          "max tokens with limit",
			body:          `{"candidates":[{"finishReason":"MAX_TOKENS","content":{"role":"model","parts":[{"text":""}]}}]}`,
			maxOutput:     4096,
			wantTruncated: true,
			wantParts:     []string{"max_output_tokens=4096"},
		},
		{
			name:      "malformed function call",
			body:      `{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL","content":{"role":"model","parts":[]}}]}`,
			wantParts: []string{"gemini response has no text", "finish_reason=MALFORMED_FUNCTION_CALL"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "streamGenerateContent") {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: " + tc.body + "\n\n"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client, err := newGeminiClient(ProviderConfig{APIKey: "test-key", BaseURL: server.URL, Model: "gemini-test"}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			req := GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "画一只骑车的鹈鹕"}}, MaxOutputTokens: tc.maxOutput}
			check := func(where string, err error) {
				t.Helper()
				if err == nil || errors.Is(err, ErrCompletionTruncatedNoText) != tc.wantTruncated {
					t.Fatalf("%s err=%v, want truncated=%t", where, err, tc.wantTruncated)
				}
				for _, part := range tc.wantParts {
					if !strings.Contains(err.Error(), part) {
						t.Fatalf("%s err=%q missing %q", where, err, part)
					}
				}
			}

			resp, err := client.Generate(context.Background(), req)
			if resp != nil {
				t.Fatalf("response=%+v", resp)
			}
			check("generate", err)

			events, err := client.Stream(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			var streamErr error
			for event := range events {
				if event.Type == ChatEventError {
					streamErr = event.ErrorCause
					if streamErr == nil {
						streamErr = errors.New(event.Error)
					}
				}
			}
			check("stream", streamErr)
		})
	}
}

// 已经流出正文之后再撞上限，是正文被截断而不是空结果，不能挂截断哨兵。
func TestGeminiStreamTruncatedAfterTextIsNotEmptyOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"前半段"}]}}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"candidates":[{"finishReason":"MAX_TOKENS","content":{"role":"model","parts":[{"text":""}]}}]}` + "\n\n"))
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
	var got ChatEvent
	for event := range events {
		if event.Type == ChatEventError {
			got = event
		}
	}
	if got.Error != "llm: incomplete gemini stream: MAX_TOKENS" || errors.Is(got.ErrorCause, ErrCompletionTruncatedNoText) {
		t.Fatalf("event=%+v", got)
	}
}

func geminiRequestedOutputLimit(t *testing.T, r *http.Request) (int64, bool) {
	t.Helper()
	var body struct {
		GenerationConfig map[string]any `json:"generationConfig"`
	}
	data, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("request body %q: %v", data, err)
	}
	value, ok := body.GenerationConfig["maxOutputTokens"].(float64)
	return int64(value), ok
}

// 没填上限时要代发模型的最大值，不能把字段空着交给网关去补。
func TestGeminiSendsImplicitMaxOutputTokens(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requested int64
		want      int64
	}{
		{name: "unset", want: 65536},
		{name: "configured", requested: 2048, want: 2048},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen []int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				limit, _ := geminiRequestedOutputLimit(t, r)
				seen = append(seen, limit)
				body := `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":"好"}]}}]}`
				if strings.Contains(r.URL.Path, "streamGenerateContent") {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: " + body + "\n\n"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			client, err := newGeminiClient(ProviderConfig{APIKey: "test-key", BaseURL: server.URL, Model: "gemini-test"}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			req := GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "你好"}}, MaxOutputTokens: tc.requested}
			if _, err := client.Generate(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			events, err := client.Stream(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			for range events {
			}
			if len(seen) != 2 || seen[0] != tc.want || seen[1] != tc.want {
				t.Fatalf("maxOutputTokens sent = %v, want %d twice", seen, tc.want)
			}
		})
	}
}

// 上限更低的模型拒绝代填值时，去掉字段重发一次；别的 400 和用户自己填的值都不重发。
func TestGeminiImplicitOutputLimitRejectionRetriesWithoutLimit(t *testing.T) {
	const rejection = `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"Unable to submit request because it has a maxOutputTokens value of 65536 but the supported range is from 1 (inclusive) to 8193 (exclusive)."}}`
	for _, tc := range []struct {
		name      string
		requested int64
		reject    string
		wantOK    bool
		wantCalls int
	}{
		{name: "implicit limit rejected", reject: rejection, wantOK: true, wantCalls: 2},
		{name: "configured limit rejected", requested: 65536, reject: rejection, wantCalls: 1},
		{name: "unrelated bad request", reject: `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"invalid request"}}`, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if _, ok := geminiRequestedOutputLimit(t, r); ok {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(tc.reject))
						return
					}
					body := `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":"完整回复"}]}}]}`
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = w.Write([]byte("data: " + body + "\n\n"))
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(body))
				}))
				client, err := newGeminiClient(ProviderConfig{APIKey: "test-key", BaseURL: server.URL, Model: "gemini-test"}, server.Client())
				if err != nil {
					t.Fatal(err)
				}
				req := GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "你好"}}, MaxOutputTokens: tc.requested}
				var text string
				if stream {
					events, err := client.Stream(context.Background(), req)
					if err != nil {
						t.Fatal(err)
					}
					for event := range events {
						if event.Type == ChatEventTextDelta {
							text += event.Text
						}
					}
				} else if resp, err := client.Generate(context.Background(), req); err == nil {
					text = resp.Text
				}
				server.Close()
				if (text == "完整回复") != tc.wantOK || calls != tc.wantCalls {
					t.Fatalf("stream=%t text=%q calls=%d, want ok=%t calls=%d", stream, text, calls, tc.wantOK, tc.wantCalls)
				}
			}
		})
	}
}
