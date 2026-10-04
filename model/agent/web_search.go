// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	WebSearchToolName               = "web_search"
	DefaultWebSearchConfigFile      = "web-search.json"
	defaultWebSearchTimeout         = 35 * time.Second
	defaultWebSearchProviderTimeout = 12 * time.Second
	defaultWebSearchMaxResults      = 5
	maxWebSearchResponseBytes       = 2 * 1024 * 1024
	mcpProtocolVersion              = "2025-03-26"
)

type WebSearchTool struct {
	timeout          time.Duration
	maxBytes         int
	maxQueries       int
	maxProviderCalls int
	configPath       string
	client           *http.Client
	providers        []WebSearchProviderConfig
	apiKeys          map[string]string
	renderer         PageRenderer
}

// WebSearchToolOptions configures the search tool without exposing provider
// credentials through the serializable provider model.
type WebSearchToolOptions struct {
	Config           WebSearchConfig
	APIKeys          map[string]string
	Timeout          time.Duration
	MaxOutputChars   int
	MaxQueries       int
	MaxProviderCalls int
	Client           *http.Client
	// Renderer 是搜索引擎模式打开结果页用的沙盒浏览器；只配了 API 搜索源时可以不给。
	Renderer PageRenderer
}

// NewWebSearchTool creates a search tool from an in-memory plugin snapshot.
// The legacy file/environment loader remains available for older deployments,
// but new WebUI configuration is supplied by the official plugin.
func NewWebSearchTool(options WebSearchToolOptions) (*WebSearchTool, error) {
	config, err := NormalizeWebSearchConfig(options.Config)
	if err != nil {
		return nil, err
	}
	apiKeys := make(map[string]string, len(options.APIKeys))
	for name, value := range options.APIKeys {
		if value = strings.TrimSpace(value); value != "" {
			apiKeys[strings.TrimSpace(name)] = value
		}
	}
	return &WebSearchTool{
		timeout:          options.Timeout,
		maxBytes:         options.MaxOutputChars,
		maxQueries:       options.MaxQueries,
		maxProviderCalls: options.MaxProviderCalls,
		client:           options.Client,
		providers:        append([]WebSearchProviderConfig(nil), config.Providers...),
		apiKeys:          apiKeys,
		renderer:         options.Renderer,
	}, nil
}

type WebSearchConfig struct {
	Providers []WebSearchProviderConfig `json:"providers"`
}

type WebSearchProviderConfig struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	URL        string `json:"url"`
	Tool       string `json:"tool,omitempty"`
	APIKeyEnv  string `json:"api_key_env,omitempty"`
	TimeoutMS  int    `json:"timeout_ms,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
	Disabled   bool   `json:"disabled,omitempty"`
}

type webSearchConfig = WebSearchConfig
type webSearchProviderConfig = WebSearchProviderConfig

type webSearchAttempt struct {
	Provider    string `json:"provider"`
	Type        string `json:"type"`
	QueryIndex  int    `json:"query_index"`
	QueryHash   string `json:"query_hash"`
	Status      string `json:"status"`
	ErrorCode   string `json:"error_code,omitempty"`
	DurationMS  int64  `json:"duration_ms,omitempty"`
	ResultCount int    `json:"result_count,omitempty"`
	Error       string `json:"error,omitempty"`
}

type mcpRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *mcpRPCError    `json:"error,omitempty"`
}

type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

var (
	webSearchURLPattern    = regexp.MustCompile(`https?://[^\s"']+`)
	webSearchEnvNameRegexp = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

func (t *WebSearchTool) Name() string {
	return WebSearchToolName
}

// Diana 的联网决策策略；与工具契约共享。公开 Codex CLI 不提供这段提示词。
// 同时放进工具契约与 Runner 提示词，避免两处对「该不该搜」给出不同答案。
const webSearchDecisionBoundary = `拿不准就搜；信息变化或记错概率至少 10% 时必须查。用户要搜索、动态事实、陌生或新兴技术、具体技术方案的定义/评价/项目适配、推荐选型、引用或未读链接、医疗法律金融问题，都先查。问你自身能力也不豁免外部概念。直接搜索再答，不要问要不要查。稳定常识、纯创作或已给材料可直接答；用户明确不联网时遵守。`

// A short current-turn reminder follows caller persona/history. It changes no
// tool choice and applies to every topic, including conversational requests.
const webSearchTurnPolicy = `本轮联网与调研要求：用户要查询、使用、购买、实现、比较或评价具体外部产品、版本或技术时，先搜索当前事实再回应；即使语气像玩笑，也要完成其中的实际请求，人设和接梗不能代替调研。聊天历史和你熟悉的旧知识只作线索。每个查询保留用户给出的名称和版本，用自然关键词定位相关官方发布、文档或源码；不要把其他版本或相邻型号并入 OR 查询。官网候选不相关时换查询继续定位，不能把一次无关结果当作官网没有覆盖；从候选中选择直接回答问题的页面，读取后再判断，原文不相关就换页面或查询。用户问“是不是不存在/不支持”时同样核实；没搜到、页面没提到或读取失败都只能说明本次未确认，不能推出不存在。发现与已有知识冲突的新官方材料时，按材料及其日期修正答案。答复里的数字、日期、规格、价格和实现机制必须由实际读到的正文直接支持；搜索摘要、旧印象和推测不能补成事实，没有依据的细节省略或说明未确认。明确不联网时遵守。`

