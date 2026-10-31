// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// Opt-in replay of quote/history responses captured on the production host.
// Delivery is intercepted locally; no production database or chat is changed.
func TestLiveStockReplay(t *testing.T) {
	path := os.Getenv("DIANA_STOCK_REPLAY_FIXTURE")
	if path == "" {
		t.Skip("set DIANA_STOCK_REPLAY_FIXTURE to captured Tencent responses")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]struct {
		Quote   string          `json:"quote"`
		History json.RawMessage `json:"history"`
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) == 0 {
		t.Fatal("empty fixtures")
	}
	for symbol, fixture := range fixtures {
		t.Run(symbol, func(t *testing.T) {
			client := newStockClient()
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/search" {
					_, _ = io.WriteString(w, `v_hint="us~nvda.oq~NVIDIA~nvda~GP"`)
				} else if req.URL.Path == "/quote" {
					if req.URL.Query().Get("q") != symbol {
						t.Errorf("wrong quote symbol %s", req.URL.Query().Get("q"))
					}
					body, err := simplifiedchinese.GBK.NewEncoder().String(fixture.Quote)
					if err != nil {
						t.Error(err)
					}
					_, _ = io.WriteString(w, body)
				} else {
					var payload struct {
						Data map[string]json.RawMessage `json:"data"`
					}
					_ = json.Unmarshal(fixture.History, &payload)
					code := strings.Split(req.URL.Query().Get("param"), ",")[0]
					if _, ok := payload.Data[code]; !ok {
						t.Errorf("requested code %s absent from captured history", code)
					}
					_, _ = w.Write(fixture.History)
				}
			}))
			defer source.Close()
			client.searchURL = source.URL + "/search?q="
			client.quoteURL, client.klineURL = source.URL+"/quote?q=", source.URL+"/history?param="
			quote, err := client.Quote(context.Background(), symbol)
			if err != nil {
				t.Fatal(err)
			}
			bars, err := client.History(context.Background(), quote, 60)
			if err != nil || len(bars) != 60 {
				t.Fatalf("bars=%d error=%v", len(bars), err)
			}
			png, err := RenderStockChartPNG(quote, bars)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("%s: %d bars %s..%s adjustment=%q", symbol, len(bars), bars[0].Date, bars[len(bars)-1].Date, stockAdjustmentLabel(bars))
			if dir := os.Getenv("DIANA_STOCK_REPLAY_OUTPUT"); dir != "" {
				if err := os.WriteFile(filepath.Join(dir, symbol+".png"), png, 0600); err != nil {
					t.Fatal(err)
				}
			}
			// A single representative production failure exercises the current model,
			// real tool and image delivery path, without sending an external message.
			if symbol != "usNVDA" || os.Getenv("DIANA_LIVE_LLM") != "1" {
				return
			}
			uploads := 0
			delivery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/bottest/sendPhoto" {
					t.Errorf("unexpected delivery %s", req.URL.Path)
				}
				if err := req.ParseMultipartForm(2 << 20); err != nil {
					t.Error(err)
					return
				}
				defer req.MultipartForm.RemoveAll()
				f, _, err := req.FormFile("photo")
				if err != nil {
					t.Error(err)
					return
				}
				defer f.Close()
				body, _ := io.ReadAll(f)
				if len(body) < 1000 || string(body[1:4]) != "PNG" {
					t.Error("missing PNG")
				}
				uploads++
				_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":99}}`)
			}))
			defer delivery.Close()
			cfg := BotConfig{Platform: PlatformTelegram}.WithDefaults()
			runtime := NewRuntime(cfg, NewTelegramChannel(TelegramConfig{BotToken: "test", APIBaseURL: delivery.URL}), NewPluginManager(), nil, nil, nil, nil)
			event := MessageEvent{Kind: EventKindGroup, GroupID: "-100123", RawMessage: "给我下英伟达 NVDA k线"}
			tool := newDianaStockTool(runtime, event, SettingValues{stockSettingChart: true}).(*dianaStockTool)
			tool.client = client
			registry := agent.NewToolRegistry(tool)
			runner, err := agent.NewRunner(liveLLMClient(t), agent.Config{MaxSteps: 6}, registry)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			prompt := runtime.systemPromptWithRelationshipAndAgentTools(event, nil, false, RelationshipPolicy{}, true, registry)
			resp, err := runner.Run(ctx, agent.Request{Messages: []llm.Message{
				{Role: llm.RoleSystem, Content: prompt},
				{Role: llm.RoleUser, Content: event.RawMessage},
			}})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("model replay finish=%s uploads=%d answer=%s", resp.FinishReason, uploads, resp.Text)
			if uploads != 1 {
				t.Fatalf("expected exactly one image, got %d", uploads)
			}
		})
	}
}
