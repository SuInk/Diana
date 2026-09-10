package assistant

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestTextBudgetCompactsHistoryWithoutTouchingImagesOrCurrentQuestion(t *testing.T) {
	req := llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: "system rules"},
		{Role: llm.RoleUser, Content: strings.Repeat("old", 21000)},
		{Role: llm.RoleUser, Content: "current question", Parts: []llm.ContentPart{{Type: llm.ContentPartText, Text: "current question"}, {Type: llm.ContentPartImageURL, ImageURL: "image", Detail: "high"}}},
	}}
	current := req.Messages[2]
	calls := 0
	got := fitBudgetText(context.Background(), req, 20000, &calls, func(_ context.Context, text string, target int64) (string, error) {
		if text != req.Messages[1].Content {
			t.Fatal("wrong summary input")
		}
		return "older facts", nil
	})
	if calls != 1 || !reflect.DeepEqual(got.Messages[2], current) || got.Messages[0].Content != "system rules" {
		t.Fatal("current input or system changed")
	}
	if llm.PlanInputBudget(got, 20000).OverBudget() {
		t.Fatal("text summary did not resolve overage")
	}
	got = fitImagesWithDescriptions(context.Background(), got, 20000, func(context.Context, string) (string, error) {
		t.Fatal("image descriptions should not run")
		return "", nil
	})
	if got.Messages[2].Parts[1].Detail != "high" {
		t.Fatal("few images were downgraded")
	}
	if req.Messages[1].Content == got.Messages[1].Content {
		t.Fatal("source request mutated")
	}
}

func TestImageQuotaDoesNotCompressFittingText(t *testing.T) {
	req := imageBudgetRequest(18)
	calls := 0
	got := fitBudgetText(context.Background(), req, 128000, &calls, func(context.Context, string, int64) (string, error) {
		t.Fatal("fitting text compressed")
		return "", nil
	})
	got = fitImagesWithDescriptions(context.Background(), got, 128000, func(context.Context, string) (string, error) { return "image facts", nil })
	plan := llm.PlanInputBudget(got, 128000)
	if plan.OverBudget() || calls != 0 {
		t.Fatalf("wrong compression: %+v", plan)
	}
	if plan.TextTokens <= llm.PlanInputBudget(req, 128000).TextTokens {
		t.Fatal("image descriptions not counted as text")
	}
}

func TestTextOnlyOverageNeverReplacesOrDowngradesImages(t *testing.T) {
	req := imageBudgetRequest(2)
	req.Messages[0].Content = strings.Repeat("x", 100000)
	req.Messages[0].Parts[0].Text = req.Messages[0].Content
	budget := int64(40000)
	if llm.PlanInputBudget(req, budget).ImageExcess != 0 {
		t.Fatal("test must be a text-only overage")
	}
	got := fitImagesWithDescriptions(context.Background(), req, budget, func(context.Context, string) (string, error) {
		t.Fatal("text overage caused image description")
		return "", nil
	})
	got = lowerOverBudgetImageDetail(got, budget)
	if !reflect.DeepEqual(got, req) {
		t.Fatal("text overage changed images")
	}
}

func TestBudgetSummaryFailurePreservesToolPairAndCurrentInput(t *testing.T) {
	req := llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleUser, Content: "current question"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "read"}}},
		{Role: llm.RoleTool, ToolCallID: "call-1", Content: strings.Repeat("result", 12000)},
		{Role: llm.RoleAssistant, Content: "continuing"},
	}}
	calls := 0
	got := fitBudgetText(context.Background(), req, 10000, &calls, func(context.Context, string, int64) (string, error) { return "", errors.New("summary unavailable") })
	if !reflect.DeepEqual(got, req) {
		t.Fatal("failed summary changed request")
	}
}

func TestUncompressibleCurrentTextFailsWithoutSendingPartialRequest(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	provider := &privacyRequestProvider{reply: "must not send"}
	client := &imageBudgetProvider{runtime: runtime, provider: provider, group: llm.GroupChat}
	req := llm.GenerateRequest{MaxContextTokens: 8000, Messages: []llm.Message{{Role: llm.RoleUser, Content: strings.Repeat("x", 40000)}}}
	if _, err := client.Generate(context.Background(), req); err == nil {
		t.Fatal("oversized current question sent")
	}
	if len(provider.request.Messages) != 0 {
		t.Fatal("partial request reached provider")
	}
}

func TestBudgetTextPreservesHistoricalToolPair(t *testing.T) {
	req := llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleUser, Content: "old question"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "read-1", Name: "read"}}},
		{Role: llm.RoleTool, ToolCallID: "read-1", ToolName: "read", Content: strings.Repeat("old result", 9000)},
		{Role: llm.RoleUser, Content: "new question"},
	}}
	calls := 0
	got := fitBudgetText(context.Background(), req, 10000, &calls, func(context.Context, string, int64) (string, error) { return "confirmed earlier findings", nil })
	if calls != 1 || got.Messages[2].ToolCallID != "read-1" || got.Messages[2].Role != llm.RoleTool || !reflect.DeepEqual(got.Messages[1], req.Messages[1]) || got.Messages[3].Content != "new question" {
		t.Fatal("tool pair or current question changed")
	}
	if got.Messages[2].Content == req.Messages[2].Content {
		t.Fatal("oversized historical tool result not summarized")
	}
}
