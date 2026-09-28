// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"log"
	"net/url"
	"slices"
	"strings"
	"sync"
)

// models.dev 的数据随版本打包，不在运行时联网：同一个版本的行为是确定的，离线部署
// 也一样，测试也不碰网络。更新快照跑 `make models-dev`（即 go generate），它拉取
// https://models.dev/api.json，只留名称、模态、窗口和输出上限，gzip 后写进仓库。
// 数据来自 sst/models.dev，MIT 许可。
//
//go:generate go run models_dev_gen.go
//go:embed models_dev_snapshot.json.gz
var modelsDevSnapshot []byte

// ModelsDevCatalog 给模型列表补上 /models 接口常常不给的模态和 token 上限，也供请求
// 时按模型查窗口。数据来自随版本打包的 models.dev 快照。
type ModelsDevCatalog struct {
	once      sync.Once
	source    []byte
	providers map[string]map[string]ModelInfo
	// apis 是 models.dev 给每个服务商登记的 API 地址，用来按配置里的地址认出服务商。
	apis map[string]string
}

// sharedModelsDevCatalog 是进程里唯一的一份：同步模型列表和请求时查窗口共用。
var sharedModelsDevCatalog = &ModelsDevCatalog{source: modelsDevSnapshot}

// modelLimitCatalog 是请求时查窗口用的目录，测试里换成本地数据。
var modelLimitCatalog = sharedModelsDevCatalog

// SharedModelsDevCatalog 返回进程共用的 models.dev 目录。
func SharedModelsDevCatalog() *ModelsDevCatalog {
	return sharedModelsDevCatalog
}

// newModelsDevCatalogFromJSON 用一份 api.json 格式的数据建目录，测试用。
func newModelsDevCatalogFromJSON(body []byte) *ModelsDevCatalog {
	catalog := &ModelsDevCatalog{}
	catalog.once.Do(func() {
		catalog.providers, catalog.apis, _ = decodeModelsDevCatalog(body)
	})
	return catalog
}

// data 第一次用到时解压快照；解不开只记一条日志，按「目录里什么都没有」处理。
func (c *ModelsDevCatalog) data() (map[string]map[string]ModelInfo, map[string]string) {
	c.once.Do(func() {
		if len(c.source) == 0 {
			return
		}
		reader, err := gzip.NewReader(bytes.NewReader(c.source))
		if err == nil {
			var body []byte
			if body, err = io.ReadAll(reader); err == nil {
				c.providers, c.apis, err = decodeModelsDevCatalog(body)
			}
		}
		if err != nil {
			log.Printf("llm: bundled models.dev snapshot unreadable: %v", err)
		}
	})
	return c.providers, c.apis
}

// ContextLimit 返回 models.dev 里这个模型的 limit.context。
func (c *ModelsDevCatalog) ContextLimit(cfg ProviderConfig, model string) (int64, bool) {
	return c.limit(cfg, model, func(info ModelInfo) int64 { return info.ContextWindowTokens })
}

// limit 按「服务商 + 模型 ID」查 models.dev 快照。
//
// 查找顺序从严到宽：认得出服务商时，先精确匹配（和 opencode 一样），再去掉命名空间和
// 档位后缀，再认网关加的前缀；认不出服务商（自建中转）时，在所有服务商里按模型 ID
// 精确找，各家数值不同就取出现最多的那个。
func (c *ModelsDevCatalog) limit(cfg ProviderConfig, model string, pick func(ModelInfo) int64) (int64, bool) {
	if c == nil {
		return 0, false
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return 0, false
	}
	catalog, apis := c.data()
	if len(catalog) == 0 {
		return 0, false
	}
	names := modelsDevLookupNames(model)
	providers := modelsDevProviderIDs(cfg, apis)
	if len(providers) == 0 {
		return modelsDevMostCommonLimit(catalog, names, pick)
	}
	for _, name := range names {
		for _, provider := range providers {
			if info, ok := catalog[provider][name]; ok && pick(info) > 0 {
				return pick(info), true
			}
		}
	}
	// 网关还会在前面加自己的标记（antigravity-gemini-3.8-flash、gcp.claude-x），
	// 这时按「以分隔符 + 目录里的 ID 结尾」找，取最长的那个，免得短 ID 误配。
	for _, name := range names {
		for _, provider := range providers {
			if limit, ok := modelsDevSuffixMatch(catalog[provider], name, pick); ok {
				return limit, true
			}
		}
	}
	return 0, false
}

