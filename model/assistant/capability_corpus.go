// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"html"
	"io/fs"
	"math"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/SuInk/diana/docs"
	"github.com/SuInk/diana/model/agent"
)

// 能力知识库的第二条检索道：回答「这个功能怎么运作、为什么这样」。
//
// 第一条道（items）只装手写的能力条目和插件清单，回答「会不会」；这里装的是随
// 本版本编译进来的设计文档、内置提示词登记表，以及本轮真正挂上的工具和 Skill 说明。
// 两条道分开排序：文档动辄上千字，和一句话的能力条目混在一起打分，只会把能力
// 条目挤出前几名。

const (
	capabilityReferenceSectionRunes = 1500
	capabilityReferenceExcerptRunes = 420
	defaultCapabilityReferenceLimit = 3
	detailCapabilityReferenceLimit  = 6
	// 分数低于第一名这个比例的参考直接不给：命中的只是零散单字，带回去只占上下文。
	capabilityReferenceRelativeFloor = 0.35
	capabilityToolDescriptionRunes   = 1200
)

const (
	capabilityReferenceSourceDoc    = "doc"
	capabilityReferenceSourcePrompt = "prompt"
	capabilityReferenceSourceTool   = "tool"
	capabilityReferenceSourceSkill  = "skill"
)

type capabilityReferenceDocument struct {
	ID      string
	Title   string
	Source  string
	Path    string
	Content string

	terms  map[string]int
	length int
}

type capabilityReference struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Source    string  `json:"source"`
	Path      string  `json:"path,omitempty"`
	Excerpt   string  `json:"excerpt"`
	Truncated bool    `json:"truncated,omitempty"`
	Score     float64 `json:"score"`
}

type capabilityReferenceIndex struct {
	documents []*capabilityReferenceDocument
	df        map[string]int
	totalLen  int
}

var (
	staticCapabilityReferencesOnce sync.Once
	staticCapabilityReferences     *capabilityReferenceIndex
)

func staticCapabilityReferenceIndex() *capabilityReferenceIndex {
	staticCapabilityReferencesOnce.Do(func() {
		documents := loadCapabilityDocSections(docs.FS)
		documents = append(documents, capabilityPromptReferences()...)
		staticCapabilityReferences = newCapabilityReferenceIndex(documents)
	})
	return staticCapabilityReferences
}

func newCapabilityReferenceIndex(documents []*capabilityReferenceDocument) *capabilityReferenceIndex {
	index := &capabilityReferenceIndex{df: map[string]int{}}
	for _, document := range documents {
		prepareCapabilityReference(document)
		index.documents = append(index.documents, document)
		index.totalLen += document.length
		for term := range document.terms {
			index.df[term]++
		}
	}
	return index
}

func isCapabilityHan(value rune) bool { return unicode.Is(unicode.Han, value) }

func prepareCapabilityReference(document *capabilityReferenceDocument) {
	if document.terms != nil {
		return
	}
	terms := map[string]int{}
	// 标题按三倍计：章节标题就是这一段在讲什么。
	for term, count := range capabilityTermCounts(document.Title) {
		terms[term] += count * 3
	}
	for term, count := range capabilityTermCounts(document.Content) {
		terms[term] += count
	}
	length := 0
	for _, count := range terms {
		length += count
	}
	document.terms = terms
	document.length = length
}

// capabilityTermCounts 和 capabilityTerms 用同一套切词：ASCII 词、汉字单字、
// 二字和三字片段。这里记的是出现次数，BM25 要用。
func capabilityTermCounts(text string) map[string]int {
	text = strings.ToLower(text)
	counts := map[string]int{}
	for _, token := range capabilityASCIIToken.FindAllString(text, -1) {
		if len(token) >= 2 {
			counts[token]++
		}
	}
	runes := []rune(text)
	for index, current := range runes {
		if !isCapabilityHan(current) {
			continue
		}
		counts[string(current)]++
		if index+1 < len(runes) && isCapabilityHan(runes[index+1]) {
			counts[string(runes[index:index+2])]++
			if index+2 < len(runes) && isCapabilityHan(runes[index+2]) {
				counts[string(runes[index:index+3])]++
			}
		}
	}
	return counts
}

