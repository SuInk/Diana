package assistant

import (
	"context"
	"encoding/json"
	"testing"
)

func TestReplyMergeThresholdConfigRoundTrip(t *testing.T) {
	if DefaultBotConfig().ReplyMergeConfidencePercent != 75 || (BotConfig{}).WithDefaults().ReplyMergeConfidencePercent != 75 {
		t.Fatal("default merge threshold must be 75 percent")
	}
	cfg := BotConfig{ReplyMergeConfidencePercent: 82}.WithDefaults()
	raw, err := json.Marshal(PayloadFromConfig(cfg))
	if err != nil {
		t.Fatal(err)
	}
	var payload ConfigPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if got := ConfigFromPayload(payload, BotConfig{}).WithDefaults().ReplyMergeConfidencePercent; got != 82 {
		t.Fatalf("round trip threshold=%d", got)
	}
	for _, tc := range []struct{ input, want int }{{-1, 75}, {0, 75}, {1, 1}, {100, 100}, {101, 100}} {
		if got := (BotConfig{ReplyMergeConfidencePercent: tc.input}).WithDefaults().ReplyMergeConfidencePercent; got != tc.want {
			t.Fatalf("input=%d threshold=%d want=%d", tc.input, got, tc.want)
		}
	}
	if got := DefaultGroupConfig("g", cfg).ReplyMergeConfidencePercent; got != 0 {
		t.Fatalf("new group must inherit dynamically, got %d", got)
	}
}

func TestReplyMergeThresholdGroupOverrideControlsMerging(t *testing.T) {
	provider := &topicTestProvider{result: `{"relation":"supplement","confidence":0.78}`}
	for _, tc := range []struct {
		name       string
		bot, group int
		want       string
	}{
		{"default", 0, 0, "supplement"},
		{"strict bot inherited", 90, 0, "uncertain"},
		{"group relaxes threshold", 90, 75, "supplement"},
		{"group tightens threshold", 75, 90, "uncertain"},
		{"updated bot inherited", 70, 0, "supplement"},
		{"boundary", 78, 0, "supplement"},
		{"below configured threshold", 79, 0, "uncertain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := topicTestRuntime(provider)
			cfg := r.Config()
			cfg.ReplyMergeConfidencePercent = tc.bot
			if err := r.UpdateConfigInPlace(cfg); err != nil {
				t.Fatal(err)
			}
			r.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
				"g": {GroupID: "g", ReplyMergeConfidencePercent: tc.group},
			}})
			root := directedGroupMessage("root", "user", "说明当前情况")
			root.GroupID = "g"
			follow := root
			follow.MessageID = "follow"
			if got := r.classifyDirectReplyTopic(context.Background(), root, nil, follow, "再补充一个条件"); got != tc.want {
				t.Fatalf("relation=%s want=%s", got, tc.want)
			}
			provider.result = `{"relation":"independent","confidence":1}`
			if got := r.classifyDirectReplyTopic(context.Background(), root, nil, follow, "另一个问题"); got != "uncertain" {
				t.Fatalf("independent message merged: %s", got)
			}
			provider.result = `{"relation":"supplement","confidence":0.78}`
		})
	}
}
