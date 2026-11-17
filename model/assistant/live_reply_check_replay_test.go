// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// TestLiveReplyCheckReplay 重放生产 debug-traces 里主回复的第一步请求，比较【输出前检查】
// 带不带「没查过别说查过」和「算数先算再说」两条（见 promptReplyClaimedLookupSpec、
// promptReplyArithmeticSpec）时，模型会不会自称查过、会不会先用 codemode 算。默认跳过：
//
//	DIANA_LIVE_LLM=1 DIANA_TEST_LLM_* DIANA_REPLY_CHECK_REPLAY=<…-reply.json 或目录> go test ./model/assistant -run TestLiveReplyCheckReplay
//
// DIANA_REPLY_CHECK_VARIANTS 逗号分隔：strip（去掉这两条）、check（接上当前默认的两条）。
func TestLiveReplyCheckReplay(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("DIANA_REPLY_CHECK_REPLAY"))
	if path == "" {
		t.Skip("set DIANA_REPLY_CHECK_REPLAY to a recorded reply request or a directory of them")
	}
	paths := []string{path}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		paths, _ = filepath.Glob(filepath.Join(path, "*.json"))
		sort.Strings(paths)
	}
	var bases []llm.GenerateRequest
	for _, file := range paths {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Metadata struct {
				Request llm.GenerateRequest `json:"request"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Metadata.Request.Messages) == 0 {
			t.Fatalf("%s is not a recorded reply request: %v", file, err)
		}
		bases = append(bases, envelope.Metadata.Request)
	}
	rules := []string{promptReplyClaimedLookupSpec.Default, promptReplyArithmeticSpec.Default}
	client := liveLLMClient(t)
	runs := envInt("DIANA_REPLY_CHECK_RUNS", 3)
	variants := strings.Split(firstNonEmpty(os.Getenv("DIANA_REPLY_CHECK_VARIANTS"), "strip,check"), ",")

	apply := func(base llm.GenerateRequest, variant string) llm.GenerateRequest {
		req := base
		req.Messages = append([]llm.Message(nil), base.Messages...)
		for index, message := range req.Messages {
			if message.Role != llm.RoleSystem || !strings.HasPrefix(message.Content, "【输出前检查】") {
				continue
			}
			for _, rule := range rules {
				message.Content = strings.ReplaceAll(message.Content, "\n"+rule, "")
				if variant == "check" {
					message.Content += "\n" + rule
				}
			}
			req.Messages[index] = message
		}
		return req
	}

	claimed := regexp.MustCompile(`我(之前|刚|刚才|已经)?(查过|查了|搜过|搜了|看过评价|看了评价|看了下评价|试过)`)
	type result struct {
		Variant  string `json:"variant"`
		Sample   string `json:"sample"`
		Run      int    `json:"run"`
		Claimed  bool   `json:"claimed"`
		Silent   bool   `json:"silent"`
		CodeMode bool   `json:"codemode"`
		Calls    string `json:"calls"`
		Text     string `json:"text,omitempty"`
		Error    string `json:"error,omitempty"`
	}
	describe := func(resp *llm.GenerateResponse, out *result) {
		var calls []string
		text := strings.TrimSpace(resp.Text)
		for _, call := range resp.ToolCalls {
			raw, _ := json.Marshal(call.Arguments)
			name := call.Name
			switch call.Name {
			case "send":
				sent, _ := call.Arguments["text"].(string)
				text = sent
				if silent, _ := call.Arguments["silent"].(bool); silent {
					out.Silent = true
					text = "[silent] " + text
				}
			case "codemode":
				out.CodeMode = true
				name += ":" + truncateRunes(string(raw), 200)
			default:
				name += ":" + truncateRunes(string(raw), 120)
			}
			calls = append(calls, name)
		}
		out.Calls = strings.Join(calls, " ; ")
		out.Claimed = claimed.MatchString(text)
		out.Text = truncateRunes(text, 120)
	}
	type job struct {
		variant string
		sample  int
		run     int
	}
	jobs := make(chan job)
	var mu sync.Mutex
	var results []result
	var wg sync.WaitGroup
	for worker := 0; worker < envInt("DIANA_REPLY_CHECK_WORKERS", 3); worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				out := result{Variant: item.variant, Sample: filepath.Base(paths[item.sample]), Run: item.run}
				// 网关常回 503/429，隔一会儿重试，别让失败样本把两边的有效样本数拉得不一样。
				var resp *llm.GenerateResponse
				var err error
				for attempt := 0; attempt < 6; attempt++ {
					if resp, err = client.Generate(context.Background(), apply(bases[item.sample], item.variant)); err == nil || !strings.Contains(err.Error(), "503") && !strings.Contains(err.Error(), "429") {
						break
					}
					time.Sleep(time.Duration(attempt+1) * 10 * time.Second)
				}
				if err != nil {
					out.Error = truncateRunes(err.Error(), 200)
				} else {
					describe(resp, &out)
				}
				mu.Lock()
				results = append(results, out)
				mu.Unlock()
			}
		}()
	}
	// 变体放在最里层交错排队，两边在同一时段跑，网关和模型的时段波动一样。
	for run := 1; run <= runs; run++ {
		for sample := range bases {
			for _, variant := range variants {
				jobs <- job{variant: strings.TrimSpace(variant), sample: sample, run: run}
			}
		}
	}
	close(jobs)
	wg.Wait()
	for _, item := range results {
		line, _ := json.Marshal(item)
		t.Log(string(line))
	}
}
