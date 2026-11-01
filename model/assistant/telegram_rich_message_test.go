package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestTelegramRichBlocksNativeStructure(t *testing.T) {
	source := "## 对比😀\n\n价格 $400-600K，**重点**，`a_b`， https://example.com\n\n> 引用\n\n3. 第三项\n4. 第四项\n\n| 名称 | 数值 |\n| --- | ---: |\n| A | 42 |\n\n```go\nx := `||literal||`\n```"
	blocks, ok := telegramRichBlocks(source)
	if !ok || len(blocks) != 6 {
		t.Fatalf("blocks=%#v, ok=%v", blocks, ok)
	}
	for i, kind := range []string{"heading", "paragraph", "blockquote", "list", "table", "pre"} {
		if blocks[i].(map[string]any)["type"] != kind {
			t.Fatalf("block %d = %#v", i, blocks[i])
		}
	}
	list := blocks[3].(map[string]any)["items"].([]any)
	if list[0].(map[string]any)["value"] != 3 || list[1].(map[string]any)["value"] != 4 {
		t.Fatal("ordered list start lost")
	}
	table := blocks[4].(map[string]any)["cells"].([]any)
	if len(table) != 2 || table[0].([]any)[0].(map[string]any)["is_header"] != true {
		t.Fatal("table header lost")
	}
	if table[1].([]any)[1].(map[string]any)["align"] != "right" {
		t.Fatal("table alignment lost")
	}
	encoded, _ := json.Marshal(blocks)
	if !strings.Contains(string(encoded), "$400-600K") || strings.Contains(string(encoded), "mathematical") {
		t.Fatal("currency interpreted as math")
	}
	if !strings.Contains(string(encoded), `"type":"url"`) {
		t.Fatal("bare URL lost its link")
	}
	if !strings.Contains(string(encoded), "||literal||") {
		t.Fatal("code lost literal spoiler markers")
	}
}

func TestTelegramRichLiteralMarkers(t *testing.T) {
	for _, source := range []string{
		"`@username ||literal||`",
		"```sh\necho '@username ||literal||'\n```",
		"    @username ||literal||\n",
		"[profile](https://example.com/@username)",
	} {
		if _, ok := telegramRichBlocks(source); !ok {
			t.Errorf("literal markers should not force fallback: %q", source)
		}
	}
	for _, source := range []string{
		"**@username**", "> ||secret||", "[||secret||](https://example.com)",
		"`safe` @username", "```sh\nsafe\n```\n\n||secret||",
	} {
		if _, ok := telegramRichBlocks(source); ok {
			t.Errorf("visible mention/spoiler must retain legacy rendering: %q", source)
		}
	}
}

func TestTelegramRichBlocksUnsupportedKeepsLegacy(t *testing.T) {
	for _, source := range []string{"<diana-at id=\"12\"/>", "@username", "||隐藏||", "<b>HTML</b>", "![image](https://example.com/a.png)", "[local](file:///tmp/a)", "网址：https://example.com", strings.Repeat("x", 32769)} {
		if _, ok := telegramRichBlocks(source); ok {
			t.Errorf("unexpected rich support for %q", source[:min(len(source), 80)])
		}
	}
	wide := "|" + strings.Repeat(" x |", 21) + "\n|" + strings.Repeat("---|", 21) + "\n|" + strings.Repeat(" y |", 21)
	if _, ok := telegramRichBlocks(wide); ok {
		t.Fatal("oversized table must use legacy path")
	}
}

func TestTelegramRichInlineTextDecodesOnce(t *testing.T) {
	for source, want := range map[string]string{
		`\&amp;`: "&amp;", "&amp;amp;": "&amp;", "&lt;b&gt;": "<b>", `\*原样\*`: "*原样*",
	} {
		if got := telegramRichInlineText([]byte(source)); got != want {
			t.Errorf("%q = %q, want %q", source, got, want)
		}
	}
}

