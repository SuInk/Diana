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

	"github.com/SuInk/diana/model/llm"
)

// TestLiveButtonsFinalizeReplay 拿生产 debug-traces 里主回复的第一步请求，在
// agent_finalize 上补 buttons 字段重放，量模型多常填、填的是什么。默认跳过：
//
//	DIANA_LIVE_LLM=1 DIANA_TEST_LLM_* DIANA_BUTTONS_REPLAY_DIR=<traces> go test ./model/assistant -run TestLiveButtonsFinalizeReplay
func TestLiveButtonsFinalizeReplay(t *testing.T) {
	dir := strings.TrimSpace(os.Getenv("DIANA_BUTTONS_REPLAY_DIR"))
	if dir == "" {
		t.Skip("set DIANA_BUTTONS_REPLAY_DIR to replay recorded reply requests")
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
				Request llm.GenerateRequest `json:"request"`
			} `json:"metadata"`
		}
		if json.Unmarshal(raw, &envelope) != nil {
			return nil
		}
		req := envelope.Metadata.Request
		if len(req.Messages) == 0 || req.Messages[len(req.Messages)-1].Role != llm.RoleUser || !buttonsReplayHasFinalize(req) {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		samples = append(samples, sample{id: rel, request: buttonsReplayWithField(trimStickerReplayRequest(req, envInt("DIANA_BUTTONS_REPLAY_TAIL", 60)))})
		return nil
	})
	sort.Slice(samples, func(i, j int) bool { return samples[i].id < samples[j].id })
	if limit := envInt("DIANA_BUTTONS_REPLAY_LIMIT", 0); limit > 0 && len(samples) > limit {
		samples = samples[len(samples)-limit:]
	}
	if len(samples) == 0 {
		t.Fatal("no replayable reply requests")
	}
	client := liveLLMClient(t)
	runs := envInt("DIANA_BUTTONS_REPLAY_RUNS", 2)
	type result struct {
		ID      string `json:"id"`
		Run     int    `json:"run"`
		Last    string `json:"last"`
		Buttons string `json:"buttons,omitempty"`
		Content string `json:"content,omitempty"`
		Calls   string `json:"calls,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	type job struct {
		sample sample
		run    int
	}
	jobs := make(chan job)
	var mu sync.Mutex
	var results []result
	var workers sync.WaitGroup
	for range envInt("DIANA_BUTTONS_REPLAY_CONCURRENCY", 4) {
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
				messages := item.sample.request.Messages
				out := result{ID: item.sample.id, Run: item.run, Last: truncateRunes(messages[len(messages)-1].Content, 400)}
				if err != nil {
					out.Error = err.Error()
				} else {
					var calls []string
					for _, call := range response.ToolCalls {
						calls = append(calls, call.Name)
						if call.Name == "agent_finalize" {
							out.Buttons, _ = call.Arguments[buttonsFinalizeFieldName].(string)
							out.Content, _ = call.Arguments["content"].(string)
						}
					}
					out.Calls = strings.Join(calls, ",")
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
	if path := os.Getenv("DIANA_BUTTONS_REPLAY_OUT"); path != "" {
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
	finalized, filled, failed := 0, 0, 0
	for _, item := range results {
		switch {
		case item.Error != "":
			failed++
		case strings.Contains(item.Calls, "agent_finalize"):
			finalized++
			if strings.TrimSpace(item.Buttons) != "" {
				filled++
			}
		}
	}
	t.Logf("样本 %d × %d 次：直接收尾 %d，填了按钮 %d，失败 %d", len(samples), runs, finalized, filled, failed)
}

func buttonsReplayHasFinalize(req llm.GenerateRequest) bool {
	for _, tool := range req.Tools {
		if tool.Name == "agent_finalize" {
			return true
		}
	}
	return false
}

// buttonsReplayWithField 给录制时的收尾工具补上 buttons 字段，其余定义原样保留。
func buttonsReplayWithField(req llm.GenerateRequest) llm.GenerateRequest {
	tools := append([]llm.ToolDefinition(nil), req.Tools...)
	for index := range tools {
		if tools[index].Name != "agent_finalize" {
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
		field := buttonsFinalizeField()
		// DIANA_BUTTONS_REPLAY_DESC 指向一份候选描述，用来对比不同写法的填写率。
		if path := os.Getenv("DIANA_BUTTONS_REPLAY_DESC"); path != "" {
			if raw, err := os.ReadFile(path); err == nil {
				field.Description = strings.TrimSpace(string(raw))
			}
		}
		properties[field.Name] = toolStringParam(field.Description)
		parameters["properties"] = properties
		tools[index].Parameters = parameters
	}
	req.Tools = tools
	return req
}
