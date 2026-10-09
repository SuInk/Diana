// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// 线上 10-07：被问「gemini-image-2.1 是今天刚发布的？」，模型连搜 5 次、一页没读，第 3 次
// 结果里就有 ai.google.dev 的「Gemini Nano Banana 2.1」官方页；它嫌名字对不上，换词再搜，
// 最后回「Google 官方根本没有这个模型」。每次搜索后的通用指引都写着「优先读取官网」，
// 那段话每步一模一样，模型看惯了就不看。
//
// 这里改成按本轮实际进度说话：搜了哪些词、读过哪些页，连搜却没读时直接点出候选链接；
// 只展示检索进度和候选，不按页面数量或结论措辞拦截收尾。
const researchCandidateLimit = 5

// researchLowValueHosts 是社交平台和视频站：作为线索可以，作为一手来源不该排在前面。
var researchLowValueHosts = []string{
	"linkedin.com", "facebook.com", "instagram.com", "threads.com", "threads.net",
	"x.com", "twitter.com", "reddit.com", "youtube.com", "tiktok.com", "bilibili.com", "zhihu.com",
}

type researchProgress struct {
	searches   []string
	read       []SourceReference
	candidates []SourceReference
}

func collectResearchProgress(steps []Step) researchProgress {
	var progress researchProgress
	for _, step := range steps {
		if step.Tool != WebSearchToolName || step.Skipped || step.Error != "" {
			continue
		}
		if query := researchStepQuery(step.Input); query != "" {
			progress.searches = append(progress.searches, query)
		}
	}
	var preferred, others []SourceReference
	for _, source := range responseSources("", steps) {
		host := researchSourceHost(source.URL)
		// 已读记录反映实际读取行为，不代表来源已被证实可靠。
		if source.Read {
			progress.read = append(progress.read, source)
			continue
		}
		switch {
		case host == "" || researchLowValueHost(host):
		case researchFirstPartyLooking(host, source.URL):
			preferred = append(preferred, source)
		default:
			others = append(others, source)
		}
	}
	progress.candidates = append(preferred, others...)
	if len(progress.candidates) > researchCandidateLimit {
		progress.candidates = progress.candidates[:researchCandidateLimit]
	}
	return progress
}

// note 是附在检索类工具结果后面的进度；本轮还没搜过时返回空串。
func (p researchProgress) note() string {
	if len(p.searches) == 0 {
		return ""
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "本轮调研进度：已搜索 %d 次（%s）；", len(p.searches), strings.Join(p.searches, "；"))
	if len(p.read) == 0 {
		builder.WriteString("还没读过任何页面。")
	} else {
		urls := make([]string, 0, len(p.read))
		for _, source := range p.read {
			urls = append(urls, source.URL)
		}
		fmt.Fprintf(&builder, "已读页面 %d 个（%s）。", len(urls), strings.Join(urls, "；"))
	}
	if len(p.read) == 0 && len(p.candidates) > 0 {
		fmt.Fprintf(&builder, "\n已经搜了 %d 次还没读页面，再换关键词多半还是同一批摘要。可从下面选择与问题相关的原始资料核对；互不依赖的页面可用 browser_render 的 url + urls 并行读取：\n", len(p.searches))
		builder.WriteString(p.candidateList())
		builder.WriteString("\n名称和用户说法不完全一致的官方页（别名、改名、型号写法不同）也要打开核对，不能因为名字没对上就当作不存在。")
	} else if len(p.candidates) > 0 {
		builder.WriteString("\n若证据仍不足，可按需读取以下候选；已有充分依据即可回答，不必凑页面数量：\n")
		builder.WriteString(p.candidateList())
	}
	return builder.String()
}

func (p researchProgress) candidateList() string {
	lines := make([]string, 0, len(p.candidates))
	for _, source := range p.candidates {
		line := "- " + source.URL
		if title := strings.TrimSpace(source.Title); title != "" {
			line += "（" + researchShort(title, 60) + "）"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func researchStepQuery(input map[string]any) string {
	if query, ok := input["query"].(string); ok && strings.TrimSpace(query) != "" {
		return researchShort(strings.TrimSpace(query), 60)
	}
	var parts []string
	switch values := input["queries"].(type) {
	case []any:
		for _, value := range values {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				parts = append(parts, strings.TrimSpace(text))
			}
		}
	case []string:
		for _, text := range values {
			if strings.TrimSpace(text) != "" {
				parts = append(parts, strings.TrimSpace(text))
			}
		}
	}
	return researchShort(strings.Join(parts, " | "), 60)
}

func researchSourceHost(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
}

func researchLowValueHost(host string) bool {
	for _, blocked := range researchLowValueHosts {
		if host == blocked || strings.HasSuffix(host, "."+blocked) {
			return true
		}
	}
	return false
}

// researchFirstPartyLooking 只是排序用的粗判：文档站、开发者站、代码仓库排前面。
// 认不出来的照样列出，只是靠后。
func researchFirstPartyLooking(host, raw string) bool {
	for _, prefix := range []string{"docs.", "developer.", "developers.", "dev.", "ai.", "support.", "help."} {
		if strings.HasPrefix(host, prefix) {
			return true
		}
	}
	if host == "github.com" || strings.HasSuffix(host, ".dev") {
		return true
	}
	path := strings.ToLower(raw)
	return strings.Contains(path, "/docs/") || strings.Contains(path, "/release")
}

// webSearchOutputForModel 去掉搜索成功时只对排障有用的诊断字段。原始输出照旧进运行
// 记录和可观测性元数据，这里只影响模型看到的那一份。
func webSearchOutputForModel(output string) string {
	var payload map[string]any
	if json.Unmarshal([]byte(output), &payload) != nil {
		return output
	}
	if status, _ := payload["status"].(string); status != "ok" {
		return output
	}
	for _, key := range []string{"providers", "attempts", "budget", "queries", "strategy", "stop_reason", "provider", "provider_type", "fallback_used"} {
		delete(payload, key)
	}
	slim, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return output
	}
	return string(slim)
}

func researchShort(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}