// 疑问句的壳。文档里「实时画面是怎么来的」这种标题也用这些词，不去掉的话
// 「记忆是怎么召回的」会先命中浏览器文档。只收多字短语和几乎不构词的单字：
// 「会」「能」这类会拆掉「会话」「能力」，不收。
var capabilityQueryFillers = []string{
	"可不可以", "能不能", "会不会", "是不是", "有没有", "要不要", "为什么", "怎么样", "是怎么",
	"怎么", "如何", "什么", "哪些", "哪个", "哪里", "多久", "多少", "是否", "请问", "一下",
	"回事", "你们", "你的", "我的", "告诉我", "你", "吗", "呢", "吧", "啊", "呀", "的", "了", "是",
}

func capabilityReferenceQueryTerms(query string) map[string]float64 {
	stripped := strings.ToLower(query)
	for _, filler := range capabilityQueryFillers {
		stripped = strings.ReplaceAll(stripped, filler, " ")
	}
	if terms := capabilityTerms(stripped); len(terms) > 0 {
		return terms
	}
	// 整句都是虚词（「你是什么」）时退回原句，总比一条都不给强。
	return capabilityTerms(query)
}

// retrieveCapabilityReferences 在静态语料和本轮工具说明上跑 BM25。
func retrieveCapabilityReferences(query string, static *capabilityReferenceIndex, dynamic []*capabilityReferenceDocument, limit int, detail bool) []capabilityReference {
	queryTerms := capabilityReferenceQueryTerms(query)
	if len(queryTerms) == 0 || limit <= 0 {
		return nil
	}
	documents := make([]*capabilityReferenceDocument, 0, len(static.documents)+len(dynamic))
	documents = append(documents, static.documents...)
	df := static.df
	totalLen := static.totalLen
	if len(dynamic) > 0 {
		df = make(map[string]int, len(queryTerms))
		for term := range queryTerms {
			df[term] = static.df[term]
		}
		for _, document := range dynamic {
			prepareCapabilityReference(document)
			documents = append(documents, document)
			totalLen += document.length
			for term := range queryTerms {
				if document.terms[term] > 0 {
					df[term]++
				}
			}
		}
	}
	if len(documents) == 0 {
		return nil
	}
	const k1, b = 1.2, 0.75
	count := float64(len(documents))
	averageLen := float64(totalLen) / count
	type scored struct {
		document *capabilityReferenceDocument
		score    float64
	}
	var hits []scored
	for _, document := range documents {
		score := 0.0
		for term, queryWeight := range queryTerms {
			tf := float64(document.terms[term])
			if tf == 0 {
				continue
			}
			docFreq := float64(df[term])
			idf := math.Log(1 + (count-docFreq+0.5)/(docFreq+0.5))
			norm := k1 * (1 - b + b*float64(document.length)/averageLen)
			score += queryWeight * idf * tf * (k1 + 1) / (tf + norm)
		}
		if score > 0 {
			hits = append(hits, scored{document: document, score: score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].document.ID < hits[j].document.ID
	})
	references := make([]capabilityReference, 0, limit)
	for _, hit := range hits {
		if len(references) >= limit || hit.score < hits[0].score*capabilityReferenceRelativeFloor {
			break
		}
		excerpt, truncated := hit.document.Content, false
		if !detail {
			excerpt, truncated = capabilityExcerpt(hit.document.Content, queryTerms, capabilityReferenceExcerptRunes)
		}
		references = append(references, capabilityReference{
			ID:        hit.document.ID,
			Title:     hit.document.Title,
			Source:    hit.document.Source,
			Path:      hit.document.Path,
			Excerpt:   excerpt,
			Truncated: truncated,
			Score:     math.Round(hit.score*100) / 100,
		})
	}
	return references
}

// capabilityExcerpt 从命中最密的段落开始截一段，而不是永远截开头：
// 章节开头常是背景，真正回答问题的那句可能在第三段。
func capabilityExcerpt(content string, queryTerms map[string]float64, maxRunes int) (string, bool) {
	if utf8.RuneCountInString(content) <= maxRunes {
		return content, false
	}
	paragraphs := strings.Split(content, "\n\n")
	best, bestScore := 0, -1.0
	for index, paragraph := range paragraphs {
		score := 0.0
		for term, count := range capabilityTermCounts(paragraph) {
			score += queryTerms[term] * float64(count)
		}
		if score > bestScore {
			best, bestScore = index, score
		}
	}
	excerpt := strings.Join(paragraphs[best:], "\n\n")
	prefix := ""
	if best > 0 {
		prefix = "…"
	}
	runes := []rune(excerpt)
	if len(runes) <= maxRunes {
		return prefix + excerpt, best > 0
	}
	return prefix + strings.TrimSpace(string(runes[:maxRunes])) + "…", true
}

// ---- 设计文档 ----

var (
	capabilityMarkdownHeading = regexp.MustCompile(`^(#{1,3})\s+(.+?)\s*#*\s*$`)
	capabilityHTMLTitle       = regexp.MustCompile(`(?is)<title>(.*?)</title>`)
	capabilityHTMLDrop        = regexp.MustCompile(`(?is)<(script|style|nav|header|footer)\b.*?</(script|style|nav|header|footer)>`)
	// 标题上方的小标签（eyebrow）属于下一节，留着会挂到上一节正文末尾。
	capabilityHTMLEyebrow = regexp.MustCompile(`(?is)<p[^>]*class="[^"]*eyebrow[^"]*"[^>]*>.*?</p>`)
	// 列表项里「<strong>小标题</strong>正文」的小标题后面补一个冒号，不然两段粘成一句。
	capabilityHTMLLead     = regexp.MustCompile(`(?i)</(?:strong|b|dt)>(\s*<(?:p|dd)\b)`)
	capabilityHTMLHeading  = regexp.MustCompile(`(?is)<h([1-3])\b[^>]*>(.*?)</h[1-3]>`)
	capabilityHTMLBlockEnd = regexp.MustCompile(`(?i)</(p|li|div|section|tr|pre|table|ul|ol|h4|h5|h6)>|<br\s*/?>`)
	capabilityHTMLTag      = regexp.MustCompile(`(?s)<[^>]+>`)
	capabilityBlankLines   = regexp.MustCompile(`\n[ \t]*\n(?:[ \t]*\n)+`)
)

// 这几份不讲 Diana 的行为：站点说明是英文的发布说明，放进来只会被「文档」这类
// 词误命中。
var capabilityDocExcluded = map[string]bool{"README.md": true}

func loadCapabilityDocSections(files fs.FS) []*capabilityReferenceDocument {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil
	}
	var documents []*capabilityReferenceDocument
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || capabilityDocExcluded[name] {
			continue
		}
		data, err := fs.ReadFile(files, name)
		if err != nil {
			continue
		}
		var sections []capabilityDocSection
		switch path.Ext(name) {
		case ".md":
			sections = splitCapabilityMarkdown(string(data))
		case ".html":
			sections = splitCapabilityHTML(string(data))
		default:
			continue
		}
		documents = append(documents, capabilityDocReferences(name, sections)...)
	}
	return documents
}

