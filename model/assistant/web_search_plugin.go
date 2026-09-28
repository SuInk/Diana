// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
)

const (
	webSearchPluginID = "official.web-search"

	webSearchSettingMode            = "search_mode"
	webSearchSettingEngines         = "search_engines"
	webSearchSettingExaEnabled      = "exa_enabled"
	webSearchSettingExaURL          = "exa_url"
	webSearchSettingExaAPIKey       = "exa_api_key"
	webSearchSettingTavilyEnabled   = "tavily_enabled"
	webSearchSettingTavilyURL       = "tavily_url"
	webSearchSettingTavilyAPIKey    = "tavily_api_key"
	webSearchSettingMaxResults      = "max_results"
	webSearchSettingProviderTimeout = "provider_timeout_seconds"
	webSearchSettingTotalTimeout    = "total_timeout_seconds"
	webSearchSettingSourceRecall    = "claim_source_recall"
	webSearchSettingLinkPolicy      = "reply_link_policy"

	webSearchModeAPI    = "api"
	webSearchModeEngine = "search_engine"
	// 搜索引擎模式每家要起一次浏览器，一家被拦再换下一家，35 秒的默认总超时只够试一家半。
	minSearchEngineTotalTimeout = 60 * time.Second

	replyLinkPolicyOnRequest = "on_request"
	replyLinkPolicyAlways    = "always"
	replyLinkPolicyNever     = "never"

	defaultExaSearchURL    = "https://mcp.exa.ai/mcp?tools=web_search_exa"
	defaultTavilySearchURL = "https://api.tavily.com/search"
)

type WebSearchPlugin struct {
	client *http.Client
	// renderer 只给测试替换浏览器用；为空时按需起一次性沙盒浏览器。
	renderer agent.PageRenderer
}

func NewWebSearchPlugin(client *http.Client) *WebSearchPlugin {
	return &WebSearchPlugin{client: client}
}

