package llm

import (
	"fmt"
	"strings"
	"testing"
)

func TestApplyContextBudgetPreservesLayeredPriorities(t *testing.T) {
	req := GenerateRequest{
		MaxOutputTokens: 64,
		Messages: []Message{
			{Role: RoleSystem, Content: "系统规则" + strings.Repeat("甲", 90), Priority: MessagePrioritySystem},
			{Role: RoleUser, Content: "长期记忆" + strings.Repeat("乙", 90), Priority: MessagePriorityMemory},
			{Role: RoleUser, Content: "压缩摘要" + strings.Repeat("丙", 90), Priority: MessagePrioritySummary},
			{Role: RoleUser, Content: "最旧历史" + strings.Repeat("丁", 220), Priority: MessagePriorityHistory},
			{Role: RoleUser, Content: "较新历史" + strings.Repeat("戊", 120), Priority: MessagePriorityHistory},
			{Role: RoleUser, Content: "当前问题" + strings.Repeat("己", 40), Priority: MessagePriorityCurrent},
		},
	}
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, ContextWindowTokens: 1024, MaxContextTokens: 1024}
	got := applyContextBudget(req, cfg)
	joined := messageTextForTest(got.Messages)
	for _, want := range []string{"系统规则", "长期记忆", "压缩摘要", "当前问题"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("budgeted messages missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "最旧历史") {
		t.Fatalf("old history should be removed before layered memory: %s", joined)
	}
	inputBudget := int64(1024 - 64 - contextBudgetSafetyReserve)
	if tokens := estimateMessagesTokens(got.Messages); tokens > inputBudget {
		t.Fatalf("estimated tokens = %d, budget = %d", tokens, inputBudget)
	}
}

func TestApplyContextBudgetCannotExceedModelWindow(t *testing.T) {
	req := GenerateRequest{
		MaxOutputTokens: 256,
		Messages: []Message{
			{Role: RoleSystem, Content: strings.Repeat("system ", 1000)},
			{Role: RoleUser, Content: strings.Repeat("context ", 3000)},
		},
	}
	cfg := ProviderConfig{
		Provider:            ProviderOpenAICompatible,
		ContextWindowTokens: 2048,
		MaxContextTokens:    8192,
	}
	got := applyContextBudget(req, cfg)
	inputBudget := int64(2048 - 256 - contextBudgetSafetyReserve)
	if tokens := estimateMessagesTokens(got.Messages); tokens > inputBudget {
		t.Fatalf("estimated tokens = %d, model input budget = %d", tokens, inputBudget)
	}
}

func TestApplyContextBudgetKeepsSystemAndOversizedCurrentTurn(t *testing.T) {
	req := GenerateRequest{
		MaxOutputTokens: 128,
		Messages: []Message{
			{Role: RoleSystem, Content: "系统规则必须保留" + strings.Repeat("甲", 2000)},
			{Role: RoleUser, Content: "旧历史应先丢弃" + strings.Repeat("乙", 2000), Priority: MessagePriorityHistory},
			{Role: RoleUser, Content: "当前问题必须保留" + strings.Repeat("丙", 5000), Priority: MessagePriorityCurrent},
		},
	}
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, ContextWindowTokens: 2048, MaxContextTokens: 2048}
	got := applyContextBudget(req, cfg)
	joined := messageTextForTest(got.Messages)
	for _, want := range []string{"系统规则必须保留", "当前问题必须保留"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("budgeted messages missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "旧历史应先丢弃") {
		t.Fatalf("history should not displace required messages: %s", joined)
	}
}

func TestApplyContextBudgetKeepsPluginEvidenceWholeBeforeSystemDetails(t *testing.T) {
	pluginEvidence := "插件证据开始" + strings.Repeat("乙", 300) + "插件证据结束"
	current := "当前问题" + strings.Repeat("丙", 40)
	req := GenerateRequest{
		MaxOutputTokens: 64,
		Messages: []Message{
			{Role: RoleSystem, Content: "通用系统规则" + strings.Repeat("甲", 1000), Priority: MessagePrioritySystem},
			{Role: RoleUser, Content: pluginEvidence, Priority: MessagePriorityPlugin},
			{Role: RoleUser, Content: current, Priority: MessagePriorityCurrent},
		},
	}
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, ContextWindowTokens: 1024, MaxContextTokens: 1024}
	got := applyContextBudget(req, cfg)
	if len(got.Messages) != 3 {
		t.Fatalf("messages = %#v", got.Messages)
	}
	if got.Messages[1].Content != pluginEvidence {
		t.Fatalf("plugin evidence was trimmed: %q", got.Messages[1].Content)
	}
	if got.Messages[2].Content != current {
		t.Fatalf("current message was trimmed: %q", got.Messages[2].Content)
	}
	if !strings.Contains(got.Messages[0].Content, "上下文已按 token 预算裁剪") {
		t.Fatalf("system details should be trimmed first: %q", got.Messages[0].Content)
	}
	inputBudget := int64(1024 - 64 - contextBudgetSafetyReserve)
	if tokens := estimateMessagesTokens(got.Messages); tokens > inputBudget {
		t.Fatalf("estimated tokens = %d, budget = %d", tokens, inputBudget)
	}
}