func TestTelegramRichLiteralMarkerDelivery(t *testing.T) {
	for _, tc := range []struct{ text, method string }{
		{"## Example\n\n`@user ||literal||`", "sendRichMessage"},
		{"## Example\n\n[profile](https://example.com/@user)", "sendRichMessage"},
		{"## Example\n\n@user", "sendMessage"},
		{"## Example\n\n||secret||", "sendMessage"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/"+tc.method) {
					t.Errorf("method = %s, want %s", r.URL.Path, tc.method)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 42}})
			}))
			defer server.Close()
			channel := NewTelegramChannel(TelegramConfig{BotToken: "test", APIBaseURL: server.URL, RichMessages: true})
			if err := channel.Send(context.Background(), OutgoingMessage{GroupID: "-123", Text: tc.text}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTelegramRichSendAndSafeFallback(t *testing.T) {
	for _, tc := range []struct {
		name             string
		enabled, literal bool
		code             int
		description      string
		want             []string
		wantErr          bool
	}{
		{"default", false, false, 0, "", []string{"sendMessage"}, false},
		{"enabled", true, false, 0, "", []string{"sendRichMessage"}, false},
		{"literal", true, true, 0, "", []string{"sendMessage"}, false},
		{"unsupported", true, false, 404, "Not Found", []string{"sendRichMessage", "sendMessage"}, false},
		{"not available", true, false, 400, "Rich messages are not supported", []string{"sendRichMessage", "sendMessage"}, false},
		{"bad blocks", true, false, 400, "Bad Request: invalid blocks", []string{"sendRichMessage"}, true},
		{"forbidden", true, false, 403, "Forbidden", []string{"sendRichMessage"}, true},
		{"rate limit", true, false, 429, "Too Many Requests", []string{"sendRichMessage"}, true},
		{"server error", true, false, 500, "Internal Server Error", []string{"sendRichMessage"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var calls []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
				mu.Lock()
				calls = append(calls, method)
				mu.Unlock()
				var params map[string]any
				_ = json.NewDecoder(r.Body).Decode(&params)
				if params["chat_id"] != "-123" || params["message_thread_id"] != "17" || params["reply_parameters"] == nil {
					t.Error("routing/reply lost")
				}
				if method == "sendRichMessage" {
					if params["rich_message"] == nil || params["text"] != nil || params["entities"] != nil {
						t.Error("invalid rich envelope")
					}
					if tc.code != 0 {
						_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": tc.code, "description": tc.description})
						return
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 42}})
			}))
			defer server.Close()
			channel := NewTelegramChannel(TelegramConfig{BotToken: "test", APIBaseURL: server.URL, RichMessages: tc.enabled})
			err := channel.Send(context.Background(), OutgoingMessage{GroupID: "-123", MessageThreadID: "17", ReplyMessageID: "8", Text: "## 标题\n\n**内容**", LiteralText: tc.literal})
			mu.Lock()
			defer mu.Unlock()
			if (err != nil) != tc.wantErr || !reflect.DeepEqual(calls, tc.want) {
				t.Fatalf("err=%v calls=%v want=%v", err, calls, tc.want)
			}
		})
	}
	if telegramRichUnsupported(errors.New("network timeout")) {
		t.Fatal("ambiguous transport error must not retry")
	}
}

func TestTelegramRichMessagesConfigRoundTrip(t *testing.T) {
	if !DefaultBotConfig().TelegramRichMessagesEnabled() || !(BotConfig{}).WithDefaults().TelegramRichMessagesEnabled() {
		t.Fatal("unconfigured bots must default on")
	}
	for _, enabled := range []bool{true, false} {
		cfg := BotConfig{Platform: PlatformTelegram, TelegramRichMessages: boolPointer(enabled)}
		got := ConfigFromPayload(PayloadFromConfig(cfg), cfg)
		if got.TelegramRichMessagesEnabled() != enabled {
			t.Fatal("setting lost")
		}
		if strings.Contains(platformOutputRulesForConfig(cfg), "已开启 Rich Messages") != enabled {
			t.Fatal("prompt capability mismatch")
		}
	}
}

func TestTelegramRichMessagesDefaultAndExplicitOptOut(t *testing.T) {
	for _, tc := range []struct {
		json string
		want bool
	}{
		{`{}`, true},
		{`{"telegram_rich_messages":null}`, true},
		{`{"telegram_rich_messages":true}`, true},
		{`{"telegram_rich_messages":false}`, false},
	} {
		t.Run(tc.json, func(t *testing.T) {
			var cfg BotConfig
			if err := json.Unmarshal([]byte(tc.json), &cfg); err != nil {
				t.Fatal(err)
			}
			cfg.Platform = PlatformTelegram
			for i := 0; i < 2; i++ {
				cfg = cfg.WithDefaults()
				if cfg.TelegramRichMessagesEnabled() != tc.want {
					t.Fatal("default or persisted preference changed")
				}
				channel := NewChannelForConfig(cfg).(*TelegramChannel)
				if channel.cfg.RichMessages != tc.want {
					t.Fatal("transport preference mismatch")
				}
				payload := PayloadFromConfig(cfg)
				encoded, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				if !tc.want && !strings.Contains(string(encoded), `"telegram_rich_messages":false`) {
					t.Fatal("explicit false omitted from payload")
				}
				var decoded ConfigPayload
				if err := json.Unmarshal(encoded, &decoded); err != nil {
					t.Fatal(err)
				}
				cfg = ConfigFromPayload(decoded, cfg)
				encoded, err = json.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(encoded, &cfg); err != nil {
					t.Fatal(err)
				}
			}
			if got := ConfigFromPayload(ConfigPayload{}, cfg); got.TelegramRichMessagesEnabled() != tc.want {
				t.Fatal("omitted field in an older client must preserve preference")
			}
		})
	}
}
