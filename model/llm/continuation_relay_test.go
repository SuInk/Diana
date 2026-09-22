package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestContinuationScopeIgnoresRelayRenamedResponseModel 钉住一件容易被"顺手改对"
// 改坏的事：续传状态的作用域必须按**请求**的模型算，不能按响应里报告的模型算。
//
// 中转站经常改写响应里的 model：聚合器把上游真名透出来、订阅转发网关贴自己的
// 别名、按可用性静默换型号。作用域一旦跟着响应走，同一轮对话的前后两次请求就会
// 落进不同的作用域，scopedContinuationMessages 把思考内容和签名当成"跨模型复用"
// 剥掉，Anthropic 的签名回放随之失效——pi 为这个专门发过修复（earendil-works/pi#9188）。
//
// 这个失败模式表现为"模型偶尔失忆"，不报错、不进日志，极难归因，所以用测试钉住。
func TestContinuationScopeIgnoresRelayRenamedResponseModel(t *testing.T) {
	const (
		requested = "requested-model"
		renamed   = "relay-renamed-model"
	)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// 中转站在响应里报告了另一个模型名。
				if stream {
					writeChatEvents(w, `{"model":"`+renamed+`","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":"stop"}]}`)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"model":"`+renamed+`","choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
			}))
			defer server.Close()

			cfg := ProviderConfig{
				Provider:  ProviderOpenAICompatible,
				APIKey:    "test",
				Model:     requested,
				BaseURL:   server.URL + "/v1",
				APIFormat: APIFormatChatCompletions,
			}
			client, err := NewClient(cfg)
			if err != nil {
				t.Fatal(err)
			}
			req := GenerateRequest{Model: requested, Messages: []Message{{Role: RoleUser, Content: "OK"}}}

			var scope string
			if stream {
				streamer, ok := client.(interface {
					Stream(context.Context, GenerateRequest) (<-chan ChatEvent, error)
				})
				if !ok {
					t.Fatal("客户端不支持流式")
				}
				events, err := streamer.Stream(context.Background(), req)
				if err != nil {
					t.Fatal(err)
				}
				for event := range events {
					if event.Type == ChatEventError {
						t.Fatal(event.Error)
					}
					if event.Response != nil {
						scope = event.Response.ContinuationScope
					}
				}
			} else {
				resp, err := client.Generate(context.Background(), req)
				if err != nil {
					t.Fatal(err)
				}
				scope = resp.ContinuationScope
			}

			if want := continuationScope(cfg, requested); scope != want {
				t.Fatalf("作用域 = %q，应当按请求模型 %q 算（得到 %q）", scope, requested, want)
			}
			if bad := continuationScope(cfg, renamed); scope == bad {
				t.Fatalf("作用域跟着中转站改写的响应模型 %q 走了，续传状态会被误剥", renamed)
			}
		})
	}
}

// TestScopedContinuationMessagesStripsOnlyForeignScopes 说明剥离规则本身：作用域
// 相同或未标注的消息保留续传状态，只有来自别处的才清空。
func TestScopedContinuationMessagesStripsOnlyForeignScopes(t *testing.T) {
	reasoning := "内部推理"
	messages := []Message{
		{Role: RoleAssistant, Content: "同作用域", ContinuationScope: "scope-a", ReasoningContent: &reasoning},
		{Role: RoleAssistant, Content: "未标注", ReasoningContent: &reasoning},
		{Role: RoleAssistant, Content: "别的作用域", ContinuationScope: "scope-b", ReasoningContent: &reasoning},
	}
	out := scopedContinuationMessages(messages, "scope-a")
	if out[0].ReasoningContent == nil {
		t.Fatal("同作用域的续传状态被误剥")
	}
	if out[1].ReasoningContent == nil {
		t.Fatal("未标注的消息被误剥：调用方自建原生历史时会丢状态")
	}
	if out[2].ReasoningContent != nil {
		t.Fatal("跨作用域的续传状态没有被剥掉")
	}
	if messages[2].ReasoningContent == nil {
		t.Fatal("剥离不该改写调用方传入的切片")
	}
}
