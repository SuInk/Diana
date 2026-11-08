// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// TestLivePlainTextClaimReplay 重放生产 debug-traces 里的一条主回复请求，看模型是
// 真去调工具，还是写一句「在画了」就收尾。默认跳过：
//
//	DIANA_LIVE_LLM=1 DIANA_TEST_LLM_* DIANA_PLAIN_CLAIM_REPLAY_FILE=<…-reply.json> go test ./model/assistant -run TestLivePlainTextClaimReplay
//
// DIANA_PLAIN_CLAIM_REPLAY_VARIANTS 逗号分隔：
//   - baseline  原样重放，允许纯文本
//   - repair    原样重放；回了纯文本就退回一句「纯文本不会发出去」再问一次
//   - history_send  历史里机器人自己的回复改成 send 调用再重放
//   - history_send_repair  历史改成 send 调用，回了纯文本再退回一次（主回复的正式行为）
//   - repair_fixed  直接接上 DIANA_PLAIN_CLAIM_REPLAY_CLAIM 那句纯文本和退回的话，只量第二步
func TestLivePlainTextClaimReplay(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("DIANA_PLAIN_CLAIM_REPLAY_FILE"))
	if path == "" {
		t.Skip("set DIANA_PLAIN_CLAIM_REPLAY_FILE to a recorded reply request")
	}
	// 给目录就把里面每条 *-reply.json 都重放一遍。
	paths := []string{path}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		paths, _ = filepath.Glob(filepath.Join(path, "*-reply.json"))
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
	client := liveLLMClient(t)
	runs := envInt("DIANA_PLAIN_CLAIM_REPLAY_RUNS", 10)
	variants := strings.Split(firstNonEmpty(os.Getenv("DIANA_PLAIN_CLAIM_REPLAY_VARIANTS"), "baseline,history_send,repair"), ",")
	// DIANA_PLAIN_CLAIM_REPLAY_REPAIR_PROMPT 换掉退回时对模型说的话，比较「丢弃重写」和「留作草稿」两种说法。
	repairPrompt := firstNonEmpty(os.Getenv("DIANA_PLAIN_CLAIM_REPLAY_REPAIR_PROMPT"), "这段普通文本没有发出去，对方什么也没看到。只有 send 会把话发给对方；要做事就先调用对应工具（按需工具用 tools_execute），做完再 send。")

	fixedClaim := firstNonEmpty(os.Getenv("DIANA_PLAIN_CLAIM_REPLAY_CLAIM"), "在画了，画好自动发出来")

	type result struct {
		Variant string `json:"variant"`
		Sample  string `json:"sample,omitempty"`
		Run     int    `json:"run"`
		Calls   string `json:"calls"`
		Silent  bool   `json:"silent"`
		Text    string `json:"text,omitempty"`
		Second  string `json:"second,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	// describe 返回调用列表、正文和这一步是不是静默（send silent=true，或 send 正文留空）。
	describe := func(resp *llm.GenerateResponse) (string, string, bool) {
		var calls []string
		text := strings.TrimSpace(resp.Text)
		silent := false
		for _, call := range resp.ToolCalls {
			name := call.Name
			if inner, _ := call.Arguments["name"].(string); name == "tools_execute" && inner != "" {
				name += ":" + inner
			}
			if call.Name == "send" {
				sent, _ := call.Arguments["text"].(string)
				flagged, _ := call.Arguments["silent"].(bool)
				if flagged || strings.TrimSpace(sent) == "" {
					silent = true
					name += "(silent)"
				} else {
					text = sent
					// continue=true 是「先说一句、下一步接着调工具」，task_state 是图片任务的回执。
					name += fmt.Sprintf("(continue=%v,task_state=%v)", call.Arguments["continue"], call.Arguments["task_state"])
				}
			}
			calls = append(calls, name)
		}
		return strings.Join(calls, ","), truncateRunes(text, 60), silent
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
	for worker := 0; worker < envInt("DIANA_PLAIN_CLAIM_REPLAY_WORKERS", 2); worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				base := bases[item.sample]
				req := base
				req.Messages = append([]llm.Message(nil), base.Messages...)
				switch item.variant {
				case "history_send", "history_send_repair":
					// 历史里机器人自己的回复改成正式代码拼的 send 调用（agent.HistoryReplyMessages），
					// 看它还会不会学着写纯文本。
					var converted []llm.Message
					for index, message := range req.Messages {
						if message.Role != llm.RoleAssistant || len(message.ToolCalls) > 0 || strings.TrimSpace(message.Content) == "" {
							converted = append(converted, message)
							continue
						}
						pair := agent.HistoryReplyMessages(strconv.Itoa(index), message.Content)
						for pairIndex := range pair {
							pair[pairIndex].Priority, pair[pairIndex].ContextGroup = message.Priority, message.ContextGroup
						}
						pair[len(pair)-1].CacheBreakpoint = message.CacheBreakpoint
						// DIANA_PLAIN_CLAIM_REPLAY_RESULT 换掉「已发送」，比较结果措辞对静默的影响。
						if result := os.Getenv("DIANA_PLAIN_CLAIM_REPLAY_RESULT"); result != "" {
							pair[len(pair)-1].Content = result
						}
						converted = append(converted, pair...)
					}
					req.Messages = converted
				case "repair_fixed":
					req.Messages = append(req.Messages, llm.Message{Role: llm.RoleAssistant, Content: fixedClaim}, llm.Message{Role: llm.RoleUser, Content: repairPrompt})
				}
				out := result{Variant: item.variant, Run: item.run}
				if len(bases) > 1 {
					out.Sample = filepath.Base(paths[item.sample])
				}
				resp, err := client.Generate(context.Background(), req)
				if err != nil {
					out.Error = err.Error()
				} else {
					out.Calls, out.Text, out.Silent = describe(resp)
					if (item.variant == "repair" || item.variant == "history_send_repair") && len(resp.ToolCalls) == 0 {
						req.Messages = append(req.Messages, llm.Message{Role: llm.RoleAssistant, Content: resp.Text}, llm.Message{Role: llm.RoleUser, Content: repairPrompt})
						if second, err := client.Generate(context.Background(), req); err != nil {
							out.Error = err.Error()
						} else {
							calls, text, _ := describe(second)
							out.Second = calls + " | " + text
						}
					}
				}
				mu.Lock()
				results = append(results, out)
				mu.Unlock()
			}
		}()
	}
	// 变体放在最里层交错排队，几个变体在同一时段跑，网关和模型的时段波动两边一样。
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