type capabilityDocSection struct {
	heading string
	body    string
}

// splitCapabilityMarkdown 以一、二、三级标题切段；代码块里的 # 不算标题。
// 第一个一级标题当作文档标题，挂在每一段标题前面。
func splitCapabilityMarkdown(text string) []capabilityDocSection {
	var sections []capabilityDocSection
	current := capabilityDocSection{}
	var body strings.Builder
	inFence := false
	flush := func() {
		current.body = strings.TrimSpace(body.String())
		if current.body != "" || current.heading != "" {
			sections = append(sections, current)
		}
		body.Reset()
	}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
		}
		if !inFence {
			if match := capabilityMarkdownHeading.FindStringSubmatch(line); match != nil {
				flush()
				current = capabilityDocSection{heading: strings.TrimSpace(match[2])}
				if len(match[1]) == 1 {
					current.heading = "#" + current.heading
				}
				continue
			}
		}
		body.WriteString(line)
		body.WriteByte('\n')
	}
	flush()
	return sections
}

func splitCapabilityHTML(source string) []capabilityDocSection {
	title := ""
	if match := capabilityHTMLTitle.FindStringSubmatch(source); match != nil {
		title = capabilityHTMLText(match[1])
	}
	source = capabilityHTMLDrop.ReplaceAllString(source, "")
	source = capabilityHTMLEyebrow.ReplaceAllString(source, "")
	if start := strings.Index(strings.ToLower(source), "<main"); start >= 0 {
		source = source[start:]
	}
	var sections []capabilityDocSection
	locations := capabilityHTMLHeading.FindAllStringSubmatchIndex(source, -1)
	for index, location := range locations {
		heading := capabilityHTMLText(source[location[4]:location[5]])
		end := len(source)
		if index+1 < len(locations) {
			end = locations[index+1][0]
		}
		body := capabilityHTMLText(source[location[1]:end])
		if source[location[2]:location[3]] == "1" && title == "" {
			title = heading
		}
		sections = append(sections, capabilityDocSection{heading: heading, body: body})
	}
	if title != "" {
		sections = append([]capabilityDocSection{{heading: "#" + title}}, sections...)
	}
	return sections
}