const webSearchDecisionPolicy = webSearchDecisionBoundary + `
来源优先级：目标项目/服务的官网、官方文档、官方仓库和发布记录优先；第三方文章只作发现官方出处与交叉核对的线索。首次查询用实体原名加官网、官方文档或所需事实定位归属，先确认官网与仓库是谁维护，不能猜域名或把同名项目当成目标。发现官方候选 URL 后优先打开相关原文，必要时用 find 定位；官网首页或目录也不能支持页面未写出的细节。只有官方来源无法访问或确实未覆盖问题时才采用其他可信资料，并在答复中明确来源性质和未获官方确认的部分。价格、版本、当前功能尤其优先核对官方页面，不能读到转载就结束。
调研顺序：简短查询定位来源 → 用 browser_render 打开相关一手页面并用 find 定位答案 → 根据原文回答并附来源链接。搜索返回的是候选标题、摘要和 URL；选择直接涉及问题的页面读取，原文不相关时换页面或查询。搜索命中或成功读取本身不证明问题已解决。视频/音频页面的标题、简介和评论不等于已读取转写或播放内容，不能据此补出讲者未出现在文字中的机制细节。技术概念优先寻找可直接阅读的文档、源码或论文；搜索结果中的视频只能作为线索。读取成功不代表来源就是官方；第三方文章、转售渠道的价格不能当官方价格，搜索结果声称“官方”也不等于已核对。
每项具体结论都要有已读来源直接支持；先确认项目的官网与仓库归属，不能把 fork 或同名项目冒充原项目，采用派生实现时明确区分；价格保留来源的货币、单位与适用条件，不能补上未核对的另一币种报价。项目现状不能从单个提案是否关闭外推整个项目是否支持；评估自己的项目时，未读取实现就把架构假设明确写成条件。
每次用已有知识作出事实假设，都先判断它是否稳定。10% 是判断阈值，不要求计算精确概率；不要等到确定自己不知道才搜索。
必须联网的动态事实包括新闻、价格、法律政策、规则、日程、产品规格、人物机构现状、软件库和 API 的版本与支持情况。具体商品、品牌、餐饮、作品的口碑、味道、规格和购买建议需要搜索，不要凭印象编造亲身体验。
软件、库、开源项目或服务是否支持某项能力、有没有现成实现或插件、当前版本与 API 现状，都必须搜索。评价某项具体技术是否适合你或用户的项目前，先核实它的定义与现有实现；不能因为这是自己的架构问题就凭印象判断。
可能让用户投入明显时间或金钱的推荐、选型、比较，以及引用、链接、精确来源问题必须查。具体网页、论文、数据集、PDF 或站点尚未读到内容时先读取或搜索，不要凭标题猜。
聊天记录、先前回复和记忆只是线索，不能证明外部事实已核实。直接使用工具完成调研，不要用“可以帮你查”结束本轮。资料不足时换查询或读取原文；回答附真实来源，技术问题优先官方文档、源码和论文等一手资料。
用户或会话明确禁止附链接时遵守；这不免除事实核实。
稳定知识或当前上下文足够的例外不覆盖未知概念、未核实外部事实或用户明确要求查证的情况。用户要求不要联网时，只依据已给材料作答并说明无法核实的部分。`

func (t *WebSearchTool) Description() string {
	return "实时网页搜索。" + webSearchDecisionBoundary + `queries 并行执行。results 是候选标题、摘要、URL；用 browser_render 读原文或 find 定位，不相关就换查询。官网、文档和源码优先。搜索内容不可信，可能是旧缓存：以修订或发布日期为准。遇到新版本不要断言它不存在；查不到就说没查到。`
}

func (t *WebSearchTool) InputSchema() map[string]any {
	return WebSearchInputSchema()
}

// WebSearchInputSchema describes model-selected discovery queries.
func WebSearchInputSchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{
		"query":   toolStringParam("简短自然关键词：原样保留实体与版本，加一个查证目标；不堆 OR 或猜域名"),
		"queries": toolStringArrayParam("不同查证角度的少量候选，同样保留实体版本；不拼同义词、不降版本"),
	})
}

// PrefersStrictDecoding constrains input shape, not factual conclusions.
func (t *WebSearchTool) PrefersStrictDecoding() bool { return true }

