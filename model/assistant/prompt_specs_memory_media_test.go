// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 拆出 Contract 的几段，默认正文加 Contract 必须和拆之前的整段常量逐字节相同。
func TestMemoryAndSocialPromptSpecsKeepOriginalBytes(t *testing.T) {
	for _, tc := range []struct {
		spec *PromptSpec
		want string
	}{
		{promptMemoryGateSpec, memoryGateSystemPrompt},
		{promptMemorySummarySpec, memorySummarySystemPrompt},
		{promptRelationshipEvaluationSpec, relationshipEvaluationSystemPrompt},
		{promptImageOCRSpec, strings.Replace(promptImageOCRSpec.Default, "代码块。", "代码块。图片没有可辨文字时只返回“[无可辨文字]”。", 1)},
		{promptWelcomeGeneratorSpec, welcomeGeneratorPrompt},
	} {
		if got := PromptOverrides(nil).text(tc.spec); got != tc.want {
			t.Errorf("%s default drifted:\n%q\nwant\n%q", tc.spec.Key, got, tc.want)
		}
	}
}

// 解析输出的提示词被改写后，锁定的输出格式仍然跟在后面。
func TestRelationshipPromptOverrideKeepsJSONContract(t *testing.T) {
	cfg := BotConfig{PromptOverrides: PromptOverrides{promptRelationshipEvaluationSpec.Key: "只看对方有没有骂人。"}}
	got := cfg.prompt(promptRelationshipEvaluationSpec)
	if !strings.HasPrefix(got, "只看对方有没有骂人。\n15. 只输出一个合法 JSON 对象") {
		t.Fatalf("override lost the contract: %q", got)
	}
	if strings.Contains(got, "关系变化评估器") {
		t.Fatalf("default body leaked into override: %q", got)
	}
}

func TestSubtaskPromptOverrideReachesProvider(t *testing.T) {
	provider := &subtaskCaptureProvider{reply: "是"}
	runtime := NewRuntime(BotConfig{
		BotAccount:      "42",
		PromptOverrides: PromptOverrides{promptSubtaskSpec.Key: "只回答是或否。"},
	}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	tool := newDianaSubtaskTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1"})
	if _, err := tool.Run(context.Background(), map[string]any{"question": "有猫吗", "material": "院子里有一只猫。"}); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider calls = %d", len(provider.requests))
	}
	// 隐私代理会在系统提示词前面加一段说明，这里只看正文。
	if got := provider.requests[0].Messages[0].Content; !strings.HasSuffix(got, "\n\n只回答是或否。") || strings.Contains(got, "被拆出来的小问题") {
		t.Fatalf("subtask system prompt = %q", got)
	}
}

func TestSummarizeBudgetTextRendersTargetPlaceholder(t *testing.T) {
	provider := &subtaskCaptureProvider{reply: "摘要"}
	runtime := NewRuntime(BotConfig{
		BotAccount:      "42",
		PromptOverrides: PromptOverrides{promptBudgetSummarySpec.Key: "压到 {tokens} 以内。"},
	}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	if _, err := runtime.summarizeBudgetText(context.Background(), "很长的历史", 300); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider calls = %d", len(provider.requests))
	}
	if got := provider.requests[0].Messages[0].Content; !strings.HasSuffix(got, "\n\n压到 300 以内。") {
		t.Fatalf("budget summary prompt = %q", got)
	}
}

// 文档 OCR 跑在后台任务里拿不到配置，覆盖随 ctx 带进去；没带时走默认值。
func TestDocumentPageOCRReadsOverridesFromContext(t *testing.T) {
	var captured llm.GenerateRequest
	services := PluginTaskServices{Generate: func(_ context.Context, req llm.GenerateRequest) (string, error) {
		captured = req
		return "第一行", nil
	}}

	if _, err := runPageVisionOCR(context.Background(), services, "合同.pdf", 2, 5, []byte{0xff}); err != nil {
		t.Fatal(err)
	}
	if got := captured.Messages[1].Content; got != "文档《合同.pdf》第 2/5 页。请完整转写本页。" {
		t.Fatalf("default page request = %q", got)
	}

	ctx := withPromptOverrides(context.Background(), PromptOverrides{
		promptDocumentPageOCRSpec.Key:     "逐字抄写。",
		promptDocumentPageRequestSpec.Key: "{name} 的 {page}/{total}",
	})
	if _, err := runPageVisionOCR(ctx, services, "合同.pdf", 2, 5, []byte{0xff}); err != nil {
		t.Fatal(err)
	}
	if got := captured.Messages[0].Content; got != "逐字抄写。" {
		t.Fatalf("page OCR system prompt = %q", got)
	}
	if got := captured.Messages[1].Content; got != "合同.pdf 的 2/5" {
		t.Fatalf("page request = %q", got)
	}
}
