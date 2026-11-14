// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
	"unicode/utf8"

	"github.com/SuInk/diana/model/agent"
)

func runCapabilityTool(t *testing.T, tool agent.Tool, input map[string]any) capabilityToolResult {
	t.Helper()
	raw, err := tool.Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	var result capabilityToolResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return result
}

func TestCapabilityReferencesIndexEmbeddedDocsAndPrompts(t *testing.T) {
	index := staticCapabilityReferenceIndex()
	sources := map[string]int{}
	for _, document := range index.documents {
		sources[document.Source]++
		if document.Source == capabilityReferenceSourceDoc && document.Path == "docs/README.md" {
			t.Fatalf("English site README should stay out of the index: %#v", document)
		}
		if runes := utf8.RuneCountInString(document.Content); document.Source == capabilityReferenceSourceDoc && runes > capabilityReferenceSectionRunes {
			t.Fatalf("section %s has %d runes, want <= %d", document.ID, runes, capabilityReferenceSectionRunes)
		}
	}
	if sources[capabilityReferenceSourceDoc] < 100 {
		t.Fatalf("doc sections = %d, embedded docs missing?", sources[capabilityReferenceSourceDoc])
	}
	if sources[capabilityReferenceSourcePrompt] != len(PromptSpecs()) {
		t.Fatalf("prompt references = %d, registry = %d", sources[capabilityReferenceSourcePrompt], len(PromptSpecs()))
	}
}

func TestCapabilityReferencesRankDesignDocsForMechanismQuestions(t *testing.T) {
	index := staticCapabilityReferenceIndex()
	for query, wantPath := range map[string]string{
		"编码代理能同时跑几个任务":          "docs/coding-agents.md",
		"推荐 GitHub 仓库时看哪些指标":    "docs/repository-discovery.md",
		"常驻工具名单是怎么回事，为什么改了会清缓存": "docs/deferred-tools.md",
		// 「是怎么」在别的文档标题里也有（「实时画面是怎么来的」），疑问词不能主导排序。
		"你的记忆是怎么召回的": "docs/memory-recall.md",
	} {
		references := retrieveCapabilityReferences(query, index, nil)
		if len(references) == 0 || references[0].Path != wantPath {
			t.Fatalf("query %q top = %#v, want %s", query, references, wantPath)
		}
	}
}

func TestCapabilityDocSectionsSplitMarkdownAndHTML(t *testing.T) {
	files := fstest.MapFS{
		"guide.md": {Data: []byte("# 指南\n\n开头。\n\n## 第一节\n\n```sh\n# 这不是标题\n```\n\n正文一。\n\n### 小节\n\n正文二。\n")},
		"page.html": {Data: []byte(`<html><head><title>站点 - 页面</title><style>.x{}</style></head><body><nav>导航</nav><main>` +
			`<section><div><p class="eyebrow">标签</p><h2>流程</h2></div><ol><li><strong>接收</strong><p>归一化 &amp; 缓存。</p></li></ol></section>` +
			`<section><div><p class="eyebrow">下一节</p><h2>存储</h2></div><p>SQLite 主库。</p></section></main></body></html>`)},
	}
	titles := map[string]string{}
	for _, document := range loadCapabilityDocSections(files) {
		titles[document.Title] = document.Content
	}
	want := map[string]string{
		"指南":           "开头。",
		"指南 › 第一节":     "```sh\n# 这不是标题\n```\n\n正文一。",
		"指南 › 小节":      "正文二。",
		"站点 - 页面 › 流程": "接收：归一化 & 缓存。",
		"站点 - 页面 › 存储": "SQLite 主库。",
	}
	for title, content := range want {
		if titles[title] != content {
			t.Fatalf("section %q = %q, want %q (all: %#v)", title, titles[title], content, titles)
		}
	}
	if len(titles) != len(want) {
		t.Fatalf("sections = %#v", titles)
	}
}

func TestCapabilityChunksNeverExceedLimit(t *testing.T) {
	text := strings.Repeat("短段。\n\n", 40) + strings.Repeat("长", 50)
	for _, chunk := range splitCapabilityChunks(text, 30) {
		if utf8.RuneCountInString(chunk) > 30 {
			t.Fatalf("chunk has %d runes: %q", utf8.RuneCountInString(chunk), chunk)
		}
	}
}