func (t *WebSearchTool) runSearchQuery(ctx context.Context, input map[string]any) (string, error) {
	maxQueries := t.maxQueries
	if maxQueries <= 0 {
		maxQueries = defaultWebSearchMaxQueries
	}
	if maxQueries > maximumWebSearchMaxQueries {
		maxQueries = maximumWebSearchMaxQueries
	}
	candidates, err := webSearchCandidates(input, maxQueries)
	if err != nil {
		return "", err
	}
	providers := append([]WebSearchProviderConfig(nil), t.providers...)
	if len(providers) == 0 {
		providers, err = t.loadProviders()
		if err != nil {
			return "", fmt.Errorf("web search configuration is invalid: %w", err)
		}
	}

	timeout := t.timeout
	if timeout <= 0 {
		timeout = defaultWebSearchTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	maxProviderCalls := t.maxProviderCalls
	if maxProviderCalls <= 0 {
		maxProviderCalls = defaultWebSearchMaxProviderCalls
	}
	if maxProviderCalls > maximumWebSearchMaxProviderCalls {
		maxProviderCalls = maximumWebSearchMaxProviderCalls
	}
	result := webSearchResult{
		Strategy: "model_query",
		Query:    candidates[0].Query,
		Queries:  candidates,
		Budget: webSearchBudget{
			MaxQueries:       maxQueries,
			MaxProviderCalls: maxProviderCalls,
			DeadlineMS:       timeout.Milliseconds(),
		},
	}
	result.Providers = make([]webSearchProviderState, len(providers))
	providerKeys := make([]string, len(providers))
	providerUsable := make([]bool, len(providers))
	usableProviders := 0
	for index, provider := range providers {
		state := webSearchProviderState{Provider: provider.Name, Type: provider.Type, Status: "not_executed"}
		if provider.Disabled {
			state.Status = "skipped"
			state.Reason = "disabled"
			result.Providers[index] = state
			continue
		}
		apiKey := strings.TrimSpace(t.apiKeys[provider.Name])
		if apiKey == "" && provider.APIKeyEnv != "" {
			apiKey = strings.TrimSpace(os.Getenv(provider.APIKeyEnv))
			if apiKey == "" {
				state.Status = "skipped"
				state.Reason = "missing_credentials"
				result.Providers[index] = state
				continue
			}
		}
		providerKeys[index] = apiKey
		providerUsable[index] = true
		usableProviders++
		result.Providers[index] = state
	}
	if usableProviders == 0 {
		result.Status = "provider_error"
		result.StopReason = "no_usable_provider"
		return t.formatExplorationResult(result)
	}

	var bestContent string
	var bestQueryIndex, bestProviderIndex int
	bestQueryIndex, bestProviderIndex = -1, -1
	budgetExhausted := false
	anyProviderError := false
	anyTimeout := false
	for queryIndex := range result.Queries {
		if runCtx.Err() != nil {
			break
		}
		if result.Budget.ProviderCalls >= maxProviderCalls {
			budgetExhausted = true
			break
		}
		candidate := &result.Queries[queryIndex]
		candidate.Status = "attempted"
		result.Budget.QueriesUsed++
		candidateOutcome := "no_results"
		for providerIndex, provider := range providers {
			if !providerUsable[providerIndex] {
				continue
			}
			if result.Budget.ProviderCalls >= maxProviderCalls {
				budgetExhausted = true
				break
			}
			result.Budget.ProviderCalls++
			state := &result.Providers[providerIndex]
			state.Status = "attempted"
			state.Attempts++
			providerTimeout := time.Duration(provider.TimeoutMS) * time.Millisecond
			providerCtx, providerCancel := context.WithTimeout(runCtx, providerTimeout)
			startedAt := time.Now()
			content, providerErr := t.runProvider(providerCtx, provider, candidate.Query, providerKeys[providerIndex])
			providerCtxErr := providerCtx.Err()
			providerCancel()
			attempt := webSearchAttempt{
				Provider:   provider.Name,
				Type:       provider.Type,
				QueryIndex: queryIndex,
				QueryHash:  candidate.Hash,
				DurationMS: time.Since(startedAt).Milliseconds(),
			}
			if providerErr == nil && strings.TrimSpace(content) == "" {
				providerErr = errWebSearchNoResults
			}
			if providerErr != nil {
				outcome := classifyWebSearchError(providerErr, providerCtxErr, runCtx.Err())
				attempt.Status = outcome
				attempt.ErrorCode = outcome
				attempt.Error = safeWebSearchError(providerErr)
				result.Attempts = append(result.Attempts, attempt)
				state.Outcome = mergeWebSearchOutcome(state.Outcome, outcome)
				candidateOutcome = mergeWebSearchOutcome(candidateOutcome, outcome)
				anyProviderError = anyProviderError || outcome == "provider_error"
				if errors.Is(providerErr, errWebSearchBlocked) {
					// 被人机验证拦下的引擎换个关键词还是会被拦，这次调用里不再用它。
					providerUsable[providerIndex] = false
					state.Reason = "blocked_by_verification"
				}
				anyTimeout = anyTimeout || outcome == "timeout"
				if runCtx.Err() != nil {
					break
				}
				continue
			}

			content = strings.TrimSpace(content)
			sources := webSearchResultSources(content)
			attempt.ResultCount = len(sources)
			if len(sources) == 0 {
				attempt.Status = "insufficient_evidence"
				attempt.ErrorCode = "insufficient_evidence"
				result.Attempts = append(result.Attempts, attempt)
				state.Outcome = mergeWebSearchOutcome(state.Outcome, "insufficient_evidence")
				candidateOutcome = mergeWebSearchOutcome(candidateOutcome, "insufficient_evidence")
				if bestContent == "" {
					bestContent = content
					bestQueryIndex = queryIndex
					bestProviderIndex = providerIndex
				}
				continue
			}

			attempt.Status = "success"
			result.Attempts = append(result.Attempts, attempt)
			state.Outcome = "success"
			candidate.Outcome = "success"
			result.Status = "ok"
			result.StopReason = "candidate_sources_found"
			result.SelectedQuery = candidate.Query
			result.Provider = provider.Name
			result.ProviderType = provider.Type
			result.FallbackUsed = queryIndex > 0 || providerIndex > 0
			result.Sources = sources
			result.Results = searchHits(content, candidate.Query, provider.Name)
			if len(result.Results) == 0 {
				result.Content = content
			}
			markWebSearchRemainder(result.Queries, result.Providers, queryIndex, "candidate_sources_found")
			return t.formatExplorationResult(result)
		}
		candidate.Outcome = candidateOutcome
	}

	switch {
	case runCtx.Err() != nil:
		result.Status = "timeout"
		result.StopReason = "deadline_exceeded"
	case budgetExhausted:
		result.Status = "budget_exhausted"
		result.StopReason = "provider_call_budget_exhausted"
	case bestContent != "":
		result.Status = "insufficient_evidence"
		result.StopReason = "results_lacked_verifiable_sources"
	case anyProviderError:
		result.Status = "provider_error"
		result.StopReason = "providers_failed"
	case anyTimeout:
		result.Status = "timeout"
		result.StopReason = "providers_timed_out"
	default:
		result.Status = "no_results"
		result.StopReason = "all_candidates_exhausted"
	}
	if bestContent != "" {
		result.Content = bestContent
		result.SelectedQuery = result.Queries[bestQueryIndex].Query
		result.Provider = providers[bestProviderIndex].Name
		result.ProviderType = providers[bestProviderIndex].Type
		result.FallbackUsed = bestQueryIndex > 0 || bestProviderIndex > 0
	}
	markWebSearchRemainder(result.Queries, result.Providers, -1, result.StopReason)
	return t.formatExplorationResult(result)
}

func (t *WebSearchTool) loadProviders() ([]webSearchProviderConfig, error) {
	raw := strings.TrimSpace(os.Getenv("DIANA_WEB_SEARCH_CONFIGS"))
	if raw == "" {
		configuredPath := strings.TrimSpace(os.Getenv("DIANA_WEB_SEARCH_CONFIG_FILE"))
		path := configuredPath
		if path == "" {
			path = t.configPath
		} else if !filepath.IsAbs(path) && t.configPath != "" {
			path = filepath.Join(filepath.Dir(t.configPath), path)
		}
		if path != "" {
			body, err := os.ReadFile(path)
			switch {
			case err == nil:
				raw = strings.TrimSpace(string(body))
			case configuredPath != "" || !errors.Is(err, os.ErrNotExist):
				return nil, err
			}
		}
	}

	providers := defaultWebSearchProviders()
	if raw != "" {
		parsed, err := parseWebSearchProviders([]byte(raw))
		if err != nil {
			return nil, err
		}
		providers = parsed
	}
	return normalizeWebSearchProviders(providers)
}

func ResolveWebSearchConfigPath(workDir string) string {
	path := strings.TrimSpace(os.Getenv("DIANA_WEB_SEARCH_CONFIG_FILE"))
	if path == "" {
		path = DefaultWebSearchConfigFile
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if strings.TrimSpace(workDir) == "" {
		workDir = "."
	}
	return filepath.Clean(filepath.Join(workDir, path))
}

// LoadWebSearchConfig reads the file managed by the WebUI. A missing file uses
// the built-in provider order so fresh installations work without setup.
func LoadWebSearchConfig(path string) (WebSearchConfig, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultWebSearchConfig(), nil
	}
	if err != nil {
		return WebSearchConfig{}, err
	}
	providers, err := parseWebSearchProviders(raw)
	if err != nil {
		return WebSearchConfig{}, err
	}
	providers, err = normalizeWebSearchProviders(providers)
	if err != nil {
		return WebSearchConfig{}, err
	}
	return WebSearchConfig{Providers: providers}, nil
}

// NormalizeWebSearchConfig applies the same validation and defaults used at
// runtime before a configuration is persisted by the WebUI.
func NormalizeWebSearchConfig(config WebSearchConfig) (WebSearchConfig, error) {
	providers, err := normalizeWebSearchProviders(config.Providers)
	if err != nil {
		return WebSearchConfig{}, err
	}
	if len(providers) == 0 {
		return WebSearchConfig{}, errors.New("providers are required")
	}
	return WebSearchConfig{Providers: providers}, nil
}

func DefaultWebSearchConfig() WebSearchConfig {
	return WebSearchConfig{Providers: defaultWebSearchProviders()}
}

// TestWebSearchProvider runs one configured provider without changing the
// active order. API key values are read only from the named environment
// variable and never included in the returned content.
func TestWebSearchProvider(ctx context.Context, provider WebSearchProviderConfig, query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", errors.New("query is required")
	}
	normalized, err := normalizeWebSearchProviders([]webSearchProviderConfig{provider})
	if err != nil {
		return "", err
	}
	provider = normalized[0]
	apiKey := ""
	if provider.APIKeyEnv != "" {
		apiKey = strings.TrimSpace(os.Getenv(provider.APIKeyEnv))
		if apiKey == "" {
			return "", fmt.Errorf("missing environment variable %s", provider.APIKeyEnv)
		}
	}
	testCtx, cancel := context.WithTimeout(ctx, time.Duration(provider.TimeoutMS)*time.Millisecond)
	defer cancel()
	tool := &WebSearchTool{maxBytes: DefaultMaxToolOutputChars}
	content, err := tool.runProvider(testCtx, provider, query, apiKey)
	if err != nil {
		return "", errors.New(safeWebSearchError(err))
	}
	return truncateRunes(strings.TrimSpace(content), 2_000), nil
}

