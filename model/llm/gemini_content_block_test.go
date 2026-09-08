package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGeminiStructuredBlockCodes(t *testing.T) {
	for _, tc := range []struct{ name, body, reason string }{
		{"prompt", `{"promptFeedback":{"blockReason":"BLOCKLIST","blockReasonMessage":"任意本地化文字"}}`, "BLOCKLIST"},
		{"candidate", `{"candidates":[{"finishReason":"SAFETY","content":{"role":"model","parts":[{"text":"不得输出的部分回答"}]}}]}`, "SAFETY"},
		{"prohibited", `{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT"}}`, "PROHIBITED_CONTENT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "streamGenerateContent") {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: " + tc.body + "\n\n"))
				} else {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(tc.body))
				}
			}))
			defer server.Close()
			client, err := newGeminiClient(ProviderConfig{APIKey: "test-key", BaseURL: server.URL, Model: "gemini-test"}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			req := GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "你好"}}}
			resp, err := client.Generate(context.Background(), req)
			var blocked *ContentBlockedError
			if resp != nil || !errors.As(err, &blocked) || blocked.Reason != tc.reason || !errors.Is(err, ErrContentBlocked) {
				t.Fatalf("response=%+v err=%v", resp, err)
			}
			events, err := client.Stream(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			seen := false
			for e := range events {
				if e.Type == ChatEventTextDelta {
					t.Fatal("blocked text leaked")
				}
				if e.Type == ChatEventError {
					seen = true
					if e.ErrorCode != tc.reason || !errors.Is(e.ErrorCause, ErrContentBlocked) {
						t.Fatalf("event=%+v", e)
					}
				}
			}
			if !seen {
				t.Fatal("stream block lost")
			}
		})
	}
}

func TestGeminiInvalidArgumentIsNotContentBlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"invalid request"}}`))
	}))
	defer server.Close()
	client, err := newGeminiClient(ProviderConfig{APIKey: "test-key", BaseURL: server.URL, Model: "gemini-test"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "你好"}}})
	if err == nil || errors.Is(err, ErrContentBlocked) {
		t.Fatalf("err=%v", err)
	}
}