func capabilityHTMLText(fragment string) string {
	fragment = capabilityHTMLLead.ReplaceAllString(fragment, "：${1}")
	fragment = capabilityHTMLBlockEnd.ReplaceAllString(fragment, "\n\n")
	fragment = capabilityHTMLTag.ReplaceAllString(fragment, "")
	fragment = html.UnescapeString(fragment)
	lines := strings.Split(fragment, "\n")
	for index, line := range lines {
		lines[index] = strings.Join(strings.Fields(line), " ")
	}
	fragment = capabilityBlankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	return strings.TrimSpace(fragment)
}

func capabilityDocReferences(name string, sections []capabilityDocSection) []*capabilityReferenceDocument {
	docTitle := strings.TrimSuffix(name, path.Ext(name))
	var documents []*capabilityReferenceDocument
	sequence := 0
	for _, section := range sections {
		heading := section.heading
		if strings.HasPrefix(heading, "#") {
			docTitle = strings.TrimPrefix(heading, "#")
			heading = ""
		}
		if section.body == "" {
			continue
		}
		title := docTitle
		if heading != "" {
			title += " › " + heading
		}
		for _, chunk := range splitCapabilityChunks(section.body, capabilityReferenceSectionRunes) {
			sequence++
			documents = append(documents, &capabilityReferenceDocument{
				ID:      "doc:" + name + "#" + itoa(sequence),
				Title:   title,
				Source:  capabilityReferenceSourceDoc,
				Path:    "docs/" + name,
				Content: chunk,
			})
		}
	}
	return documents
}

// splitCapabilityChunks 按段落把过长的章节切成几块，单段超长时硬切。
func splitCapabilityChunks(text string, maxRunes int) []string {
	if utf8.RuneCountInString(text) <= maxRunes {
		return []string{text}
	}
	var chunks []string
	var current strings.Builder
	currentRunes := 0
	flush := func() {
		if chunk := strings.TrimSpace(current.String()); chunk != "" {
			chunks = append(chunks, chunk)
		}
		current.Reset()
		currentRunes = 0
	}
	for _, paragraph := range strings.Split(text, "\n\n") {
		runes := []rune(paragraph)
		for len(runes) > maxRunes {
			flush()
			chunks = append(chunks, strings.TrimSpace(string(runes[:maxRunes])))
			runes = runes[maxRunes:]
		}
		if currentRunes > 0 && currentRunes+len(runes)+2 > maxRunes {
			flush()
		}
		if currentRunes > 0 {
			current.WriteString("\n\n")
			currentRunes += 2
		}
		current.WriteString(string(runes))
		currentRunes += len(runes)
	}
	flush()
	return chunks
}

