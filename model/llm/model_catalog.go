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

// ModelsDevCatalog 给模型列表补上 /models 接口常常不给的模态和 token 上限。数据来自
// 随版本打包的 models.dev 快照。
type ModelsDevCatalog struct {
	once      sync.Once
	source    []byte
	providers map[string]map[string]ModelInfo
	// apis 是 models.dev 给每个服务商登记的 API 地址，用来按配置里的地址认出服务商。
	apis map[string]string
	// modalities 缓存 InputModalities 的结果，每轮回复都要查一次，不必每次扫整份目录。
	modalities sync.Map
}

// sharedModelsDevCatalog 是进程里唯一的一份，同步模型列表时用它补全模态和上限。
var sharedModelsDevCatalog = &ModelsDevCatalog{source: modelsDevSnapshot}

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

// reasoningEffortModelSuffixes 是聚合网关常挂在模型名后面的思考档后缀：
// gemini-3.8-flash-low 实际就是 gemini-3.8-flash，目录里只登记后者。
var reasoningEffortModelSuffixes = []string{"-minimal", "-low", "-medium", "-high", "-xhigh"}

// InputModalities 按模型名在快照里查输入模态，不认服务商：自建网关的地址认不出是
// 哪家，而同一个模型在各家登记的输入模态基本一致，这里取所有登记的并集。名字查不到时
// 去掉思考档后缀再查一次。返回空表示目录里没有这个模型。
func (c *ModelsDevCatalog) InputModalities(model string) []string {
	if c == nil {
		return nil
	}
	name := bareModelName(model)
	if cached, ok := c.modalities.Load(name); ok {
		return cached.([]string)
	}
	modalities := c.lookupInputModalities(name)
	c.modalities.Store(name, modalities)
	return modalities
}

func (c *ModelsDevCatalog) lookupInputModalities(name string) []string {
	providers, _ := c.data()
	candidates := []string{name}
	for _, suffix := range reasoningEffortModelSuffixes {
		if trimmed, ok := strings.CutSuffix(name, suffix); ok && trimmed != "" {
			candidates = append(candidates, trimmed)
		}
	}
	for _, candidate := range candidates {
		seen := map[string]bool{}
		var modalities []string
		for _, models := range providers {
			for id, info := range models {
				if bareModelName(id) != candidate {
					continue
				}
				for _, modality := range info.InputModalities {
					if !seen[modality] {
						seen[modality] = true
						modalities = append(modalities, modality)
					}
				}
			}
		}
		if len(modalities) > 0 {
			slices.Sort(modalities)
			return modalities
		}
	}
	return nil
}
