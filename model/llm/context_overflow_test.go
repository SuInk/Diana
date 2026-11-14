// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 智谱 glm-4v-flash 实际返回的报错原文（群里贴出来的那条）。
const glmOverflowDetail = "code=1210; message=API 调用参数有误，请检查文档。Input validation error: `inputs` tokens + `max_new_tokens` must be <= 16384. Given: 20017 `inputs` tokens and 1024 `max_new_tokens`"

func TestContextOverflowLimitReadsProviderWindow(t *testing.T) {
	for _, item := range []struct {
		name string
		err  error
		want int64
		ok   bool
	}{
		{"智谱 / TGI", errors.New("400 Bad Request: " + glmOverflowDetail), 16384, true},
		{"OpenAI / vLLM", errors.New("This model's maximum context length is 32768 tokens. However, you requested 40000 tokens"), 32768, true},
		{"认得出超限但没写数字", errors.New("context_length_exceeded"), 0, false},
		{"max_tokens 范围不是超限", errors.New("code=1210; message=max_tokens参数非法：限制数值范围[1,1024]"), 0, false},
	} {
		t.Run(item.name, func(t *testing.T) {
			if item.ok && !IsContextOverflowError(item.err) {
				t.Fatalf("not recognized as overflow: %v", item.err)
			}
			got, ok := ContextOverflowLimit(item.err)
			if got != item.want || ok != item.ok {
				t.Fatalf("ContextOverflowLimit = %d, %t; want %d, %t", got, ok, item.want, item.ok)
			}
		})
	}
}

// 撞过一次超限之后记住真实窗口：同一端点同一模型的下一次请求按它校验，
// 不再先吃一个 400。
func TestOpenAICompatibleLearnsWindowFromOverflow(t *testing.T) {
	saved := rememberedContextLimits
	rememberedContextLimits = &contextLimitMemo{}
	t.Cleanup(func() { rememberedContextLimits = saved })

	var sizes []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sizes = append(sizes, len(body.Messages))
		w.Header().Set("Content-Type", "application/json")
		if len(sizes) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"1210","message":` + strconvQuote(glmOverflowDetail) + `}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"好"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, APIFormat: APIFormatChatCompletions, APIKey: "k", BaseURL: server.URL + "/v1", Model: "glm-4v-flash"}
	history := make([]Message, 0, 60)
	for i := 0; i < 60; i++ {
		history = append(history, Message{Role: RoleUser, Content: strings.Repeat("聊天记录", 150)})
	}

	client := newOpenAICompatibleClient(cfg, server.Client())
	if _, err := client.Generate(context.Background(), GenerateRequest{Messages: history}); err == nil || !IsContextOverflowError(err) {
		t.Fatalf("first call err = %v, want the overflow", err)
	}
	if limit, ok := rememberedContextLimits.get(cfg, "glm-4v-flash"); !ok || limit != 16384 {
		t.Fatalf("learned = %d, %t; want 16384", limit, ok)
	}
	// 每条消息都会新建 client，学到的窗口要跨 client 生效：同样的请求本地就判超限，
	// 不再发出去先吃一个 400，也不裁剪消息。
	client = newOpenAICompatibleClient(cfg, server.Client())
	if _, err := client.Generate(context.Background(), GenerateRequest{Messages: history}); !errors.Is(err, ErrContextOverflow) {
		t.Fatalf("second call err = %v, want local ErrContextOverflow", err)
	}
	if len(sizes) != 1 {
		t.Fatalf("requests sent = %v, want only the first", sizes)
	}
	if got := applyContextLimit(GenerateRequest{Model: "glm-4v-flash", Messages: history}, cfg); got.MaxContextTokens != 16384 {
		t.Fatalf("limit = %d, want the learned 16384", got.MaxContextTokens)
	}
	if limit, ok := LearnedContextLimit(cfg, "glm-4v-flash"); !ok || limit != 16384 {
		t.Fatalf("LearnedContextLimit = %d, %t", limit, ok)
	}
	// 换个模型不受影响。
	other := cfg
	other.Model = "glm-4-plus"
	if got := applyContextLimit(GenerateRequest{Model: "glm-4-plus", Messages: history}, other); got.MaxContextTokens == 16384 {
		t.Fatal("learned window leaked to another model")
	}
}

func strconvQuote(text string) string {
	quoted, _ := json.Marshal(text)
	return string(quoted)
}