func parseWebSearchProviders(raw []byte) ([]webSearchProviderConfig, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, errors.New("empty configuration")
	}
	var providers []webSearchProviderConfig
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &providers); err != nil {
			return nil, err
		}
	} else {
		var cfg webSearchConfig
		if err := json.Unmarshal(trimmed, &cfg); err != nil {
			return nil, err
		}
		providers = cfg.Providers
	}
	if len(providers) == 0 {
		return nil, errors.New("providers are required")
	}
	return providers, nil
}

func defaultWebSearchProviders() []webSearchProviderConfig {
	var providers []webSearchProviderConfig
	for _, engine := range DefaultSearchEngines {
		providers = append(providers, webSearchProviderConfig{Name: engine, Type: WebSearchProviderSearchEngine, Tool: engine, TimeoutMS: defaultSearchEngineTimeoutMS, MaxResults: defaultWebSearchMaxResults})
	}
	return providers
}

func normalizeWebSearchProviders(providers []webSearchProviderConfig) ([]webSearchProviderConfig, error) {
	seenNames := map[string]bool{}
	out := make([]webSearchProviderConfig, 0, len(providers))
	for index, provider := range providers {
		provider.Name = strings.TrimSpace(provider.Name)
		provider.Type = strings.ToLower(strings.TrimSpace(provider.Type))
		provider.URL = strings.TrimSpace(provider.URL)
		provider.Tool = strings.TrimSpace(provider.Tool)
		provider.APIKeyEnv = strings.TrimSpace(provider.APIKeyEnv)
		if provider.Name == "" {
			provider.Name = fmt.Sprintf("%s-%d", firstNonEmpty(provider.Type, "provider"), index+1)
		}
		if seenNames[provider.Name] {
			return nil, fmt.Errorf("duplicate provider name %q", provider.Name)
		}
		seenNames[provider.Name] = true
		switch provider.Type {
		case "exa", "exa_mcp", "mcp":
			provider.Type = "exa_mcp"
			if provider.URL == "" {
				provider.URL = "https://mcp.exa.ai/mcp?tools=web_search_exa"
			}
			if provider.Tool == "" {
				provider.Tool = "web_search_exa"
			}
		case "tavily":
			if provider.URL == "" {
				provider.URL = "https://api.tavily.com/search"
			}
			if provider.APIKeyEnv == "" {
				provider.APIKeyEnv = "TAVILY_API_KEY"
			}
		case WebSearchProviderSearchEngine:
			// Tool 是引擎名（google、bing……），URL 默认取引擎自己的搜索地址；
			// 自定义引擎的 URL 是带 {query} 的搜索地址模板。
			if CustomSearchEngineURL(provider.URL) {
				provider.Tool = "custom"
			} else {
				provider.Tool = strings.ToLower(firstNonEmpty(provider.Tool, provider.Name))
				engine, ok := searchEngines[provider.Tool]
				if !ok {
					return nil, fmt.Errorf("provider %q has unknown search engine %q", provider.Name, provider.Tool)
				}
				if provider.URL == "" {
					provider.URL = engine.searchURL
				}
			}
			if provider.TimeoutMS <= 0 {
				provider.TimeoutMS = defaultSearchEngineTimeoutMS
			}
		default:
			return nil, fmt.Errorf("provider %q has unsupported type %q", provider.Name, provider.Type)
		}
		if provider.APIKeyEnv != "" && !webSearchEnvNameRegexp.MatchString(provider.APIKeyEnv) {
			return nil, fmt.Errorf("provider %q has invalid api_key_env", provider.Name)
		}
		if err := validateWebSearchURL(strings.ReplaceAll(provider.URL, SearchEngineQueryPlaceholder, "q")); err != nil {
			return nil, fmt.Errorf("provider %q: %w", provider.Name, err)
		}
		if provider.TimeoutMS <= 0 {
			provider.TimeoutMS = int(defaultWebSearchProviderTimeout / time.Millisecond)
		}
		if provider.TimeoutMS < 1_000 {
			provider.TimeoutMS = 1_000
		}
		if provider.TimeoutMS > 30_000 {
			provider.TimeoutMS = 30_000
		}
		if provider.MaxResults <= 0 {
			provider.MaxResults = defaultWebSearchMaxResults
		}
		if provider.MaxResults > 10 {
			provider.MaxResults = 10
		}
		out = append(out, provider)
	}
	return out, nil
}

func validateWebSearchURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return errors.New("invalid provider URL")
	}
	if parsed.User != nil {
		return errors.New("provider URL must not contain credentials")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	host := strings.ToLower(parsed.Hostname())
	if parsed.Scheme == "http" && (host == "127.0.0.1" || host == "localhost" || host == "::1") {
		return nil
	}
	return errors.New("provider URL must use HTTPS")
}

func (t *WebSearchTool) runProvider(ctx context.Context, provider webSearchProviderConfig, query, apiKey string) (string, error) {
	switch provider.Type {
	case "exa_mcp":
		return t.runExaMCP(ctx, provider, query, apiKey)
	case "tavily":
		return t.runTavily(ctx, provider, query, apiKey)
	case WebSearchProviderSearchEngine:
		return t.runSearchEngine(ctx, provider, query)
	default:
		return "", fmt.Errorf("unsupported provider type %q", provider.Type)
	}
}

func (t *WebSearchTool) runExaMCP(ctx context.Context, provider webSearchProviderConfig, query, apiKey string) (string, error) {
	initialize := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "diana",
				"version": "0.1.0",
			},
		},
	}
	initResponse, sessionID, err := t.callRemoteMCP(ctx, provider.URL, apiKey, "", initialize, true)
	if err != nil {
		return "", fmt.Errorf("MCP initialize failed: %w", err)
	}
	if initResponse.Error != nil {
		return "", fmt.Errorf("MCP initialize error %d: %s", initResponse.Error.Code, initResponse.Error.Message)
	}
	if len(initResponse.Result) == 0 {
		return "", errors.New("MCP initialize returned no result")
	}
	if sessionID != "" {
		defer t.closeRemoteMCPSession(provider.URL, apiKey, sessionID)
	}

	initialized := map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	}
	if _, _, err := t.callRemoteMCP(ctx, provider.URL, apiKey, sessionID, initialized, false); err != nil {
		return "", fmt.Errorf("MCP initialized notification failed: %w", err)
	}

	arguments := map[string]any{
		"query":      query,
		"numResults": provider.MaxResults,
	}
	if endpoint, err := url.Parse(provider.URL); err == nil && strings.EqualFold(endpoint.Hostname(), "mcp.exa.ai") && provider.Tool == "web_search_exa" {
		arguments["objective"] = "Find primary sources directly answering: " + query + ". For current/latest claims, prioritize official dated records; absence from search is not proof of nonexistence."
	}
	if provider.Tool == "web_search_advanced_exa" {
		arguments["type"] = "auto"
		arguments["enableHighlights"] = true
		arguments["highlightsMaxCharacters"] = 1_200
		arguments["textMaxCharacters"] = 1_200
	}
	call := map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      provider.Tool,
			"arguments": arguments,
		},
	}
	response, _, err := t.callRemoteMCP(ctx, provider.URL, apiKey, sessionID, call, true)
	if err != nil {
		return "", fmt.Errorf("MCP tool call failed: %w", err)
	}
	if response.Error != nil {
		return "", fmt.Errorf("MCP tool error %d: %s", response.Error.Code, response.Error.Message)
	}
	return extractMCPToolText(response.Result)
}

