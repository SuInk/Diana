package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPromptCacheKeySurvivesRegistryAndBothWireProtocols(t *testing.T) {
	for _, format := range []APIFormat{APIFormatResponses, APIFormatChatCompletions} {
		for _, stream := range []bool{false, true} {
			for _, key := range []string{"", "diana-opaque-synthetic-key"} {
				t.Run(fmt.Sprintf("%s/stream=%v/key=%v", format, stream, key != ""), func(t *testing.T) {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						var payload map[string]any
						if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
							t.Error(err)
						}
						got, present := payload["prompt_cache_key"]
						if (key == "" && present) || (key != "" && got != key) {
							t.Errorf("wire cache key=%v present=%v", got, present)
						}
						// 中转网关的调度先查请求头、再回落到 body 的 prompt_cache_key，
						// 所以两样都要在每条协议路径上原样到达。
						for _, name := range sessionAffinityHeaders {
							header := r.Header.Get(name)
							if (key == "" && header != "") || (key != "" && header != key) {
								t.Errorf("wire session affinity header %s=%q, want %q", name, header, key)
							}
						}
						if format == APIFormatChatCompletions {
							if payload["stream"] == true {
								writeChatEvents(w, `{"model":"test","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":"stop"}]}`)
								return
							}
							w.Header().Set("Content-Type", "application/json")
							fmt.Fprint(w, `{"model":"test","choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
							return
						}
						response := `{"id":"resp","object":"response","created_at":1,"model":"test","status":"completed","output":[{"id":"msg","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"OK","annotations":[]}]}],"usage":{"input_tokens":8,"output_tokens":1,"total_tokens":9}}`
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":"+response+"}\n\n")
							return
						}
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, response)
					}))
					defer server.Close()
					registry, selection, err := NewProviderRegistryFromProfiles(NewProfileSet(ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "test", Model: "test", BaseURL: server.URL + "/v1", APIFormat: format}))
					if err != nil {
						t.Fatal(err)
					}
					client := RegistryClient{Registry: registry, Selection: selection}
					req := GenerateRequest{PromptCacheKey: key, Messages: []Message{{Role: RoleUser, Content: "OK"}}}
					if stream {
						events, err := client.Stream(context.Background(), req)
						if err != nil {
							t.Fatal(err)
						}
						for event := range events {
							if event.Type == ChatEventError {
								t.Fatal(event.Error)
							}
						}
					} else {
						if _, err := client.Generate(context.Background(), req); err != nil {
							t.Fatal(err)
						}
					}
				})
			}
		}
	}
}

// TestSessionAffinityHeaderYieldsToConfiguredHeader 保证会话亲和头不覆盖部署方自己
// 配的同名头：那是他对自己网关的了解，比我们的默认猜测可靠。
func TestSessionAffinityHeaderYieldsToConfiguredHeader(t *testing.T) {
	const configured = "deployment-owned-value"
	seen := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"test","choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	registry, selection, err := NewProviderRegistryFromProfiles(NewProfileSet(ProviderConfig{
		Provider:  ProviderOpenAICompatible,
		APIKey:    "test",
		Model:     "test",
		BaseURL:   server.URL + "/v1",
		APIFormat: APIFormatChatCompletions,
		Headers:   map[string]string{"X-Session-Id": configured},
	}))
	if err != nil {
		t.Fatal(err)
	}
	client := RegistryClient{Registry: registry, Selection: selection}
	if _, err := client.Generate(context.Background(), GenerateRequest{
		PromptCacheKey: "diana-opaque-synthetic-key",
		Messages:       []Message{{Role: RoleUser, Content: "OK"}},
	}); err != nil {
		t.Fatal(err)
	}

	header := <-seen
	if got := header.Get("X-Session-Id"); got != configured {
		t.Fatalf("X-Session-Id = %q, 配置档的值应当胜出 (%q)", got, configured)
	}
	// 没被配置档占用的那些仍然要带上，否则换个网关就没有亲和信号了。
	if got := header.Get("X-Session-Affinity"); got != "diana-opaque-synthetic-key" {
		t.Fatalf("X-Session-Affinity = %q, 未被占用的头应当照常发出", got)
	}
}
