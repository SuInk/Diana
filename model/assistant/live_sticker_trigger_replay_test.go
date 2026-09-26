// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// TestLiveStickerTriggerReplay 拿线上调试轨迹里的主回复请求原样重放，比较改表情包
// 说明前后模型会不会去用 sticker 工具。只看第一步：模型是否 tools_load sticker 或
// 直接执行它，不真的发图。
//
//	DIANA_STICKER_REPLAY_DIR=<目录>     调试轨迹文件（debug-traces 下的 .json/.json.gz，或每行一条 metadata 的 .jsonl）
//	DIANA_STICKER_REPLAY_EXPORT=<文件>  只导出供盲标的对话末尾，不调模型
//	DIANA_STICKER_REPLAY_IDS=<文件>     只回放这些样本 id（空白分隔）
//	DIANA_STICKER_REPLAY_LABELS=<文件>  盲标结果 {"样本 id": "Y"|"N"|"U"}，按标签分开统计
//	DIANA_STICKER_REPLAY_VARIANTS=baseline,desc_only,new   DIANA_STICKER_REPLAY_RUNS=2
//	DIANA_STICKER_REPLAY_TAIL=60（保留的末尾消息数）  DIANA_STICKER_REPLAY_OUT=<结果 jsonl>
//	DIANA_STICKER_REPLAY_PROVIDER=gemini 时用 Gemini 协议，否则同其他 live 测试走 OpenAI 兼容
func TestLiveStickerTriggerReplay(t *testing.T) {
	dir := strings.TrimSpace(os.Getenv("DIANA_STICKER_REPLAY_DIR"))
	if dir == "" {
		t.Skip("set DIANA_STICKER_REPLAY_DIR to replay recorded reply requests")
	}
	samples := loadStickerReplaySamples(t, dir, envInt("DIANA_STICKER_REPLAY_TAIL", 60))
	samples = filterStickerReplaySamples(t, samples)
	if len(samples) == 0 {
		t.Fatal("no replayable reply requests with sticker in the tool catalog")
	}
	t.Logf("loaded %d samples", len(samples))
	if path := os.Getenv("DIANA_STICKER_REPLAY_EXPORT"); path != "" {
		exportStickerReplayForLabeling(t, samples, path)
		return
	}
	labels := map[string]string{}
	if path := os.Getenv("DIANA_STICKER_REPLAY_LABELS"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &labels); err != nil {
			t.Fatal(err)
		}
	}
	client := stickerReplayClient(t)
	variants := strings.Split(firstNonEmpty(os.Getenv("DIANA_STICKER_REPLAY_VARIANTS"), "baseline,new"), ",")
	runs := envInt("DIANA_STICKER_REPLAY_RUNS", 2)

	type result struct {
		ID      string `json:"id"`
		Variant string `json:"variant"`
		Run     int    `json:"run"`
		Label   string `json:"label,omitempty"`
		Trigger bool   `json:"trigger"`
		Calls   string `json:"calls,omitempty"`
		Text    string `json:"text,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	type job struct {
		sample  stickerReplaySample
		variant string
		run     int
	}
	jobs := make(chan job)
	var mu sync.Mutex
	var results []result
	var workers sync.WaitGroup
	for range envInt("DIANA_STICKER_REPLAY_CONCURRENCY", 4) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for item := range jobs {
				req := stickerReplayVariant(item.sample.request, item.variant)
				var response *llm.GenerateResponse
				var err error
				// 网关忙时成片 503/429，重试几次，不然大半样本白跑。
				for attempt := 0; attempt < 4; attempt++ {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
					response, err = client.Generate(ctx, req)
					cancel()
					if err == nil || !(strings.Contains(err.Error(), " 503 ") || strings.Contains(err.Error(), " 429 ")) {
						break
					}
					time.Sleep(time.Duration(5*(attempt+1)) * time.Second)
				}
				out := result{ID: item.sample.id, Variant: item.variant, Run: item.run, Label: labels[item.sample.id]}
				if err != nil {
					out.Error = err.Error()
				} else {
					out.Trigger, out.Calls = stickerReplayTriggered(response)
					out.Text = truncateRunes(response.Text, 120)
				}
				mu.Lock()
				results = append(results, out)
				mu.Unlock()
			}
		}()
	}
	for run := 1; run <= runs; run++ {
		for _, sample := range samples {
			for _, variant := range variants {
				jobs <- job{sample: sample, variant: strings.TrimSpace(variant), run: run}
			}
		}
	}
	close(jobs)
	workers.Wait()

	if path := os.Getenv("DIANA_STICKER_REPLAY_OUT"); path != "" {
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		encoder := json.NewEncoder(file)
		for _, item := range results {
			_ = encoder.Encode(item)
		}
		_ = file.Close()
	}
	type tally struct{ trigger, total, errors int }
	summary := map[string]*tally{}
	for _, item := range results {
		for _, key := range []string{item.Variant + " 全部", item.Variant + " 标签=" + firstNonEmpty(item.Label, "?")} {
			if summary[key] == nil {
				summary[key] = &tally{}
			}
			if item.Error != "" {
				summary[key].errors++
				continue
			}
			summary[key].total++
			if item.Trigger {
				summary[key].trigger++
			}
		}
	}
	keys := make([]string, 0, len(summary))
	for key := range summary {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		item := summary[key]
		t.Logf("%-28s 触发 %3d / %3d（失败 %d）", key, item.trigger, item.total, item.errors)
	}
}

type stickerReplaySample struct {
	id      string
	request llm.GenerateRequest
}

func loadStickerReplaySamples(t *testing.T, dir string, tail int) []stickerReplaySample {
	t.Helper()
	var samples []stickerReplaySample
	add := func(id string, raw []byte) {
		var envelope struct {
			Metadata json.RawMessage `json:"metadata"`
			Purpose  string          `json:"purpose"`
		}
		if json.Unmarshal(raw, &envelope) != nil {
			return
		}
		body := raw
		if len(envelope.Metadata) > 0 {
			body = envelope.Metadata
		}
		var metadata struct {
			Purpose string              `json:"purpose"`
			Request llm.GenerateRequest `json:"request"`
		}
		if json.Unmarshal(body, &metadata) != nil || metadata.Purpose != "unlabeled" {
			return
		}
		req := metadata.Request
		if len(req.Tools) == 0 || len(req.Messages) == 0 || req.Messages[len(req.Messages)-1].Role != llm.RoleUser ||
			!strings.Contains(req.Messages[0].Content, "\n- sticker: ") {
			return
		}
		samples = append(samples, stickerReplaySample{id: id, request: trimStickerReplayRequest(req, tail)})
	}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		switch {
		case strings.HasSuffix(path, ".jsonl"):
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for index, line := range strings.Split(string(raw), "\n") {
				if strings.TrimSpace(line) != "" {
					add(rel+"#"+strconv.Itoa(index+1), []byte(line))
				}
			}
		case strings.HasSuffix(path, ".json"), strings.HasSuffix(path, ".json.gz"):
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			var reader io.Reader = file
			if strings.HasSuffix(path, ".gz") {
				gz, err := gzip.NewReader(file)
				if err != nil {
					return nil
				}
				defer gz.Close()
				reader = gz
			}
			raw, err := io.ReadAll(reader)
			if err != nil {
				return err
			}
			add(rel, raw)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].id < samples[j].id })
	return samples
}

// filterStickerReplaySamples 按 DIANA_STICKER_REPLAY_IDS（空白分隔的样本 id）只留指定样本。
func filterStickerReplaySamples(t *testing.T, samples []stickerReplaySample) []stickerReplaySample {
	t.Helper()
	path := os.Getenv("DIANA_STICKER_REPLAY_IDS")
	if path == "" {
		return samples
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	keep := map[string]bool{}
	for _, id := range strings.Fields(string(raw)) {
		keep[id] = true
	}
	filtered := samples[:0]
	for _, sample := range samples {
		if keep[sample.id] {
			filtered = append(filtered, sample)
		}
	}
	return filtered
}

// trimStickerReplayRequest 保留开头的系统消息和末尾 tail 条，控制回放成本；两个变体截法相同。
func trimStickerReplayRequest(req llm.GenerateRequest, tail int) llm.GenerateRequest {
	head := 0
	for head < len(req.Messages) && req.Messages[head].Role == llm.RoleSystem {
		head++
	}
	start := len(req.Messages) - tail
	if tail <= 0 || start <= head {
		return req
	}
	for start < len(req.Messages) && req.Messages[start].Role == llm.RoleTool {
		start++
	}
	req.Messages = append(append([]llm.Message(nil), req.Messages[:head]...), req.Messages[start:]...)
	return req
}

func stickerReplayVariant(req llm.GenerateRequest, variant string) llm.GenerateRequest {
	if variant == "baseline" {
		return req
	}
	messages := append([]llm.Message(nil), req.Messages...)
	system := messages[0].Content
	if start := strings.Index(system, "\n- sticker: "); start >= 0 {
		end := strings.Index(system[start+1:], "\n")
		registry := agent.NewToolRegistry()
		registry.Register(newDianaStickerTool(nil, MessageEvent{}, nil))
		line := "\n" + registry.SystemPromptCatalog()
		if end < 0 {
			system = system[:start] + line
		} else {
			system = system[:start] + line + system[start+1+end:]
		}
	}
	if variant == "new" {
		system += "\n" + promptToolSticker
	}
	messages[0].Content = system
	req.Messages = messages
	return req
}

func stickerReplayTriggered(response *llm.GenerateResponse) (bool, string) {
	var calls []string
	triggered := false
	for _, call := range response.ToolCalls {
		encoded, _ := json.Marshal(call.Arguments)
		calls = append(calls, call.Name+string(encoded))
		switch call.Name {
		case dianaStickerToolName:
			triggered = true
		case agent.ToolsLoadToolName, "tools_execute":
			if strings.Contains(string(encoded), `"`+dianaStickerToolName+`"`) {
				triggered = true
			}
		}
	}
	return triggered, strings.Join(calls, "; ")
}

// exportStickerReplayForLabeling 只导出对话末尾给人（或另一个模型）盲标：不含模型回复和任何分数。
func exportStickerReplayForLabeling(t *testing.T, samples []stickerReplaySample, path string) {
	t.Helper()
	type view struct {
		ID    string   `json:"id"`
		Lines []string `json:"lines"`
	}
	views := make([]view, 0, len(samples))
	for _, sample := range samples {
		var lines []string
		for index := len(sample.request.Messages) - 1; index >= 0 && len(lines) < 14; index-- {
			message := sample.request.Messages[index]
			if message.Role == llm.RoleSystem || message.Role == llm.RoleTool || strings.TrimSpace(message.Content) == "" {
				continue
			}
			// 用户侧除了聊天记录还有记忆、常用表达这些注入说明，盲标只看真实对话。
			if message.Role == llm.RoleUser && !strings.HasPrefix(message.Content, "[历史") &&
				!strings.Contains(message.Content, "【当前需要回复的消息】") {
				continue
			}
			who := "群友"
			if message.Role == llm.RoleAssistant {
				who = "机器人"
			}
			lines = append([]string{who + "：" + truncateRunes(message.Content, 300)}, lines...)
		}
		views = append(views, view{ID: sample.id, Lines: lines})
	}
	raw, err := json.MarshalIndent(views, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("exported %d samples to %s", len(views), path)
}

func stickerReplayClient(t *testing.T) llm.LLMClient {
	t.Helper()
	if os.Getenv("DIANA_STICKER_REPLAY_PROVIDER") != "gemini" {
		return liveLLMClient(t)
	}
	liveLLMClient(t) // 只借它的跳过条件
	client, err := llm.NewClient(llm.ProviderConfig{
		Provider: llm.ProviderGemini,
		APIKey:   strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_API_KEY")),
		BaseURL:  strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_BASE_URL")),
		Model:    strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_MODEL")),
		Timeout:  90 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// 回放变体只换掉目录里 sticker 那一行、在 new 里补上发送时机，别的目录行原样保留。
func TestStickerReplayVariantSwapsOnlyStickerLine(t *testing.T) {
	req := llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: "规则\n- send_file: 发文件\n- sticker: 旧说明...\n- subscription: 订阅"},
		{Role: llm.RoleUser, Content: "hi"},
	}}
	if got := stickerReplayVariant(req, "baseline"); got.Messages[0].Content != req.Messages[0].Content {
		t.Fatalf("baseline changed: %q", got.Messages[0].Content)
	}
	descOnly := stickerReplayVariant(req, "desc_only").Messages[0].Content
	if strings.Contains(descOnly, "旧说明") || !strings.Contains(descOnly, "被要表情包时必用") ||
		!strings.Contains(descOnly, "- send_file: 发文件\n- sticker: ") || !strings.HasSuffix(descOnly, "\n- subscription: 订阅") {
		t.Fatalf("desc_only = %q", descOnly)
	}
	if full := stickerReplayVariant(req, "new").Messages[0].Content; !strings.HasSuffix(full, promptToolSticker) {
		t.Fatalf("new variant missing prompt: %q", full)
	}
	if req.Messages[0].Content != "规则\n- send_file: 发文件\n- sticker: 旧说明...\n- subscription: 订阅" {
		t.Fatal("variant mutated the shared sample")
	}
}