func (p *WebSearchPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:          webSearchPluginID,
		Name:        "联网搜索",
		Version:     "0.3.2",
		Description: "为对话提供带候选查询探索和空结果恢复的实时网页搜索。可以走搜索 API（优先 Exa MCP，失败回退 Tavily），也可以用沙盒浏览器直接打开搜索引擎的结果页。",
		Official:    true,
		BuiltIn:     true,
		Permissions: []string{"network:http", "llm:tool"},
		Settings: []PluginSettingSpec{
			{
				Key:   webSearchSettingMode,
				Label: "搜索方式",
				Description: "API 搜索调用 Exa / Tavily，快、结果带摘要；搜索引擎用「网页渲染」的一次性沙盒浏览器打开 Google、Bing 等的结果页，不需要 API Key，" +
					"但每次要起浏览器、慢一些，遇到人机验证会自动换下一家。两种方式对模型都是同一个 web_search 工具。",
				Type:    PluginSettingTypeSelect,
				Default: webSearchModeAPI,
				Options: []PluginSettingOption{
					{Value: webSearchModeAPI, Label: "API 搜索（Exa / Tavily）"},
					{Value: webSearchModeEngine, Label: "搜索引擎（沙盒浏览器）"},
				},
			},
			{
				Key:   webSearchSettingEngines,
				Label: "搜索引擎顺序",
				Description: "搜索引擎方式下按顺序尝试，逗号或换行分隔，前一家被人机验证拦下或没有结果时换下一家。内置 google、bing、duckduckgo、baidu；" +
					"也可以直接写自定义搜索地址，用 {query} 代表查询词，例如 https://search.example.com/search?q={query}，和内置引擎一起排序。",
				Type:    PluginSettingTypeString,
				Default: strings.Join(agent.DefaultSearchEngines, ","),
			},
			{
				Key:         webSearchSettingExaEnabled,
				Label:       "启用 Exa",
				Description: "作为首选搜索源；默认公共 MCP 地址无需密钥。",
				Type:        PluginSettingTypeBool,
				Default:     true,
			},
			{
				Key:         webSearchSettingExaURL,
				Label:       "Exa MCP 地址",
				Description: "仅允许 HTTPS 地址，或本机调试用的 localhost HTTP 地址。",
				Type:        PluginSettingTypeString,
				Default:     defaultExaSearchURL,
			},
			{
				Key:         webSearchSettingExaAPIKey,
				Label:       "Exa API Key",
				Description: "可选。使用需要鉴权的 Exa MCP 服务时填写。",
				Type:        PluginSettingTypeString,
				Default:     "",
				Secret:      true,
			},
			{
				Key:         webSearchSettingTavilyEnabled,
				Label:       "启用 Tavily 回退",
				Description: "Exa 超时、限流或无结果时尝试 Tavily；需要配置 API Key。",
				Type:        PluginSettingTypeBool,
				Default:     true,
			},
			{
				Key:         webSearchSettingTavilyURL,
				Label:       "Tavily API 地址",
				Description: "仅允许 HTTPS 地址，或本机调试用的 localhost HTTP 地址。",
				Type:        PluginSettingTypeString,
				Default:     defaultTavilySearchURL,
			},
			{
				Key:         webSearchSettingTavilyAPIKey,
				Label:       "Tavily API Key",
				Description: "回退搜索凭据；保存后不会在接口或页面中回显。",
				Type:        PluginSettingTypeString,
				Default:     "",
				Secret:      true,
			},
			{
				Key:         webSearchSettingMaxResults,
				Label:       "每次结果上限",
				Description: "每个搜索源最多返回的结果数量。",
				Type:        PluginSettingTypeNumber,
				Default:     5,
				Min:         settingRange(1),
				Max:         settingRange(10),
				Step:        1,
				Unit:        "条",
			},
			{
				Key:         webSearchSettingProviderTimeout,
				Label:       "单搜索源超时",
				Description: "单个搜索源失败后切换到下一个来源的等待上限。",
				Type:        PluginSettingTypeNumber,
				Default:     12,
				Min:         settingRange(2),
				Max:         settingRange(30),
				Step:        1,
				Unit:        "秒",
			},
			{
				Key:         webSearchSettingTotalTimeout,
				Label:       "总搜索超时",
				Description: "一次搜索在所有来源之间回退时的总等待上限。搜索引擎方式下不低于 60 秒。",
				Type:        PluginSettingTypeNumber,
				Default:     35,
				Min:         settingRange(5),
				Max:         settingRange(90),
				Step:        1,
				Unit:        "秒",
			},
			{
				Key:         webSearchSettingSourceRecall,
				Label:       "来源链接回溯",
				Description: "把结论引用过的来源链接留到之后几轮，有人追问「链接呢」时可以原样给出。关闭后不再记录也不再注入，追问链接需要重新检索。",
				Type:        PluginSettingTypeBool,
				Default:     true,
			},
			{
				Key:   webSearchSettingLinkPolicy,
				Label: "回复附带链接",
				// 默认档不注入任何规则，完全交给人设文本，避免和已有人设互相打架。
				Description: "决定联网结论要不要在回复里直接给出 URL。「跟随人设」不额外注入规则，沿用系统提示词里的写法；另外两档会注入明确规则并覆盖人设的相应说法。",
				Type:        PluginSettingTypeSelect,
				Default:     replyLinkPolicyOnRequest,
				Options: []PluginSettingOption{
					{Value: replyLinkPolicyOnRequest, Label: "跟随人设，追问再给（默认）"},
					{Value: replyLinkPolicyAlways, Label: "有可靠来源时随回复附一条"},
					{Value: replyLinkPolicyNever, Label: "从不给出链接"},
				},
			},
		},
	}
}

