package agent

import (
	"encoding/json"
	"regexp"
	"strings"
)

var citationURLPattern = regexp.MustCompile(`https?://[A-Za-z0-9\-._~:/?#@!$&*+,;=%()\[\]]+`)

// 尾部标点交给 trim：链接结尾的句号、右括号通常属于句子而不属于链接。
const citationTrailingPunctuation = `.,;:!?)]}>*_`

// extractCitationURLs 返回文本里出现的链接，已规范化并去重。
func extractCitationURLs(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range citationURLPattern.FindAllString(text, -1) {
		raw = strings.TrimRight(raw, citationTrailingPunctuation)
		canonical, _ := canonicalWebSearchURL(raw)
		if canonical == "" || seen[canonical] {
			continue
		}
		seen[canonical] = true
		out = append(out, raw)
	}
	return out
}

// SourceReference records discovery/read provenance without grading the answer.
type SourceReference struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
	Read  bool   `json:"read"`
	Cited bool   `json:"cited"`
}

func responseSources(answer string, steps []Step) []SourceReference {
	var sources []SourceReference
	indices := map[string]int{}
	add := func(source SourceReference) {
		key, _ := canonicalWebSearchURL(source.URL)
		if key == "" {
			return
		}
		if index, ok := indices[key]; ok {
			sources[index].Read = sources[index].Read || source.Read
			if source.Title != "" {
				sources[index].Title = source.Title
			}
			return
		}
		indices[key] = len(sources)
		sources = append(sources, source)
	}
	for _, step := range steps {
		if step.Error != "" || step.Skipped {
			continue
		}
		switch step.Tool {
		case WebSearchToolName:
			var result webSearchResult
			if json.Unmarshal([]byte(step.Output), &result) != nil {
				continue
			}
			for _, hit := range result.Results {
				add(SourceReference{URL: hit.URL, Title: hit.Title})
			}
			for _, url := range result.Sources {
				add(SourceReference{URL: url})
			}
		case browserRenderToolName:
			for _, page := range browserRenderPages(step.Output) {
				if page.Text == "" && len(page.FindMatches) == 0 {
					continue
				}
				add(SourceReference{URL: page.RequestedURL, Title: page.Title, Read: true})
				add(SourceReference{URL: page.URL, Title: page.Title, Read: true})
			}
		}
	}
	for _, url := range extractCitationURLs(answer) {
		key, _ := canonicalWebSearchURL(url)
		if i, ok := indices[key]; ok {
			sources[i].Cited = true
		}
	}
	// Preserve source order within each group.
	var ordered []SourceReference
	for _, cited := range []bool{true, false} {
		for _, source := range sources {
			if source.Cited == cited {
				ordered = append(ordered, source)
			}
		}
	}
	return ordered
}
