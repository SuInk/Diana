// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// TestLiveClaimReviewReplay 重放生产 debug-traces 里录下的收尾复核请求，系统提示分别用
// 录下时的原样和当前代码的 plainTextReviewPrompt，比两边判出的 unfinished / missing。
// 默认跳过：
//
//	DIANA_LIVE_LLM=1 DIANA_TEST_LLM_* DIANA_CLAIM_REVIEW_REPLAY_DIR=<放复核 *-reply.json 的目录> \
//	  scripts/test-serial.sh ./model/agent -run '^TestLiveClaimReviewReplay$' -v
func TestLiveClaimReviewReplay(t *testing.T) {
	dir := strings.TrimSpace(os.Getenv("DIANA_CLAIM_REVIEW_REPLAY_DIR"))
	if dir == "" {
		t.Skip("set DIANA_CLAIM_REVIEW_REPLAY_DIR to a directory of recorded claim review requests")
	}
	var paths []string
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && strings.HasSuffix(path, "-reply.json") {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	client := liveAgentClient(t)
	const marker = "你在检查一条聊天机器人回复草稿"
	verdict := func(req llm.GenerateRequest) string {
		resp, err := client.Generate(context.Background(), req)
		if err != nil {
			if strings.Contains(err.Error(), "429") {
				t.Fatal("rate limited, stop")
			}
			return "ERROR " + err.Error()
		}
		var parsed struct {
			Unfinished string `json:"unfinished"`
			Missing    string `json:"missing"`
		}
		text := strings.TrimSpace(resp.Text)
		if start, end := strings.Index(text, "{"), strings.LastIndex(text, "}"); start >= 0 && end > start {
			text = text[start : end+1]
		}
		if json.Unmarshal([]byte(text), &parsed) != nil {
			return "看不懂「" + truncateForLog(resp.Text, 60) + "」"
		}
		return "unfinished「" + parsed.Unfinished + "」missing「" + parsed.Missing + "」"
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Metadata struct {
				Request llm.GenerateRequest `json:"request"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Metadata.Request.Messages) < 2 {
			t.Fatalf("%s is not a recorded review request: %v", path, err)
		}
		req := envelope.Metadata.Request
		system := req.Messages[0].Content
		index := strings.Index(system, marker)
		if index < 0 {
			continue
		}
		recorded := verdict(req)
		req.Messages[0].Content = system[:index] + plainTextReviewPrompt
		current := verdict(req)
		input := req.Messages[1].Content
		asked, draft := "", ""
		if start := strings.Index(input, "【对方的消息】\n"); start >= 0 {
			asked = input[start+len("【对方的消息】\n"):]
			if end := strings.Index(asked, "\n\n【本轮工具记录】"); end >= 0 {
				asked = asked[:end]
			}
			if head := strings.Index(asked, "【当前需要回复的消息】"); head >= 0 {
				if line := strings.Index(asked[head:], "\n"); line >= 0 {
					asked = asked[head+line+1:]
				}
			}
		}
		if start := strings.Index(input, "【草稿】\n"); start >= 0 {
			draft = input[start+len("【草稿】\n"):]
		}
		rel, _ := filepath.Rel(dir, path)
		t.Logf("%s\n  问：%s\n  稿：%s\n  旧：%s\n  新：%s", rel, truncateForLog(asked, 160), truncateForLog(draft, 120), recorded, current)
	}
}