func (p *WebSearchPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

func (p *WebSearchPlugin) AgentTools(settings SettingValues) ([]agent.Tool, error) {
	providerTimeout := settings.Int(webSearchSettingProviderTimeout, 12) * int(time.Second/time.Millisecond)
	maxResults := settings.Int(webSearchSettingMaxResults, 5)
	totalTimeout := time.Duration(settings.Int(webSearchSettingTotalTimeout, 35)) * time.Second
	var providers []agent.WebSearchProviderConfig
	apiKeys := map[string]string{}
	var renderer agent.PageRenderer

	if strings.TrimSpace(settings.String(webSearchSettingMode, webSearchModeAPI)) == webSearchModeEngine {
		// 单源超时不沿用 API 那档：起一次浏览器加渲染就要十来秒，12 秒会把能用的引擎也掐掉。
		for index, engine := range webSearchEngineOrder(settings.String(webSearchSettingEngines, "")) {
			provider := agent.WebSearchProviderConfig{
				Name:       engine,
				Type:       agent.WebSearchProviderSearchEngine,
				Tool:       engine,
				MaxResults: maxResults,
			}
			if agent.CustomSearchEngineURL(engine) {
				provider.Name = fmt.Sprintf("custom-%d", index+1)
				provider.Tool = ""
				provider.URL = engine
			}
			providers = append(providers, provider)
		}
		totalTimeout = max(totalTimeout, minSearchEngineTotalTimeout)
		renderer = p.renderer
		if renderer == nil {
			renderer = agent.NewSandboxedHeadlessBrowser(agent.SandboxedBrowserConfig{Window: agent.BrowserWindowHidden})
		}
	} else {
		if settings.Bool(webSearchSettingExaEnabled, true) {
			providers = append(providers, agent.WebSearchProviderConfig{
				Name:       "exa",
				Type:       "exa_mcp",
				URL:        settings.String(webSearchSettingExaURL, defaultExaSearchURL),
				Tool:       "web_search_exa",
				TimeoutMS:  providerTimeout,
				MaxResults: maxResults,
			})
			if key := strings.TrimSpace(settings.String(webSearchSettingExaAPIKey, "")); key != "" {
				apiKeys["exa"] = key
			}
		}
		if settings.Bool(webSearchSettingTavilyEnabled, true) {
			providers = append(providers, agent.WebSearchProviderConfig{
				Name:       "tavily",
				Type:       "tavily",
				URL:        settings.String(webSearchSettingTavilyURL, defaultTavilySearchURL),
				TimeoutMS:  providerTimeout,
				MaxResults: maxResults,
			})
			if key := strings.TrimSpace(settings.String(webSearchSettingTavilyAPIKey, "")); key != "" {
				apiKeys["tavily"] = key
			}
		}
	}
	if len(providers) == 0 {
		return nil, nil
	}

	tool, err := agent.NewWebSearchTool(agent.WebSearchToolOptions{
		Config:         agent.WebSearchConfig{Providers: providers},
		APIKeys:        apiKeys,
		Timeout:        totalTimeout,
		MaxOutputChars: agent.DefaultMaxToolOutputChars,
		Client:         p.client,
		Renderer:       renderer,
	})
	if err != nil {
		return nil, err
	}
	return []agent.Tool{tool}, nil
}

// webSearchEngineOrder 解析「搜索引擎顺序」：内置引擎名不分大小写，自定义地址原样保留；
// 去掉不认识的和重复的，一个都不剩就用默认顺序。
func webSearchEngineOrder(raw string) []string {
	var engines []string
	seen := map[string]bool{}
	for _, item := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '，' || r == '、' || r == '\n' || r == ' ' || r == '\t'
	}) {
		name := strings.TrimSpace(item)
		if !agent.CustomSearchEngineURL(name) {
			name = strings.ToLower(name)
			if !agent.KnownSearchEngine(name) {
				continue
			}
		}
		if !seen[name] {
			seen[name] = true
			engines = append(engines, name)
		}
	}
	if len(engines) == 0 {
		return append([]string(nil), agent.DefaultSearchEngines...)
	}
	return engines
}
