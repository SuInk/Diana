// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// TestLiveStickerSelectionReplay 在真实对话上走一遍「检索 → 挑一张」：模型按新说明写关键词，
// 检索和候选排序走真正的 sticker 工具，表情包库用导出的线上库存；最后记下模型挑了哪张
// （或者不发），供人判断合不合适。不真的发图。
//
//	DIANA_STICKER_REPLAY_DIR / DIANA_STICKER_REPLAY_IDS / DIANA_STICKER_REPLAY_TAIL 同触发回放
//	DIANA_STICKER_SELECT_LIBRARY=<jsonl>  每行一条线上 sticker_assets（含 description），字段见 stickerLibraryRow
//	DIANA_STICKER_SELECT_OUT=<jsonl>      每条样本的关键词、候选和最终选择
func TestLiveStickerSelectionReplay(t *testing.T) {
	libraryPath := strings.TrimSpace(os.Getenv("DIANA_STICKER_SELECT_LIBRARY"))
	dir := strings.TrimSpace(os.Getenv("DIANA_STICKER_REPLAY_DIR"))
	if libraryPath == "" || dir == "" {
		t.Skip("set DIANA_STICKER_REPLAY_DIR and DIANA_STICKER_SELECT_LIBRARY to replay sticker selection")
	}
	library := loadStickerLibraryRows(t, libraryPath)
	// 库存里的路径在生产机上，本机不存在；候选阶段会按失效文件滤掉。换成本地占位文件，
	// 输出里仍然给原路径，方便事后去生产机取图。
	stubDir := t.TempDir()
	remotePaths := map[string]string{}
	for index := range library {
		remotePaths[library[index].Hash] = library[index].Path
		stub := stubDir + "/" + library[index].Hash
		if err := os.WriteFile(stub, []byte(library[index].Hash), 0o600); err != nil {
			t.Fatal(err)
		}
		library[index].Path = stub
	}
	samples := loadStickerReplaySamples(t, dir, envInt("DIANA_STICKER_REPLAY_TAIL", 60))
	samples = filterStickerReplaySamples(t, samples)
	metas := loadStickerSampleMeta(t, dir)
	client := stickerReplayClient(t)

	type candidateView struct {
		Name        string   `json:"name"`
		Tags        []string `json:"tags,omitempty"`
		Description string   `json:"description,omitempty"`
		Matched     bool     `json:"matched"`
		Hash        string   `json:"hash"`
		Path        string   `json:"path"`
	}
	type outcome struct {
		ID         string          `json:"id"`
		Library    int             `json:"library"`
		Query      string          `json:"query"`
		Candidates []candidateView `json:"candidates"`
		Chosen     *candidateView  `json:"chosen,omitempty"`
		Text       string          `json:"text,omitempty"`
		Error      string          `json:"error,omitempty"`
	}
	generate := func(req llm.GenerateRequest) (*llm.GenerateResponse, error) {
		var response *llm.GenerateResponse
		var err error
		for attempt := 0; attempt < 4; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			response, err = client.Generate(ctx, req)
			cancel()
			if err == nil || !(strings.Contains(err.Error(), " 503 ") || strings.Contains(err.Error(), " 429 ")) {
				break
			}
			time.Sleep(stickerReplayBackoff(err, attempt))
		}
		return response, err
	}
	run := func(sample stickerReplaySample) outcome {
		out := outcome{ID: sample.id}
		meta := metas[sample.id]
		event := MessageEvent{Kind: EventKind(meta.Kind), GroupID: meta.Group, UserID: meta.User, MessageID: "replay"}
		if event.Kind == EventKindPrivate {
			event.GroupID = ""
		}
		var assets []StickerAsset
		for _, row := range library {
			if row.Kind == meta.Kind && ((row.Kind == "group" && row.GroupID == meta.Group) || (row.Kind == "private" && strings.HasSuffix(row.Session, ":private:"+meta.User))) {
				assets = append(assets, row.asset(sessionKey(event)))
			}
		}
		out.Library = len(assets)
		store := &stickerAssetTestStore{stickerHistoryStore: stickerHistoryStore{events: map[string][]MessageEvent{}}, assets: assets}
		runtime := NewRuntime(BotConfig{}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, nil)
		runtime.SetMessageHistoryStore(stickerSearchOnlyStore{store})
		tool := newDianaStickerTool(runtime, event, nil)
		definition := llm.ToolDefinition{Name: tool.Name(), Description: tool.Description(), Parameters: tool.InputSchema()}

		req := stickerReplayVariant(sample.request, "new")
		req.Tools = []llm.ToolDefinition{definition}
		req.ToolChoice = dianaStickerToolName
		first, err := generate(req)
		if err != nil {
			out.Error = err.Error()
			return out
		}
		var searchCall *llm.ToolCall
		for index := range first.ToolCalls {
			if first.ToolCalls[index].Name == dianaStickerToolName {
				searchCall = &first.ToolCalls[index]
				break
			}
		}
		if searchCall == nil {
			out.Error = "model did not call sticker"
			return out
		}
		input := map[string]any{"operation": "search", "query": searchCall.Arguments["query"]}
		out.Query, _ = input["query"].(string)
		result, err := tool.Run(context.Background(), input)
		if err != nil {
			out.Error = err.Error()
			return out
		}
		var parsed stickerToolResult
		_ = json.Unmarshal([]byte(result), &parsed)
		for _, item := range parsed.Candidates {
			candidate, _ := tool.searchedCandidate(item.ID)
			out.Candidates = append(out.Candidates, candidateView{Name: item.Name, Tags: item.Tags, Description: item.Description, Matched: item.Matched, Hash: candidate.Hash, Path: remotePaths[candidate.Hash]})
		}

		searchCall.Arguments = input
		req.Messages = append(append([]llm.Message(nil), req.Messages...),
			llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{*searchCall}},
			llm.Message{Role: llm.RoleTool, ToolCallID: searchCall.ID, ToolName: dianaStickerToolName, Content: result},
		)
		req.ToolChoice = ""
		second, err := generate(req)
		if err != nil {
			out.Error = err.Error()
			return out
		}
		out.Text = truncateRunes(second.Text, 200)
		for _, call := range second.ToolCalls {
			if call.Name != dianaStickerToolName || call.Arguments["operation"] != "send" {
				continue
			}
			id, _ := call.Arguments["sticker_id"].(string)
			if candidate, ok := tool.searchedCandidate(id); ok {
				out.Chosen = &candidateView{Name: candidate.Summary, Tags: candidate.Tags, Description: candidate.Description, Hash: candidate.Hash, Path: remotePaths[candidate.Hash]}
			}
		}
		return out
	}

	jobs := make(chan stickerReplaySample)
	var mu sync.Mutex
	var outcomes []outcome
	var workers sync.WaitGroup
	for range envInt("DIANA_STICKER_REPLAY_CONCURRENCY", 2) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for sample := range jobs {
				result := run(sample)
				mu.Lock()
				outcomes = append(outcomes, result)
				mu.Unlock()
			}
		}()
	}
	for _, sample := range samples {
		jobs <- sample
	}
	close(jobs)
	workers.Wait()

	chosen, failed := 0, 0
	for _, item := range outcomes {
		if item.Error != "" {
			failed++
		} else if item.Chosen != nil {
			chosen++
		}
	}
	t.Logf("samples=%d chosen=%d declined=%d failed=%d", len(outcomes), chosen, len(outcomes)-chosen-failed, failed)
	if path := os.Getenv("DIANA_STICKER_SELECT_OUT"); path != "" {
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		encoder := json.NewEncoder(file)
		for _, item := range outcomes {
			_ = encoder.Encode(item)
		}
		_ = file.Close()
	}
}

