// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// TestLiveUnknownReferenceReplay 重放生产 debug-traces 里主回复的第一步请求，看当前消息
// 提到上下文里找不到来由的旧事时，模型会不会先查 chat_history（见
// promptReplyUnknownReferenceSpec）。默认跳过：
//
//	DIANA_LIVE_LLM=1 DIANA_TEST_LLM_* DIANA_UNKNOWN_REF_REPLAY=<…-reply.json 或目录> go test ./model/assistant -run TestLiveUnknownReferenceReplay
//
// DIANA_UNKNOWN_REF_VARIANTS 逗号分隔：
//   - baseline  原样重放（录下的请求带不带这条检查，取决于录的时候的版本）
//   - check     先去掉录下的检查，再把当前默认的检查接在【输出前检查】后面
//   - strip     去掉录下的检查，量没有这条时的表现
//   - step2     模型已调用 chat_history，接上 DIANA_UNKNOWN_REF_TOOL_RESULT 的检索结果
//     （查询词 DIANA_UNKNOWN_REF_TOOL_QUERY），看第二步是直接答还是换词再查
func TestLiveUnknownReferenceReplay(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("DIANA_UNKNOWN_REF_REPLAY"))
	if path == "" {
		t.Skip("set DIANA_UNKNOWN_REF_REPLAY to a recorded reply request or a directory of them")
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
	rule := promptReplyUnknownReferenceSpec.Default
	client := liveLLMClient(t)
	runs := envInt("DIANA_UNKNOWN_REF_RUNS", 5)
	variants := strings.Split(firstNonEmpty(os.Getenv("DIANA_UNKNOWN_REF_VARIANTS"), "strip,check"), ",")

	// withCheck 去掉录下的检查，need 时再把当前的接上；【输出前检查】挪到最后，第二步也贴着生成。
	withCheck := func(messages []llm.Message, need bool) []llm.Message {
		for index, message := range messages {
			if message.Role != llm.RoleSystem || !strings.HasPrefix(message.Content, "【输出前检查】") {
				continue
			}
			message.Content = strings.TrimSuffix(message.Content, "\n"+rule)
			if need {
				message.Content += "\n" + rule
			}
			return append(append(messages[:index:index], messages[index+1:]...), message)
		}
		return messages
	}
	apply := func(base llm.GenerateRequest, variant string) llm.GenerateRequest {
		req := base
		req.Messages = append([]llm.Message(nil), base.Messages...)
		switch variant {
		case "check", "strip":
			req.Messages = withCheck(req.Messages, variant == "check")
		case "step2":
			result, _ := os.ReadFile(os.Getenv("DIANA_UNKNOWN_REF_TOOL_RESULT"))
			query := firstNonEmpty(os.Getenv("DIANA_UNKNOWN_REF_TOOL_QUERY"), "眼镜 杯子")
			req.Messages = append(req.Messages,
				llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call_unknown_ref_1", Name: "tools_execute", Arguments: map[string]any{"name": "chat_history", "input": map[string]any{"query": query, "limit": 20}}}}},
				llm.Message{Role: llm.RoleTool, ToolCallID: "call_unknown_ref_1", ToolName: "tools_execute", Content: string(result)})
			req.Messages = withCheck(req.Messages, true)
		}
		return req
	}

	type result struct {
		Variant string `json:"variant"`
		Sample  string `json:"sample"`
		Run     int    `json:"run"`
		Lookup  bool   `json:"lookup"`
		Calls   string `json:"calls"`
		Text    string `json:"text,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	// describe 列出这一步的调用；tools_execute、codemode、tool_search 里提到 chat_history 就算去查了。
	describe := func(resp *llm.GenerateResponse) (string, string, bool) {
		var calls []string
		text := strings.TrimSpace(resp.Text)
		lookup := false
		for _, call := range resp.ToolCalls {
			raw, _ := json.Marshal(call.Arguments)
			name := call.Name
			switch call.Name {
			case "send":
				sent, _ := call.Arguments["text"].(string)
				text = sent
			case "tools_execute", "codemode", "tool_search":
				lookup = lookup || strings.Contains(string(raw), "chat_history")
				name += ":" + truncateRunes(string(raw), 160)
			}
			calls = append(calls, name)
		}
		return strings.Join(calls, " ; "), truncateRunes(text, 80), lookup
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
	for worker := 0; worker < envInt("DIANA_UNKNOWN_REF_WORKERS", 3); worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				out := result{Variant: item.variant, Sample: filepath.Base(paths[item.sample]), Run: item.run}
				resp, err := client.Generate(context.Background(), apply(bases[item.sample], item.variant))
				if err != nil {
					out.Error = truncateRunes(err.Error(), 200)
				} else {
					out.Calls, out.Text, out.Lookup = describe(resp)
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