func TestApplyContextBudgetReservesVisionTokens(t *testing.T) {
	req := GenerateRequest{
		MaxOutputTokens: 128,
		Messages: []Message{{
			Role:     RoleUser,
			Content:  "看这些图片",
			Priority: MessagePriorityCurrent,
			Parts: []ContentPart{
				{Type: ContentPartText, Text: "看这些图片"},
				{Type: ContentPartImageURL, ImageURL: "https://example.com/1.jpg"},
				{Type: ContentPartImageURL, ImageURL: "https://example.com/2.jpg"},
				{Type: ContentPartImageURL, ImageURL: "https://example.com/3.jpg"},
			},
		}},
	}
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, ContextWindowTokens: 6000, MaxContextTokens: 6000}
	got := applyContextBudget(req, cfg)
	if len(got.Messages) != 1 {
		t.Fatalf("messages = %#v", got.Messages)
	}
	images := 0
	for _, part := range got.Messages[0].Parts {
		if part.Type == ContentPartImageURL {
			images++
		}
	}
	if images != 3 {
		t.Fatalf("images = %d, want all 3 at low detail; message = %#v", images, got.Messages[0])
	}
	for _, part := range got.Messages[0].Parts {
		if part.Type == ContentPartImageURL && part.Detail != "low" {
			t.Fatalf("image detail = %q, want low; message = %#v", part.Detail, got.Messages[0])
		}
	}
}

func TestContextBudgetKeepsCurrentImageDetailWhenOnlyHistoryOverflows(t *testing.T) {
	messages := []Message{
		{Role: RoleUser, Content: strings.Repeat("旧历史", 3000), Priority: MessagePriorityHistory},
		{Role: RoleUser, Content: "看图", Priority: MessagePriorityCurrent, Parts: []ContentPart{
			{Type: ContentPartText, Text: "看图"},
			{Type: ContentPartImageURL, ImageURL: "https://example.com/current.jpg", Detail: "auto"},
		}},
	}
	got := fitMessagesToTokenBudget(messages, 5000)
	if len(got) != 1 || len(got[0].Parts) != 2 {
		t.Fatalf("budgeted messages = %#v", got)
	}
	if got[0].Parts[1].Detail != "auto" {
		t.Fatalf("current image detail = %q, want auto", got[0].Parts[1].Detail)
	}
}

func TestContextBudgetDropsHistoricalImageLabelWithImage(t *testing.T) {
	messages := []Message{
		{Role: RoleUser, Content: "历史图片", Priority: MessagePriorityMemory, Parts: []ContentPart{
			{Type: ContentPartText, Text: "此消息包含 1 张真实图片"},
			{Type: ContentPartImageURL, ImageURL: "https://example.com/history.jpg", Detail: "low"},
		}},
		{Role: RoleUser, Content: strings.Repeat("当前问题", 200), Priority: MessagePriorityCurrent},
	}
	got := fitMessagesToTokenBudget(messages, 900)
	joined := messageTextForTest(got)
	if strings.Contains(joined, "此消息包含 1 张真实图片") {
		t.Fatalf("historical image label survived without its image: %#v", got)
	}
}

func TestContextBudgetKeepsHistoricalImageMessageAtomic(t *testing.T) {
	messages := []Message{
		{Role: RoleUser, Content: "两张历史图片", Priority: MessagePriorityPlugin, Parts: []ContentPart{
			{Type: ContentPartText, Text: "此消息包含 2 张真实图片"},
			{Type: ContentPartImageURL, ImageURL: "https://example.com/1.jpg", Detail: "low"},
			{Type: ContentPartImageURL, ImageURL: "https://example.com/2.jpg", Detail: "low"},
		}},
		{Role: RoleUser, Content: strings.Repeat("当前问题", 200), Priority: MessagePriorityCurrent},
	}
	got := fitMessagesToTokenBudget(messages, 1600)
	for _, message := range got {
		images := 0
		for _, part := range message.Parts {
			if part.Type == ContentPartImageURL {
				images++
			}
		}
		if images == 1 {
			t.Fatalf("historical multi-image message was partially retained: %#v", got)
		}
	}
}

func TestContextBudgetKeepsEightToolImagesTogetherAtDefaultWindow(t *testing.T) {
	parts := []ContentPart{{Type: ContentPartText, Text: "历史原图工具返回了 8 张图片"}}
	for index := 0; index < 8; index++ {
		parts = append(parts, ContentPart{Type: ContentPartImageURL, ImageURL: fmt.Sprintf("https://example.com/%d.jpg", index), Detail: "auto"})
	}
	messages := []Message{
		{Role: RoleSystem, Content: strings.Repeat("系统说明", 500), Priority: MessagePrioritySystem},
		{Role: RoleUser, Content: "历史原图工具返回了 8 张图片", Priority: MessagePriorityPlugin, Parts: parts},
		{Role: RoleUser, Content: "比较这些图片", Priority: MessagePriorityCurrent},
	}
	got := fitMessagesToTokenBudget(messages, DefaultMaxContextTokens-DefaultMaxOutputTokens-contextBudgetSafetyReserve)
	images := 0
	for _, message := range got {
		for _, part := range message.Parts {
			if part.Type == ContentPartImageURL {
				images++
				if part.Detail != "low" {
					t.Fatalf("tool image detail = %q, want low after fitting", part.Detail)
				}
			}
		}
	}
	if images != 8 {
		t.Fatalf("budget retained %d of 8 tool images: %#v", images, got)
	}
}

func messageTextForTest(messages []Message) string {
	var parts []string
	for _, message := range messages {
		parts = append(parts, message.Content)
		for _, part := range message.Parts {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "\n")
}
