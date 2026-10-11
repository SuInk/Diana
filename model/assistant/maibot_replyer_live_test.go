package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// TestMaiBotReplyerRecordedReplay is an opt-in, read-only replay. Its inputs
// are captured requests from real group messages; it calls only the configured
// model and never executes tools or sends a chat message.
func TestMaiBotReplyerRecordedReplay(t *testing.T) {
	if os.Getenv("DIANA_MAIBOT_REPLYER_REPLAY") != "1" {
		t.Skip("opt-in recorded replyer replay")
	}
	configPath := os.Getenv("DIANA_MAIBOT_PROVIDER_CONFIG")
	inputPaths := filepath.SplitList(os.Getenv("DIANA_MAIBOT_REPLAY_INPUTS"))
	outputPath := os.Getenv("DIANA_MAIBOT_REPLAY_OUTPUT")
	if configPath == "" || len(inputPaths) == 0 || outputPath == "" {
		t.Fatal("provider config, replay inputs and output are required")
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var providerConfig llm.ProviderConfig
	if json.Unmarshal(raw, &providerConfig) != nil || providerConfig.Model != "gemini-3.8-flash-medium" {
		t.Fatal("unexpected replay model")
	}
	providerConfig.Timeout = 90 * time.Second
	client, err := llm.NewClient(providerConfig)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	results := make([]map[string]any, 0, len(inputPaths))
	for _, inputPath := range inputPaths {
		data, err := os.ReadFile(inputPath)
		if err != nil {
			t.Fatal(err)
		}
		var saved struct {
			Results []struct {
				Arm     string              `json:"arm"`
				Case    string              `json:"case"`
				Input   string              `json:"input"`
				Text    string              `json:"text"`
				Request llm.GenerateRequest `json:"request"`
			} `json:"results"`
		}
		if json.Unmarshal(data, &saved) != nil {
			t.Fatalf("invalid replay input %s", inputPath)
		}
		var actual, original *struct {
			Arm     string              `json:"arm"`
			Case    string              `json:"case"`
			Input   string              `json:"input"`
			Text    string              `json:"text"`
			Request llm.GenerateRequest `json:"request"`
		}
		for index := range saved.Results {
			row := &saved.Results[index]
			if row.Arm == "diana_actual" {
				actual = row
			}
			if row.Arm == "python_clean" {
				original = row
			}
		}
		if actual == nil || original == nil || len(original.Request.Messages) == 0 {
			t.Fatalf("incomplete replay input %s", inputPath)
		}
		identity := strings.SplitN(original.Request.Messages[0].Content, "\n现在请你读读之前的聊天记录", 2)[0]
		cfg := (BotConfig{Name: "嘉然", Platform: PlatformOneBotV11, ReplyComposer: "maibot", SystemPrompt: identity}).WithDefaults()
		runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return client, nil })
		runtime.now = func() time.Time { return time.Date(2026, 10, 11, 21, 0, 0, 0, time.Local) }
		event := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, GroupID: "recorded", UserID: "member", SenderName: "群友", MessageID: "recorded-" + actual.Case, RawMessage: actual.Input}
		rendered := runtime.composeMaiBotReply(ctx, cfg, event, actual.Request.Messages, actual.Text)
		results = append(results, map[string]any{"case": actual.Case, "input": actual.Input, "draft": actual.Text, "rendered": rendered})
		t.Logf("%s complete", actual.Case)
	}
	body, _ := json.MarshalIndent(map[string]any{"model": providerConfig.Model, "results": results}, "", "  ")
	if err := os.WriteFile(outputPath, body, 0600); err != nil {
		t.Fatal(err)
	}
}
