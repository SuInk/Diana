package assistant

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// TestLiveFeedbackFinalizeReplay 拿生产 debug-traces 里私聊主回复的第一步请求，在 send 上
// 补齐 QQ 官方单聊会带的收尾字段（按钮、版式、卡片、推荐回复、赞踩栏）重放，量模型多常
// 要赞踩栏、要在什么样的回复上。默认跳过：
//
//	DIANA_LIVE_LLM=1 DIANA_TEST_LLM_* DIANA_FEEDBACK_REPLAY_DIR=<traces> go test ./model/assistant -run TestLiveFeedbackFinalizeReplay
func TestLiveFeedbackFinalizeReplay(t *testing.T) {
	dir := strings.TrimSpace(os.Getenv("DIANA_FEEDBACK_REPLAY_DIR"))
	if dir == "" {
		t.Skip("set DIANA_FEEDBACK_REPLAY_DIR to replay recorded private reply requests")
	}
	type sample struct {
		id      string
		request llm.GenerateRequest
	}
	var samples []sample
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, "-reply.json") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var envelope struct {
			Metadata struct {
				Kind    string              `json:"kind"`
				Request llm.GenerateRequest `json:"request"`
			} `json:"metadata"`
		}
		if json.Unmarshal(raw, &envelope) != nil || envelope.Metadata.Kind != string(EventKindPrivate) {
			return nil
		}
		req := envelope.Metadata.Request
		// 现在的请求尾部还跟着几条 system（人设重申等），第一步看的是最后一条非 system 消息。
		if last, ok := feedbackReplayLastTurn(req); !ok || last.Role != llm.RoleUser || !buttonsReplayHasFinalize(req) {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		samples = append(samples, sample{id: rel, request: feedbackReplayWithFields(trimStickerReplayRequest(req, envInt("DIANA_FEEDBACK_REPLAY_TAIL", 60)))})
		return nil
	})
	sort.Slice(samples, func(i, j int) bool { return samples[i].id < samples[j].id })
	if limit := envInt("DIANA_FEEDBACK_REPLAY_LIMIT", 0); limit > 0 && len(samples) > limit {
		// 均匀抽样，免得只取到同一个人同一天的对话。
		picked := make([]sample, 0, limit)
		for index := 0; index < limit; index++ {
			picked = append(picked, samples[index*len(samples)/limit])
		}
		samples = picked
	}
	if len(samples) == 0 {
		t.Fatal("no replayable private reply requests")
	}
	client := liveLLMClient(t)
	runs := envInt("DIANA_FEEDBACK_REPLAY_RUNS", 2)
	type result struct {
		ID       string `json:"id"`
		Run      int    `json:"run"`
		Last     string `json:"last"`
		Feedback string `json:"feedback,omitempty"`
		Others   string `json:"others,omitempty"`
		Content  string `json:"content,omitempty"`
		Calls    string `json:"calls,omitempty"`
		Error    string `json:"error,omitempty"`
	}
	type job struct {
		sample sample
		run    int
	}
	jobs := make(chan job)
	var mu sync.Mutex
	var results []result
	var workers sync.WaitGroup
	for range envInt("DIANA_FEEDBACK_REPLAY_CONCURRENCY", 3) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for item := range jobs {
				var response *llm.GenerateResponse
				var err error
				for attempt := 0; attempt < 4; attempt++ {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
					response, err = client.Generate(ctx, item.sample.request)
					cancel()
					if err == nil || !(strings.Contains(err.Error(), " 503 ") || strings.Contains(err.Error(), " 429 ")) {
						break
					}
					time.Sleep(stickerReplayBackoff(err, attempt))
				}
				last, _ := feedbackReplayLastTurn(item.sample.request)
				out := result{ID: item.sample.id, Run: item.run, Last: truncateRunesFromStart(last.Content, 400)}
				if err != nil {
					out.Error = err.Error()
				} else {
					var calls, others []string
					for _, call := range response.ToolCalls {
						calls = append(calls, call.Name)
						if call.Name != agent.FinalizeToolName {
							continue
						}
						out.Feedback, _ = call.Arguments[feedbackFinalizeFieldName].(string)
						out.Content, _ = call.Arguments["text"].(string)
						for _, name := range []string{buttonsFinalizeFieldName, messageStyleFinalizeFieldName, cardFinalizeFieldName, suggestionsFinalizeFieldName} {
							if value, _ := call.Arguments[name].(string); strings.TrimSpace(value) != "" {
								others = append(others, name)
							}
						}
					}
					out.Calls, out.Others = strings.Join(calls, ","), strings.Join(others, ",")
					if out.Content == "" {
						out.Content = response.Text
					}
					out.Content = truncateRunes(out.Content, 300)
				}
				mu.Lock()
				results = append(results, out)
				mu.Unlock()
			}
		}()
	}
	for run := 1; run <= runs; run++ {
		for _, item := range samples {
			jobs <- job{sample: item, run: run}
		}
	}
	close(jobs)
	workers.Wait()
	if path := os.Getenv("DIANA_FEEDBACK_REPLAY_OUT"); path != "" {
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		encoder := json.NewEncoder(file)
		encoder.SetEscapeHTML(false)
		for _, item := range results {
			_ = encoder.Encode(item)
		}
		_ = file.Close()
	}
	finalized, asked, filled, others, failed := 0, 0, 0, 0, 0
	for _, item := range results {
		switch {
		case item.Error != "":
			failed++
		case strings.Contains(item.Calls, agent.FinalizeToolName):
			finalized++
			if strings.TrimSpace(item.Feedback) != "" {
				filled++
			}
			if parseFeedback(item.Feedback) {
				asked++
			}
			if item.Others != "" {
				others++
			}
		}
	}
	t.Logf("样本 %d × %d 次：直接收尾 %d，feedback 填了 %d（要挂 %d），填了别的附加字段 %d，失败 %d", len(samples), runs, finalized, filled, asked, others, failed)
}