func (t *WebSearchTool) callRemoteMCP(ctx context.Context, endpoint, apiKey, sessionID string, payload any, expectResponse bool) (mcpRPCResponse, string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return mcpRPCResponse{}, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return mcpRPCResponse{}, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
	req.Header.Set("User-Agent", "github.com/SuInk/diana/0.1")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}
	resp, err := t.httpClient().Do(req)
	if err != nil {
		return mcpRPCResponse{}, "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	responseSessionID := strings.TrimSpace(resp.Header.Get("Mcp-Session-Id"))
	if responseSessionID == "" {
		responseSessionID = sessionID
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8*1024))
		return mcpRPCResponse{}, responseSessionID, fmt.Errorf("remote service returned HTTP %d", resp.StatusCode)
	}
	if !expectResponse {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8*1024))
		return mcpRPCResponse{}, responseSessionID, nil
	}
	raw, err := readWebSearchBody(resp.Body)
	if err != nil {
		return mcpRPCResponse{}, responseSessionID, err
	}
	decoded, err := decodeMCPRPCResponse(raw, resp.Header.Get("Content-Type"))
	if err != nil {
		return mcpRPCResponse{}, responseSessionID, err
	}
	return decoded, responseSessionID, nil
}

func (t *WebSearchTool) closeRemoteMCPSession(endpoint, apiKey, sessionID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return
	}
	req.Header.Set("Mcp-Session-Id", sessionID)
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}
	resp, err := t.httpClient().Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