func TestCapabilityToolReturnsReferencesAlongsideItems(t *testing.T) {
	tool := NewCapabilityKnowledgePlugin().AgentTools()[0]
	result := runCapabilityTool(t, tool, map[string]any{"query": "编码代理能同时跑几个任务"})
	if result.KnowledgeVersion == "" {
		t.Fatal("knowledge_version missing")
	}
	if len(result.References) == 0 || len(result.References) > defaultCapabilityReferenceLimit || result.References[0].Path != "docs/coding-agents.md" {
		t.Fatalf("references = %#v", result.References)
	}
	// 参考资料给整段原文，不再截成摘录。
	index := staticCapabilityReferenceIndex()
	for _, reference := range result.References {
		for _, document := range index.documents {
			if document.ID == reference.ID && document.Content != reference.Content {
				t.Fatalf("reference %s content was cut: %q", reference.ID, reference.Content)
			}
		}
	}
	if result.ReferencesPage == nil || result.ReferencesPage.Total < len(result.References) || result.ItemsPage == nil {
		t.Fatalf("pages = %#v %#v", result.ItemsPage, result.ReferencesPage)
	}
}

func TestCapabilityToolPagesItemsAndReferencesWithOffsets(t *testing.T) {
	tool := NewCapabilityKnowledgePlugin().AgentTools()[0]
	query := "编码代理能同时跑几个任务"
	first := runCapabilityTool(t, tool, map[string]any{"query": query, "limit": 1, "reference_limit": 1})
	if first.ItemsPage.Total < 2 || !first.ItemsPage.HasMore || first.ItemsPage.NextOffset != 1 || len(first.Items) != 1 {
		t.Fatalf("items page = %#v items=%d", first.ItemsPage, len(first.Items))
	}
	if first.ReferencesPage.Total < 2 || !first.ReferencesPage.HasMore || first.ReferencesPage.NextOffset != 1 ||
		!strings.Contains(first.ReferencesPage.PageNote, "reference_offset=1") {
		t.Fatalf("references page = %#v", first.ReferencesPage)
	}
	rest := runCapabilityTool(t, tool, map[string]any{"query": query, "offset": 1, "limit": 1000, "reference_offset": 1, "reference_limit": 1000})
	if len(rest.Items) != first.ItemsPage.Total-1 || rest.ItemsPage.HasMore || rest.Items[0].ID == first.Items[0].ID {
		t.Fatalf("rest items = %d page=%#v", len(rest.Items), rest.ItemsPage)
	}
	// 参考资料是整段原文，一次可能装不下全部；顺着 next_offset 读完要拿到全部剩余条目。
	seen := map[string]bool{first.References[0].ID: true}
	page := rest
	for {
		for _, reference := range page.References {
			if seen[reference.ID] {
				t.Fatalf("reference %s returned twice", reference.ID)
			}
			seen[reference.ID] = true
		}
		if !page.ReferencesPage.HasMore {
			break
		}
		page = runCapabilityTool(t, tool, map[string]any{"query": query, "limit": 1, "reference_offset": page.ReferencesPage.NextOffset, "reference_limit": 1000})
	}
	if len(seen) != first.ReferencesPage.Total {
		t.Fatalf("read %d references, total %d", len(seen), first.ReferencesPage.Total)
	}
}

func TestCapabilityToolSplitsOversizedWindowIntoPages(t *testing.T) {
	tool := NewCapabilityKnowledgePlugin().AgentTools()[0]
	ctx := agent.WithToolOutputBudget(context.Background(), 3000)
	raw, err := tool.Run(ctx, map[string]any{"query": "编码代理能同时跑几个任务", "limit": 1, "reference_limit": 1000})
	if err != nil {
		t.Fatal(err)
	}
	var result capabilityToolResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	page := result.ReferencesPage
	if page.Total <= len(result.References) || !page.HasMore || page.NextOffset != len(result.References) || page.RemainingCount != page.Total-len(result.References) {
		t.Fatalf("references page = %#v returned=%d", page, len(result.References))
	}
}

func TestCapabilityToolSearchesToolsAttachedForThisTurn(t *testing.T) {
	plugin := NewCapabilityKnowledgePlugin()
	tools := []agent.Tool{capabilityToolForConfig(plugin.AgentTools()[0], BotConfig{}.WithDefaults())}
	registry := agent.NewToolRegistry(append(tools, &dianaVersionTool{})...)
	attachCapabilityRegistry(tools, registry)
	result := runCapabilityTool(t, tools[0], map[string]any{"query": "有没有新版本、能不能自更新"})
	for _, reference := range result.References {
		if reference.ID == "tool:"+dianaVersionToolName && reference.Source == capabilityReferenceSourceTool {
			return
		}
	}
	t.Fatalf("attached tool missing from references: %#v", result.References)
}