func modelsDevSuffixMatch(models map[string]ModelInfo, name string, pick func(ModelInfo) int64) (int64, bool) {
	lower := strings.ToLower(name)
	best, bestLen := int64(0), 0
	for id, info := range models {
		if pick(info) <= 0 || len(id) <= bestLen || len(id) >= len(lower) {
			continue
		}
		cut := len(lower) - len(id)
		if lower[cut:] != strings.ToLower(id) || !strings.ContainsRune("-._:", rune(lower[cut-1])) {
			continue
		}
		best, bestLen = pick(info), len(id)
	}
	return best, bestLen > 0
}

// modelsDevMostCommonLimit 在所有服务商里按模型 ID 精确找。同一个模型在不同转售商
// 那里登记的数常有出入，取出现次数最多的，平票取小的——宁可保守。
func modelsDevMostCommonLimit(catalog map[string]map[string]ModelInfo, names []string, pick func(ModelInfo) int64) (int64, bool) {
	for _, name := range names {
		counts := map[int64]int{}
		for _, models := range catalog {
			if info, ok := models[name]; ok && pick(info) > 0 {
				counts[pick(info)]++
			}
		}
		best, bestCount := int64(0), 0
		for value, count := range counts {
			if count > bestCount || count == bestCount && value < best {
				best, bestCount = value, count
			}
		}
		if bestCount > 0 {
			return best, true
		}
	}
	return 0, false
}

// modelsDevEffortSuffixes 是网关挂在模型名后面的推理档位。models.dev 只收基础
// 模型名，gemini-3.8-flash-low 要按 gemini-3.8-flash 查。
var modelsDevEffortSuffixes = []string{"-minimal", "-low", "-medium", "-high", "-xhigh", "-thinking"}

