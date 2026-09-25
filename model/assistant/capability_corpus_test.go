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

type capabilityToolResult struct {
	KnowledgeVersion string                `json:"knowledge_version"`
	Items            []capabilitySearchHit `json:"items"`
	References       []capabilityReference `json:"references"`
}

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
		references := retrieveCapabilityReferences(query, index, nil, defaultCapabilityReferenceLimit, false)
		if len(references) == 0 || references[0].Path != wantPath {
			t.Fatalf("query %q top = %#v, want %s", query, references, wantPath)
		}
	}
}

func TestCapabilityReferenceExcerptStartsAtBestParagraph(t *testing.T) {
	content := strings.Repeat("背景介绍。", 60) + "\n\n" + "编码代理的并发默认是一，同一个工作区不能同时跑两个任务。" + "\n\n" + strings.Repeat("其他说明。", 60)
	excerpt, truncated := capabilityExcerpt(content, capabilityReferenceQueryTerms("编码代理并发"), 80)
	if !truncated || !strings.HasPrefix(excerpt, "…编码代理的并发") {
		t.Fatalf("excerpt = %q truncated=%v", excerpt, truncated)
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
	for _, reference := range result.References {
		if runes := utf8.RuneCountInString(reference.Excerpt); runes > capabilityReferenceExcerptRunes+2 {
			t.Fatalf("brief excerpt has %d runes", runes)
		}
	}
	detailed := runCapabilityTool(t, tool, map[string]any{"query": "编码代理能同时跑几个任务", "detail": true})
	if len(detailed.References) == 0 || detailed.References[0].Truncated {
		t.Fatalf("detail references = %#v", detailed.References)
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