// stickerSearchOnlyStore 只暴露资产库，回放时不记发送、不补标签。
type stickerSearchOnlyStore struct{ inner *stickerAssetTestStore }

func (s stickerSearchOnlyStore) AppendMessageEvent(ctx context.Context, session string, event MessageEvent) error {
	return s.inner.AppendMessageEvent(ctx, session, event)
}

func (s stickerSearchOnlyStore) ListRecentMessageEvents(ctx context.Context, session string, limit int) ([]MessageEvent, error) {
	return s.inner.ListRecentMessageEvents(ctx, session, limit)
}

func (s stickerSearchOnlyStore) ListStickerAssets(ctx context.Context, query StickerHistoryQuery) ([]StickerAsset, error) {
	return s.inner.ListStickerAssets(ctx, query)
}

type stickerLibraryRow struct {
	Session     string `json:"session"`
	ProfileID   string `json:"profile_id"`
	Namespace   string `json:"context_namespace"`
	Kind        string `json:"kind"`
	GroupID     string `json:"group_id"`
	UserID      string `json:"user_id"`
	MessageID   string `json:"message_id"`
	EventTime   int64  `json:"event_time"`
	Summary     string `json:"summary"`
	Path        string `json:"path"`
	MIME        string `json:"mime"`
	Hash        string `json:"hash"`
	Description string `json:"description"`
}

// asset 把线上一行库存当成当前会话的表情包（新旧会话键都算本会话，和存储层一致）。
func (row stickerLibraryRow) asset(session string) StickerAsset {
	return StickerAsset{
		Session: session, ProfileID: row.ProfileID, ContextNamespace: row.Namespace, Kind: EventKind(row.Kind),
		GroupID: row.GroupID, UserID: row.UserID, MessageID: row.MessageID, EventTime: row.EventTime,
		Summary: row.Summary, Path: row.Path, MIME: row.MIME, ContentSHA256: row.Hash, Description: row.Description,
	}
}

func loadStickerLibraryRows(t *testing.T, path string) []stickerLibraryRow {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var rows []stickerLibraryRow
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 16<<20)
	for scanner.Scan() {
		var row stickerLibraryRow
		if json.Unmarshal(scanner.Bytes(), &row) == nil && row.Hash != "" {
			rows = append(rows, row)
		}
	}
	return rows
}

type stickerSampleMeta struct {
	Kind  string `json:"kind"`
	Group string `json:"group_id"`
	User  string `json:"user_id"`
}

// loadStickerSampleMeta 按和 loadStickerReplaySamples 相同的 id 规则取每条样本的会话。
func loadStickerSampleMeta(t *testing.T, dir string) map[string]stickerSampleMeta {
	t.Helper()
	metas := map[string]stickerSampleMeta{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		raw, err := os.ReadFile(dir + "/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		for index, line := range strings.Split(string(raw), "\n") {
			var envelope struct {
				Metadata stickerSampleMeta `json:"metadata"`
			}
			if strings.TrimSpace(line) != "" && json.Unmarshal([]byte(line), &envelope) == nil {
				metas[entry.Name()+"#"+itoa(index+1)] = envelope.Metadata
			}
		}
	}
	return metas
}