// modelsDevLookupNames 按先精确、后宽松的顺序给出查表用的名字：原名、去掉命名
// 空间（models/gemini-x）、再去掉档位后缀。精确匹配永远优先，所以 models.dev 里
// 本来就收了带后缀的条目时照用它的数。
func modelsDevLookupNames(model string) []string {
	names := []string{model}
	add := func(name string) {
		if name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	bare := strings.TrimSpace(model)
	if index := strings.LastIndex(bare, "/"); index >= 0 {
		bare = bare[index+1:]
	}
	add(bare)
	lower := strings.ToLower(bare)
	for _, suffix := range modelsDevEffortSuffixes {
		if strings.HasSuffix(lower, suffix) {
			add(bare[:len(bare)-len(suffix)])
			break
		}
	}
	return names
}

// Enrich 给同步下来的模型列表补名称、模态和 token 上限；列表里已有的值不覆盖。
func (c *ModelsDevCatalog) Enrich(_ context.Context, cfg ProviderConfig, models []ModelInfo) []ModelInfo {
	if c == nil || len(models) == 0 {
		return append([]ModelInfo(nil), models...)
	}
	catalog, apis := c.data()
	providers := modelsDevProviderIDs(cfg, apis)
	if len(providers) == 0 {
		return append([]ModelInfo(nil), models...)
	}
	out := append([]ModelInfo(nil), models...)
	for index := range out {
		for _, provider := range providers {
			info, ok := catalog[provider][out[index].ID]
			if !ok {
				continue
			}
			if out[index].Name == "" {
				out[index].Name = info.Name
			}
			if len(out[index].InputModalities) == 0 {
				out[index].InputModalities = append([]string(nil), info.InputModalities...)
			}
			if len(out[index].OutputModalities) == 0 {
				out[index].OutputModalities = append([]string(nil), info.OutputModalities...)
			}
			if out[index].ContextWindowTokens == 0 {
				out[index].ContextWindowTokens = info.ContextWindowTokens
			}
			if out[index].MaxInputTokens == 0 {
				out[index].MaxInputTokens = info.MaxInputTokens
			}
			if out[index].MaxOutputTokens == 0 {
				out[index].MaxOutputTokens = info.MaxOutputTokens
			}
			break
		}
	}
	return out
}

func decodeModelsDevCatalog(body []byte) (map[string]map[string]ModelInfo, map[string]string, error) {
	var payload map[string]struct {
		API    string `json:"api"`
		Models map[string]struct {
			Name       string `json:"name"`
			Modalities struct {
				Input  []string `json:"input"`
				Output []string `json:"output"`
			} `json:"modalities"`
			Limit struct {
				Context int64 `json:"context"`
				Input   int64 `json:"input"`
				Output  int64 `json:"output"`
			} `json:"limit"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, nil, err
	}
	out := make(map[string]map[string]ModelInfo, len(payload))
	apis := make(map[string]string, len(payload))
	for providerID, provider := range payload {
		if api := strings.TrimSpace(provider.API); api != "" {
			apis[providerID] = api
		}
		models := make(map[string]ModelInfo, len(provider.Models))
		for modelID, model := range provider.Models {
			models[modelID] = ModelInfo{
				ID:                  modelID,
				Name:                model.Name,
				InputModalities:     normalizeModalities(model.Modalities.Input),
				OutputModalities:    normalizeModalities(model.Modalities.Output),
				ContextWindowTokens: model.Limit.Context,
				MaxInputTokens:      model.Limit.Input,
				MaxOutputTokens:     model.Limit.Output,
			}
		}
		out[providerID] = models
	}
	return out, apis, nil
}

// modelsDevProviderIDs 认出这套配置对应 models.dev 里的哪家服务商：先看内置的几家
// 官方地址，再拿配置的 API 地址去比 models.dev 给每家登记的 api（主机名相同即算，
// 路径也对得上的排前面，区分智谱的普通和编程套餐这类同主机的两家）。
func modelsDevProviderIDs(cfg ProviderConfig, apis map[string]string) []string {
	ids := modelsDevProviderCandidates(cfg)
	if cfg.Provider != ProviderOpenAICompatible && cfg.Provider != ProviderAnthropic || strings.TrimSpace(cfg.BaseURL) == "" {
		return ids
	}
	base, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
	if err != nil || base.Hostname() == "" {
		return ids
	}
	host := strings.ToLower(base.Hostname())
	path := strings.TrimRight(strings.ToLower(base.Path), "/")
	var exact, sameHost []string
	for id, api := range apis {
		parsed, err := url.Parse(api)
		if err != nil || strings.ToLower(parsed.Hostname()) != host || slices.Contains(ids, id) {
			continue
		}
		apiPath := strings.TrimRight(strings.ToLower(parsed.Path), "/")
		if apiPath != "" && strings.HasPrefix(path, apiPath) {
			exact = append(exact, id)
		} else {
			sameHost = append(sameHost, id)
		}
	}
	slices.Sort(exact)
	slices.Sort(sameHost)
	return append(append(ids, exact...), sameHost...)
}

func modelsDevProviderCandidates(cfg ProviderConfig) []string {
	switch cfg.Provider {
	case ProviderGemini:
		return []string{"google"}
	case ProviderAnthropic:
		return []string{"anthropic"}
	case ProviderOpenAICompatible:
		if strings.TrimSpace(cfg.BaseURL) == "" {
			return []string{"openai"}
		}
		parsed, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
		if err != nil {
			return nil
		}
		host := strings.ToLower(parsed.Hostname())
		path := strings.ToLower(parsed.Path)
		switch {
		case host == "opencode.ai" && strings.Contains(path, "/go/"):
			return []string{"opencode-go", "opencode"}
		case host == "opencode.ai":
			return []string{"opencode"}
		case host == "api.openai.com":
			return []string{"openai"}
		case host == "api.deepseek.com":
			return []string{"deepseek"}
		case host == "generativelanguage.googleapis.com":
			return []string{"google"}
		case host == "openrouter.ai":
			return []string{"openrouter"}
		}
	}
	return nil
}
