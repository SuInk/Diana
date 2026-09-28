// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	modelsDevCatalogURL = "https://models.dev/api.json"
	modelsDevCacheTTL   = 6 * time.Hour
)

// ModelsDevCatalog adds model modalities and context/output limits that many
// OpenAI-compatible /models endpoints omit. Failures are non-fatal; unknown
// capabilities and the configured conservative context fallback remain in force.
type ModelsDevCatalog struct {
	mu        sync.Mutex
	client    *http.Client
	url       string
	fetchedAt time.Time
	providers map[string]map[string]ModelInfo
	// apis 是 models.dev 给每个服务商登记的 API 地址，用来按配置里的地址认出服务商。
	apis map[string]string
	// refreshing 和 lastAttempt 只给 OutputLimit 的后台刷新用：请求路径上不能等
	// 网络，离线部署也不能每个请求都去拉一次。
	refreshing  bool
	lastAttempt time.Time
}

// modelsDevRetryInterval 是后台刷新失败后多久再试。
const modelsDevRetryInterval = 10 * time.Minute

// sharedModelsDevCatalog 是进程里唯一的 models.dev 缓存：同步模型列表和请求时查
// 输出上限用的是同一份，不重复下载 api.json。
var sharedModelsDevCatalog = NewModelsDevCatalog(nil)

// modelLimitCatalog 是请求时查输出上限和窗口用的目录，测试里换成本地数据。
var modelLimitCatalog = sharedModelsDevCatalog

// SharedModelsDevCatalog 返回进程共用的 models.dev 目录。
func SharedModelsDevCatalog() *ModelsDevCatalog {
	return sharedModelsDevCatalog
}

// Warm 在后台预取一次目录，让启动后的第一批请求就能查到上限。
func (c *ModelsDevCatalog) Warm() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.startRefreshLocked()
	c.mu.Unlock()
}

// ContextLimit 返回 models.dev 里这个模型的 limit.context。
func (c *ModelsDevCatalog) ContextLimit(cfg ProviderConfig, model string) (int64, bool) {
	return c.limit(cfg, model, func(info ModelInfo) int64 { return info.ContextWindowTokens })
}

// limit 按「服务商 + 模型 ID」查 models.dev。只读缓存、不等网络：缓存为空或过期时
// 在后台刷新，这一次按查不到处理，由调用方退回默认值。
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
	c.mu.Lock()
	if len(c.providers) == 0 || time.Since(c.fetchedAt) >= modelsDevCacheTTL {
		c.startRefreshLocked()
	}
	catalog, apis := c.providers, c.apis
	c.mu.Unlock()
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

// recoverCatalogRefreshPanic 让解析坏掉的 api.json 只丢掉这一次刷新，不拖垮进程。
func recoverCatalogRefreshPanic() {
	if recovered := recover(); recovered != nil {
		log.Printf("llm: models.dev refresh panicked: %v", recovered)
	}
}

// startRefreshLocked 在后台拉一次目录；已经在拉、或刚失败过就不再发起。
func (c *ModelsDevCatalog) startRefreshLocked() {
	if c.refreshing || len(c.providers) > 0 && time.Since(c.fetchedAt) < modelsDevCacheTTL {
		return
	}
	if !c.lastAttempt.IsZero() && time.Since(c.lastAttempt) < modelsDevRetryInterval {
		return
	}
	c.refreshing = true
	c.lastAttempt = time.Now()
	go func() {
		defer recoverCatalogRefreshPanic()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		_, _ = c.load(ctx)
		c.mu.Lock()
		c.refreshing = false
		c.mu.Unlock()
	}()
}

func NewModelsDevCatalog(client *http.Client) *ModelsDevCatalog {
	return newModelsDevCatalog(client, modelsDevCatalogURL)
}

func newModelsDevCatalog(client *http.Client, endpoint string) *ModelsDevCatalog {
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	return &ModelsDevCatalog{client: client, url: endpoint}
}

func (c *ModelsDevCatalog) Enrich(ctx context.Context, cfg ProviderConfig, models []ModelInfo) []ModelInfo {
	if c == nil || len(models) == 0 {
		return append([]ModelInfo(nil), models...)
	}
	catalog, err := c.load(ctx)
	if err != nil {
		return append([]ModelInfo(nil), models...)
	}
	c.mu.Lock()
	apis := c.apis
	c.mu.Unlock()
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

// load 返回缓存的目录，过期时重新下载。下载期间不持锁：OutputLimit 在请求路径上
// 读缓存，不能被一次慢下载卡住。并发时可能多下载一次，结果相同。
func (c *ModelsDevCatalog) load(ctx context.Context) (map[string]map[string]ModelInfo, error) {
	c.mu.Lock()
	if len(c.providers) > 0 && time.Since(c.fetchedAt) < modelsDevCacheTTL {
		providers := c.providers
		c.mu.Unlock()
		return providers, nil
	}
	c.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, modelListHTTPError{statusCode: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	providers, apis, err := decodeModelsDevCatalog(body)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.providers = providers
	c.apis = apis
	c.fetchedAt = time.Now()
	c.mu.Unlock()
	return providers, nil
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
