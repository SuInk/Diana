// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// searchRef 给搜索候选一个短编号，参照 Codex web.run 的 ref_id：模型照抄长网址常会
// 抄错或干脆猜一个，编号短得抄不错。编号取自规范化 URL 的哈希，同一页面在不同次
// 搜索里编号相同，一轮内多次搜索也不会撞号。
func searchRef(rawURL string) string {
	key, _ := canonicalWebSearchURL(rawURL)
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return "r" + hex.EncodeToString(sum[:3])
}

// resolveSearchRefs 把 browser_render 入参里的编号换回本轮搜索结果的原始 URL。
// 认不出的编号原样留着，交给 browser_render 按无效网址报错。
func resolveSearchRefs(tool string, input map[string]any, steps []Step) map[string]any {
	if tool != browserRenderToolName || input == nil {
		return input
	}
	refs := map[string]string{}
	for _, step := range steps {
		if step.Tool != WebSearchToolName || step.Error != "" || step.Skipped {
			continue
		}
		var result webSearchResult
		if json.Unmarshal([]byte(step.Output), &result) != nil {
			continue
		}
		for _, hit := range result.Results {
			if ref := searchRef(hit.URL); ref != "" {
				refs[ref] = hit.URL
			}
		}
	}
	if len(refs) == 0 {
		return input
	}
	resolve := func(value any) any {
		if s, ok := value.(string); ok {
			if url, found := refs[s]; found {
				return url
			}
		}
		return value
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	if value, ok := out["url"]; ok {
		out["url"] = resolve(value)
	}
	switch urls := out["urls"].(type) {
	case []any:
		resolved := make([]any, len(urls))
		for i, value := range urls {
			resolved[i] = resolve(value)
		}
		out["urls"] = resolved
	case []string:
		resolved := make([]string, len(urls))
		for i, value := range urls {
			resolved[i], _ = resolve(value).(string)
		}
		out["urls"] = resolved
	}
	return out
}
