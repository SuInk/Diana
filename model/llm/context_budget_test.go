// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

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

func TestContextBudgetRetainsRecentHistoryAndMemoryUnderOversizedAgentPrompts(t *testing.T) {
	messages := []Message{
		{Role: RoleSystem, Content: "Agent 协议\n" + strings.Repeat("工具定义与执行规则。", 1800), Priority: MessagePrioritySystem},
		{Role: RoleSystem, Content: "Diana 系统提示\n" + strings.Repeat("人格与群聊规则。", 1400), Priority: MessagePrioritySystem},
		{Role: RoleUser, Content: "【当前发言者长期记忆】\n喜欢测试上下文保留。" + strings.Repeat("长期事实。", 500), Priority: MessagePriorityMemory},
	}
	for index := 1; index <= 19; index++ {
		messages = append(messages, Message{
			Role:     RoleUser,
			Content:  fmt.Sprintf("【历史参考消息 %02d】用户在讨论连续话题。%s", index, strings.Repeat("上下文。", 45)),
			Priority: MessagePriorityHistory,
		})
	}
	messages = append(messages,
		Message{Role: RoleSystem, Content: "当前运行时钟：2026-08-14 00:30:00", Priority: MessagePrioritySystem},
		Message{Role: RoleUser, Content: "【当前需要回复的消息】继续刚才的话题。", Priority: MessagePriorityCurrent},
	)

	req := GenerateRequest{Messages: messages, MaxOutputTokens: 1024}
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, ContextWindowTokens: 16384, MaxContextTokens: 16384}
	got := applyContextBudget(req, cfg)
	budget := int64(16384 - 1024 - contextBudgetSafetyReserve)
	joined := messageTextForTest(got.Messages)
	if !strings.Contains(joined, "【当前发言者长期记忆】") {
		t.Fatalf("structured memory was completely discarded: %s", joined)
	}
	historyCount := 0
	for _, message := range got.Messages {
		if strings.Contains(message.Content, "【历史参考消息") {
			historyCount++
		}
	}
	if historyCount < minimumRecentHistoryCount {
		t.Fatalf("retained history = %d, want at least %d; messages = %#v", historyCount, minimumRecentHistoryCount, got.Messages)
	}
	if !strings.Contains(joined, "【历史参考消息 19】") {
		t.Fatalf("most recent history was not retained: %s", joined)
	}
	if strings.Contains(joined, "【历史参考消息 01】") {
		t.Fatalf("oldest history displaced newer context: %s", joined)
	}
	for _, want := range []string{"Agent 协议", "Diana 系统提示", "当前运行时钟", "【当前需要回复的消息】"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("budgeted messages missing %q: %s", want, joined)
		}
	}
	if tokens := estimateMessagesTokens(got.Messages); tokens > budget {
		t.Fatalf("estimated tokens = %d, budget = %d", tokens, budget)
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
	// 默认窗口下 8 张 auto 图（各 4096）总计约 32K，128K 的预算放得下，
	// 不需要牺牲清晰度。
	got := fitMessagesToTokenBudget(messages, DefaultMaxContextTokens-DefaultMaxOutputTokens-contextBudgetSafetyReserve)
	images := 0
	for _, message := range got {
		for _, part := range message.Parts {
			if part.Type == ContentPartImageURL {
				images++
				if part.Detail != "auto" {
					t.Fatalf("tool image detail = %q, want auto to be preserved at the default window", part.Detail)
				}
			}
		}
	}
	if images != 8 {
		t.Fatalf("budget retained %d of 8 tool images: %#v", images, got)
	}

	// 窗口真的紧张时（小窗口本地模型）仍然整批降级保留，而不是丢图。
	tight := fitMessagesToTokenBudget(messages, 15232)
	images = 0
	for _, message := range tight {
		for _, part := range message.Parts {
			if part.Type == ContentPartImageURL {
				images++
				if part.Detail != "low" {
					t.Fatalf("tight budget must lower detail, got %q", part.Detail)
				}
			}
		}
	}
	if images != 8 {
		t.Fatalf("tight budget retained %d of 8 tool images: %#v", images, tight)
	}
}

func TestContextBudgetKeepsHistoryTurnAtomic(t *testing.T) {
	messages := []Message{
		{Role: RoleSystem, Content: strings.Repeat("系统", 80), Priority: MessagePrioritySystem},
		{Role: RoleUser, Content: strings.Repeat("过大的旧问题", 300), Priority: MessagePriorityHistory, ContextGroup: "old"},
		{Role: RoleAssistant, Content: strings.Repeat("过大的旧回答", 300), Priority: MessagePriorityHistory, ContextGroup: "old"},
		{Role: RoleUser, Content: "Melvor Idle 的深渊在哪", Priority: MessagePriorityHistory, ContextGroup: "recent"},
		{Role: RoleAssistant, Content: "先查看地下城列表", Priority: MessagePriorityHistory, ContextGroup: "recent"},
		{Role: RoleUser, Content: "具体入口在哪", Priority: MessagePriorityCurrent},
	}
	got := fitMessagesToTokenBudget(messages, 700)
	joined := messageTextForTest(got)
	for _, want := range []string{"Melvor Idle 的深渊在哪", "先查看地下城列表", "具体入口在哪"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("atomic recent turn missing %q: %#v", want, got)
		}
	}
	if strings.Contains(joined, "过大的旧问题") || strings.Contains(joined, "过大的旧回答") {
		t.Fatalf("oversized old turn leaked partially: %#v", got)
	}
}

func TestContextBudgetPrefersRecentHistoryOverMemoryAndSummary(t *testing.T) {
	messages := []Message{
		{Role: RoleSystem, Content: strings.Repeat("系统", 60), Priority: MessagePrioritySystem},
		{Role: RoleUser, Content: strings.Repeat("长期记忆", 100), Priority: MessagePriorityMemory},
		{Role: RoleUser, Content: strings.Repeat("旧摘要", 100), Priority: MessagePrioritySummary},
		{Role: RoleUser, Content: "刚才用户说密码是 2468", Priority: MessagePriorityRecentHistory, ContextGroup: "recent"},
		{Role: RoleAssistant, Content: "我记住了密码 2468", Priority: MessagePriorityRecentHistory, ContextGroup: "recent"},
		{Role: RoleUser, Content: "我刚说的密码是什么", Priority: MessagePriorityCurrent},
	}
	got := fitMessagesToTokenBudget(messages, 360)
	joined := messageTextForTest(got)
	for _, want := range []string{"刚才用户说密码是 2468", "我记住了密码 2468", "我刚说的密码是什么"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("recent context lost %q: %#v", want, got)
		}
	}
	if strings.Count(joined, "长期记忆") >= 100 || strings.Contains(joined, "旧摘要") {
		t.Fatalf("lower-priority context was not trimmed after recent history: %#v", got)
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