// feedbackReplayWithFields 给录制时的 send 补上 QQ 官方单聊的全部收尾字段，其余定义原样保留。
func feedbackReplayWithFields(req llm.GenerateRequest) llm.GenerateRequest {
	tools := append([]llm.ToolDefinition(nil), req.Tools...)
	for index := range tools {
		if tools[index].Name != agent.FinalizeToolName {
			continue
		}
		parameters := map[string]any{}
		for key, value := range tools[index].Parameters {
			parameters[key] = value
		}
		properties := map[string]any{}
		if existing, ok := parameters["properties"].(map[string]any); ok {
			for key, value := range existing {
				properties[key] = value
			}
		}
		for _, field := range []agent.FinalizeField{buttonsFinalizeField(), messageStyleFinalizeField(), cardFinalizeField(), suggestionsFinalizeField(), feedbackFinalizeField()} {
			if field.Name == feedbackFinalizeFieldName {
				// DIANA_FEEDBACK_REPLAY_DESC 指向一份候选描述，用来对比不同写法。
				if path := os.Getenv("DIANA_FEEDBACK_REPLAY_DESC"); path != "" {
					if raw, err := os.ReadFile(path); err == nil {
						field.Description = strings.TrimSpace(string(raw))
					}
				}
			}
			properties[field.Name] = toolStringParam(field.Description)
		}
		parameters["properties"] = properties
		tools[index].Parameters = parameters
	}
	req.Tools = tools
	return req
}

// feedbackReplayLastTurn 是请求里最后一条非 system 消息。
func feedbackReplayLastTurn(req llm.GenerateRequest) (llm.Message, bool) {
	for index := len(req.Messages) - 1; index >= 0; index-- {
		if req.Messages[index].Role != llm.RoleSystem {
			return req.Messages[index], true
		}
	}
	return llm.Message{}, false
}