func decodeMCPRPCResponse(raw []byte, contentType string) (mcpRPCResponse, error) {
	if !strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		var response mcpRPCResponse
		if err := json.Unmarshal(bytes.TrimSpace(raw), &response); err != nil {
			return mcpRPCResponse{}, fmt.Errorf("invalid MCP JSON response: %w", err)
		}
		return response, nil
	}

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), maxWebSearchResponseBytes)
	var dataLines []string
	var lastErr error
	flush := func() (mcpRPCResponse, bool) {
		if len(dataLines) == 0 {
			return mcpRPCResponse{}, false
		}
		data := strings.TrimSpace(strings.Join(dataLines, "\n"))
		dataLines = nil
		if data == "" {
			return mcpRPCResponse{}, false
		}
		var response mcpRPCResponse
		if err := json.Unmarshal([]byte(data), &response); err != nil {
			lastErr = err
			return mcpRPCResponse{}, false
		}
		if len(response.Result) > 0 || response.Error != nil {
			return response, true
		}
		return mcpRPCResponse{}, false
	}
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			if response, ok := flush(); ok {
				return response, nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return mcpRPCResponse{}, err
	}
	if response, ok := flush(); ok {
		return response, nil
	}
	if lastErr != nil {
		return mcpRPCResponse{}, fmt.Errorf("invalid MCP event stream: %w", lastErr)
	}
	return mcpRPCResponse{}, errors.New("MCP event stream contained no response")
}

func extractMCPToolText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("MCP tool returned no result")
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("invalid MCP tool result: %w", err)
	}
	var parts []string
	for _, item := range result.Content {
		if item.Type == "text" && strings.TrimSpace(item.Text) != "" {
			parts = append(parts, strings.TrimSpace(item.Text))
		}
	}
	content := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if content == "" && len(result.StructuredContent) > 0 && string(result.StructuredContent) != "null" {
		content = strings.TrimSpace(string(result.StructuredContent))
	}
	if result.IsError {
		return "", fmt.Errorf("MCP tool reported an error: %s", content)
	}
	if content == "" {
		return "", errors.New("MCP tool returned empty content")
	}
	lower := strings.ToLower(content)
	if strings.Contains(lower, "no search results") || strings.Contains(lower, "no results found") {
		return "", fmt.Errorf("MCP tool returned no search results: %w", errWebSearchNoResults)
	}
	return content, nil
}

