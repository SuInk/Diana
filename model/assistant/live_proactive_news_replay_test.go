// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// 主动开口找新闻的回放：同一个真实冷场点，分别按旧做法（不带素材）和新做法（先定
// 搜索词、联网搜、带素材）生成一次，结果逐条写进 JSONL。
//
//	DIANA_LIVE_LLM=1 与 DIANA_TEST_LLM_* 真实模型配置（见 liveLLMClient）
//	DIANA_REPLAY_BOT      机器人配置 JSON（人设、账号）
//	DIANA_REPLAY_SAMPLES  冷场样本 JSON
//	DIANA_REPLAY_OUT      结果 JSONL
func TestLiveProactiveNewsReplay(t *testing.T) {
	client := liveLLMClient(t)
	var botFile struct {
		BotConfig
	}
	readReplayJSON(t, os.Getenv("DIANA_REPLAY_BOT"), &botFile)
	var samples []struct {
		Group     string  `json:"group"`
		GroupName string  `json:"group_name"`
		At        int64   `json:"at"`
		GapH      float64 `json:"gap_h"`
		History   []struct {
			U   string `json:"u"`
			N   string `json:"n"`
			T   int64  `json:"t"`
			X   string `json:"x"`
			Out bool   `json:"out"`
		} `json:"history"`
	}
	readReplayJSON(t, os.Getenv("DIANA_REPLAY_SAMPLES"), &samples)
	out, err := os.Create(os.Getenv("DIANA_REPLAY_OUT"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	searchTools, err := NewWebSearchPlugin(&http.Client{Timeout: 40 * time.Second}).AgentTools(SettingValues{
		webSearchSettingMode:          webSearchModeAPI,
		webSearchSettingTavilyEnabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	var search agent.Tool
	for _, tool := range searchTools {
		if tool.Name() == agent.WebSearchToolName {
			search = tool
		}
	}
	if search == nil {
		t.Fatal("no web_search tool")
	}

	image := regexp.MustCompile(`\[CQ:image[^\]]*\]`)
	cq := regexp.MustCompile(`\[CQ:[^\]]*\]`)
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, 2)
	for index, sample := range samples {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			cfg := botFile.BotConfig
			cfg.ID, cfg.Enabled, cfg.Platform, cfg.AgentEnabled = "bot-replay", true, "onebot-v11", false
			cfg.ReplySafetyMasterEnabled = boolPointer(false)
			cfg.ProactiveMessaging = &ProactiveMessaging{Enabled: true}
			runtime := newLLMWelcomeRuntime(cfg, &recordingChannel{}, &welcomeStubLLM{})
			rec := &replayRecorder{inner: client}
			runtime.SetLLMProviderConfigFactory(func(_ llm.ProviderConfig) (LLMProvider, error) { return rec, nil })
			// 时钟拨回样本当时的冷场时刻，凌晨的「现在」会让模型以为半夜不该开口。
			at := time.Unix(sample.At, 0)
			runtime.now = func() time.Time { return at }
			shift := int64(0)
			var event MessageEvent
			for i, item := range sample.History {
				text := strings.TrimSpace(cq.ReplaceAllString(image.ReplaceAllString(item.X, "[图片]"), ""))
				if text == "" {
					continue
				}
				event = MessageEvent{
					Kind: EventKindGroup, Platform: "onebot-v11", ProfileID: cfg.ID, SelfID: cfg.BotAccount,
					GroupID: sample.Group, GroupName: firstNonEmpty(sample.GroupName, "群聊"),
					UserID: item.U, SenderName: item.N, MessageID: "r" + strconv.Itoa(index) + "-" + strconv.Itoa(i),
					RawMessage: text, Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
					Time: item.T + shift, Outbound: item.Out,
				}
				runtime.remember(event)
			}
			target := MessageEvent{Kind: EventKindGroup, Platform: "onebot-v11", ProfileID: cfg.ID, SelfID: cfg.BotAccount,
				GroupID: sample.Group, GroupName: event.GroupName}
			plan := proactivePlan{IdleFor: at.Sub(time.Unix(sample.History[len(sample.History)-1].T, 0))}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()

			record := map[string]any{"index": index, "group": sample.Group[len(sample.Group)-4:], "gap_h": sample.GapH}
			lines := proactiveNewsChatLines(runtime.proactiveRecentHistory(target), cfg.BotAccount)
			if n := len(lines); n > 12 {
				record["chat_tail"] = lines[n-12:]
			} else {
				record["chat_tail"] = lines
			}
			news := proactiveNews{}
			query, err := runtime.pickProactiveNewsQuery(ctx, target, lines)
			if err != nil {
				record["query_error"] = err.Error()
			}
			news.Query = query
			if query != "" {
				searchCtx, searchCancel := context.WithTimeout(ctx, proactiveNewsSearchTimeout)
				results, err := search.Run(searchCtx, map[string]any{"query": query, "recency": float64(proactiveNewsRecencyDays)})
				searchCancel()
				if err != nil {
					record["search_error"] = err.Error()
				}
				news.Results = truncateRunesFromStart(strings.TrimSpace(results), proactiveNewsResultMaxRunes)
			}
			record["query"] = news.Query
			record["results_head"] = truncateRunes(news.Results, 600)
			for _, variant := range []struct {
				key  string
				news proactiveNews
			}{{"old", proactiveNews{}}, {"new", news}} {
				if variant.key == "new" && news.Results == "" {
					record["new"] = "（同旧版：没有素材）"
					continue
				}
				text, err := runtime.generateProactiveMessage(ctx, cfg, target, plan, variant.news)
				if err != nil {
					text = "ERROR: " + err.Error()
				}
				record[variant.key] = text
				record[variant.key+"_raw"] = rec.lastText()
			}
			data, _ := json.Marshal(record)
			mu.Lock()
			out.Write(append(data, '\n'))
			mu.Unlock()
			t.Logf("sample %d done: query=%q", index, news.Query)
		}()
	}
	wg.Wait()
}

func readReplayJSON(t *testing.T, path string, target any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

type replayRecorder struct {
	inner LLMProvider
	mu    sync.Mutex
	last  string
}

func (r *replayRecorder) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	resp, err := r.inner.Generate(ctx, req)
	if path := os.Getenv("DIANA_REPLAY_DUMP"); path != "" {
		os.WriteFile(path, []byte(welcomeRequestDump(req)), 0o600)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.last = "ERR " + err.Error()
	} else {
		r.last = resp.Text
	}
	return resp, err
}

func (r *replayRecorder) lastText() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}