// ---- 内置提示词 ----

const capabilityPromptDefaultRunes = 600

// capabilityPromptReferences 收的是登记表里的默认原文。人设可以逐段改写，
// 所以这里只说「默认怎么写」，不冒充这台机器人当前实际发出去的那一份。
func capabilityPromptReferences() []*capabilityReferenceDocument {
	specs := PromptSpecs()
	labels := make(map[PromptGroup]string, len(promptGroupOrder))
	for _, group := range promptGroupOrder {
		labels[group.ID] = group.Label
	}
	documents := make([]*capabilityReferenceDocument, 0, len(specs))
	for _, spec := range specs {
		content := "分组：" + labels[spec.Group] + "\n用途：" + strings.TrimSpace(spec.Usage) +
			"\n默认原文（人设可覆盖）：" + truncateRunes(strings.TrimSpace(spec.Default), capabilityPromptDefaultRunes)
		documents = append(documents, &capabilityReferenceDocument{
			ID:      "prompt:" + spec.Key,
			Title:   "内置提示词 › " + spec.Title,
			Source:  capabilityReferenceSourcePrompt,
			Content: content,
		})
	}
	return documents
}

// ---- 本轮工具与 Skill ----

// capabilityRegistryReferences 取的是本轮注册表里真正挂上的工具：权限、平台和
// 插件开关已经筛过，模型问到的就是它这一轮能调的那一份说明。
func capabilityRegistryReferences(registry *agent.ToolRegistry) []*capabilityReferenceDocument {
	if registry == nil {
		return nil
	}
	var documents []*capabilityReferenceDocument
	for _, name := range registry.Names() {
		tool, ok := registry.Get(name)
		if !ok {
			continue
		}
		definition := agent.ToolDefinitionFor(tool)
		content := truncateRunes(strings.TrimSpace(definition.Description), capabilityToolDescriptionRunes)
		if params := capabilityToolParameters(definition.Parameters); params != "" {
			content += "\n参数：" + params
		}
		documents = append(documents, &capabilityReferenceDocument{
			ID:      "tool:" + name,
			Title:   "工具 " + name,
			Source:  capabilityReferenceSourceTool,
			Content: content,
		})
	}
	// 被安全模式这类配置关掉的工具不在 Names 里，单独收一条：主人问「你能不能跑命令」
	// 时检索得到它，答得出「安全模式关了」，而不是「没有这个能力」。
	if disabled := registry.DisabledSummary(); disabled != "" {
		documents = append(documents, &capabilityReferenceDocument{
			ID:      "runtime:disabled-tools",
			Title:   "本轮被关掉的工具（安全模式）运行命令 编码 浏览器 MCP 写文件 改配置",
			Source:  capabilityReferenceSourceTool,
			Content: disabled,
		})
	}
	for _, skill := range registry.Skills() {
		description := strings.TrimSpace(skill.Description)
		if description == "" {
			description = strings.TrimSpace(skill.ShortDescription)
		}
		documents = append(documents, &capabilityReferenceDocument{
			ID:      "skill:" + skill.Name,
			Title:   "Skill " + skill.Name,
			Source:  capabilityReferenceSourceSkill,
			Content: truncateRunes(description, capabilityToolDescriptionRunes),
		})
	}
	return documents
}

func capabilityToolParameters(schema map[string]any) string {
	properties, _ := schema["properties"].(map[string]any)
	if len(properties) == 0 {
		return ""
	}
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		property, _ := properties[name].(map[string]any)
		description, _ := property["description"].(string)
		description = truncateRunes(strings.Join(strings.Fields(description), " "), 80)
		if description == "" {
			parts = append(parts, name)
			continue
		}
		parts = append(parts, name+"（"+description+"）")
	}
	return strings.Join(parts, "；")
}