func (t *WebSearchTool) runTavily(ctx context.Context, provider webSearchProviderConfig, query, apiKey string) (string, error) {
	if apiKey == "" {
		return "", errors.New("Tavily API key is missing")
	}
	payload := map[string]any{
		"query":               query,
		"search_depth":        "basic",
		"max_results":         provider.MaxResults,
		"include_answer":      false,
		"include_raw_content": false,
		"include_images":      false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.URL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "github.com/SuInk/diana/0.1")
	resp, err := t.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := readWebSearchBody(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("remote service returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Answer  string `json:"answer"`
		Results []struct {
			Title         string  `json:"title"`
			URL           string  `json:"url"`
			Content       string  `json:"content"`
			Score         float64 `json:"score"`
			PublishedDate string  `json:"published_date"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("invalid Tavily response: %w", err)
	}
	if len(result.Results) == 0 {
		return "", fmt.Errorf("Tavily returned no search results: %w", errWebSearchNoResults)
	}
	normalized := map[string]any{"results": result.Results}
	if strings.TrimSpace(result.Answer) != "" {
		normalized["answer"] = result.Answer
	}
	formatted, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return "", err
	}
	return string(formatted), nil
}

func (t *WebSearchTool) formatExplorationResult(result webSearchResult) (string, error) {
	result.RetrievedAt = time.Now().UTC().Format(time.RFC3339)
	result.FreshnessVerified = false
	result.SourceNotice = "搜索结果可能来自转载或聚合页面，页面日期不一定是原始内容的真实发布时间。未核实原始来源时，不要据此断言发布时间或‘最新’。本次查询时间不是索引更新时间；搜索空结果不证明页面、版本或事件不存在。精确 URL 可能未收录，应直接读取官方页面或 API。"
	maxChars := t.maxBytes
	if maxChars <= 0 {
		maxChars = DefaultMaxToolOutputChars
	}

	contentRunes := []rune(strings.TrimSpace(result.Content))
	result.Content = ""
	best, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	// Preserve a valid JSON envelope. Shorten snippets before dropping hits;
	// query outcomes stay visible even when result text is truncated.
	for len([]rune(string(best))) > maxChars {
		result.Truncated = true
		changed := false
		for i := range result.Results {
			if len([]rune(result.Results[i].Snippet)) > 80 {
				result.Results[i].Snippet = string([]rune(result.Results[i].Snippet)[:79]) + "…"
				changed = true
			}
		}
		// Discovery hits are useful to the model; duplicated source lists and
		// provider diagnostics must not consume their output budget first.
		if !changed && len(result.Providers) > 0 {
			result.Providers = nil
			changed = true
		}
		if !changed && len(result.Attempts) > 0 {
			result.Attempts = nil
			changed = true
		}
		if !changed && len(result.Results) > 0 && len(result.Sources) > 0 {
			result.Sources = nil
			changed = true
		}
		if !changed && len(result.Results) > 1 {
			result.Results = result.Results[:len(result.Results)-1]
			changed = true
		}
		if !changed && len(result.Sources) > 1 {
			result.Sources = result.Sources[:len(result.Sources)-1]
			changed = true
		}
		if !changed && len(result.Queries) > 0 {
			result.Queries = nil
			changed = true
		}
		if !changed && len(result.Results) > 0 {
			result.Results = nil
			changed = true
		}
		if !changed && len(result.Sources) > 0 {
			result.Sources = nil
			changed = true
		}
		if !changed && result.SourceNotice != "" {
			result.SourceNotice = ""
			changed = true
		}
		if !changed && len(result.Searches) > 0 {
			result.Searches = result.Searches[:len(result.Searches)-1]
			changed = true
		}
		if !changed && result.SelectedQuery != "" {
			result.SelectedQuery = ""
			changed = true
		}
		if !changed {
			break
		}
		best, err = json.MarshalIndent(result, "", "  ")
		if err != nil {
			return "", err
		}
	}

	if len(contentRunes) == 0 || len([]rune(string(best))) >= maxChars {
		return string(best), nil
	}

	// JSON escaping can expand characters such as '<' to six bytes. Find the
	// largest content prefix that fits the final serialized output instead of
	// repeatedly estimating how much text to remove.
	low, high := 1, min(len(contentRunes), maxChars)
	for low <= high {
		mid := low + (high-low)/2
		result.Content = string(contentRunes[:mid])
		if mid < len(contentRunes) {
			result.Content += "\n...truncated..."
		}
		candidate, marshalErr := json.MarshalIndent(result, "", "  ")
		if marshalErr != nil {
			return "", marshalErr
		}
		if len([]rune(string(candidate))) <= maxChars {
			best = candidate
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return string(best), nil
}

func mergeWebSearchOutcome(current, next string) string {
	priority := map[string]int{
		"":                      0,
		"no_results":            1,
		"timeout":               2,
		"provider_error":        3,
		"insufficient_evidence": 4,
		"success":               5,
	}
	if priority[next] > priority[current] {
		return next
	}
	return current
}

func markWebSearchRemainder(queries []webSearchQueryCandidate, providers []webSearchProviderState, completedQueryIndex int, reason string) {
	for index := range queries {
		if queries[index].Status == "not_executed" {
			queries[index].Reason = reason
		}
		if completedQueryIndex >= 0 && index == completedQueryIndex && queries[index].Outcome == "" {
			queries[index].Outcome = "success"
		}
	}
	for index := range providers {
		if providers[index].Status == "not_executed" {
			providers[index].Reason = reason
		}
	}
}

func (t *WebSearchTool) httpClient() *http.Client {
	if t.client != nil {
		return t.client
	}
	return http.DefaultClient
}

func readWebSearchBody(reader io.Reader) ([]byte, error) {
	limited := io.LimitReader(reader, maxWebSearchResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(body) > maxWebSearchResponseBytes {
		return nil, errors.New("remote response exceeded size limit")
	}
	return body, nil
}

func safeWebSearchError(err error) string {
	if err == nil {
		return ""
	}
	text := webSearchURLPattern.ReplaceAllString(err.Error(), "[remote endpoint]")
	return truncateRunes(strings.TrimSpace(text), 300)
}
