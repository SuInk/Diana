package assistant

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// Diagnostic replay of sampled production pairs; historical model labels are
// comparison data, not ground truth. No platform transport is used.
func TestLiveProductionBurstRelations(t *testing.T) {
	path := os.Getenv("DIANA_BURST_REPLAY_ROWS")
	if path == "" {
		t.Skip("set DIANA_BURST_REPLAY_ROWS to sampled production pairs")
	}
	client := liveLLMClient(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Root     MessageEvent   `json:"root"`
		Follow   MessageEvent   `json:"follow"`
		History  []MessageEvent `json:"history"`
		Gap      float64        `json:"gap"`
		Bucket   string         `json:"bucket"`
		Decision struct {
			Relation     string  `json:"relation"`
			MergeAllowed bool    `json:"merge_allowed"`
			Threshold    float64 `json:"merge_threshold"`
		} `json:"decision"`
	}
	if err = json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	for i, row := range rows {
		for sample := 0; sample < 2; sample++ {
			r := topicTestRuntime(client)
			cfg := r.ProfileConfig("")
			cfg.ReplyMergeConfidencePercent = int(row.Decision.Threshold * 100)
			if err := r.ApplyProfiles(context.Background(), NewProfileSet(cfg), nil); err != nil {
				t.Fatal(err)
			}
			// Use a local runtime identity while preserving sender, conversation and quotes.
			row.Root.ProfileID = ""
			row.Follow.ProfileID = ""
			row.Root.replyHistory = row.History
			start := time.Now()
			relation := r.classifyDirectReplyTopic(context.Background(), row.Root, nil, row.Follow, row.Follow.RawMessage)
			t.Logf("pair=%d sample=%d bucket=%s gap=%.3f old=%s allowed=%v result=%s elapsed_ms=%d", i+1, sample+1, row.Bucket, row.Gap, row.Decision.Relation, row.Decision.MergeAllowed, relation, time.Since(start).Milliseconds())
		}
	}
}
