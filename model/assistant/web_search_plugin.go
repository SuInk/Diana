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

	webSearchSettingExaEnabled      = "exa_enabled"
	webSearchSettingExaURL          = "exa_url"
	webSearchSettingExaAPIKey       = "exa_api_key"
	webSearchSettingTavilyEnabled   = "tavily_enabled"
	webSearchSettingTavilyURL       = "tavily_url"
	webSearchSettingTavilyAPIKey    = "tavily_api_key"
	webSearchSettingMaxResults      = "max_results"
	webSearchSettingProviderTimeout = "provider_timeout_seconds"
	webSearchSettingTotalTimeout    = "total_timeout_seconds"
	webSearchSettingEvidenceLedger  = "evidence_ledger_enforced"
	webSearchSettingSourceRecall    = "claim_source_recall"
	webSearchSettingLinkPolicy      = "reply_link_policy"

	replyLinkPolicyOnRequest = "on_request"
	replyLinkPolicyAlways    = "always"
	replyLinkPolicyNever     = "never"

	defaultExaSearchURL    = "https://mcp.exa.ai/mcp?tools=web_search_exa"
	defaultTavilySearchURL = "https://api.tavily.com/search"
)

type WebSearchPlugin struct {
	client *http.Client
}

type unavailableWebSearchTool struct{}

func (*unavailableWebSearchTool) Name() string { return agent.WebSearchToolName }
func (*unavailableWebSearchTool) Description() string {
	return `实时联网搜索。当前没有启用的搜索 Provider；调用后会返回明确配置诊断。input: {"query":"搜索内容"}`
}
func (*unavailableWebSearchTool) Run(context.Context, map[string]any) (string, error) {
	return "", fmt.Errorf("联网搜索工具已注册，但搜索插件或全部搜索 Provider 当前已停用或未配置")
}

func ensureWebSearchAgentTool(tools []agent.Tool) []agent.Tool {
	for _, tool := range tools {
		if tool != nil && tool.Name() == agent.WebSearchToolName {
			return tools
		}
	}
	return append(tools, &unavailableWebSearchTool{})
}

func NewWebSearchPlugin(client *http.Client) *WebSearchPlugin {
	return &WebSearchPlugin{client: client}
}

func (p *WebSearchPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:          webSearchPluginID,
		Name:        "联网搜索",
		Version:     "0.3.0",
		Description: "为对话提供带候选查询探索和空结果恢复的实时网页搜索。优先使用 Exa MCP，失败时自动回退到 Tavily。",
		Official:    true,
		BuiltIn:     true,
		Permissions: []string{"network:http", "llm:tool"},
		Settings: []PluginSettingSpec{
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
				Description: "一次搜索在所有来源之间回退时的总等待上限。",
				Type:        PluginSettingTypeNumber,
				Default:     35,
				Min:         settingRange(5),
				Max:         settingRange(90),
				Step:        1,
				Unit:        "秒",
			},
			{
				Key:         webSearchSettingEvidenceLedger,
				Label:       "证据账本强制校验",
				Description: "开启后联网研究必须为每条结论绑定已检索或已渲染的来源，绑不上就要求重写。关闭后仍然记录来源和结论，但不再拦截回复，适合以确定性查询为主的场景。",
				Type:        PluginSettingTypeBool,
				Default:     true,
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
	providers := make([]agent.WebSearchProviderConfig, 0, 2)
	apiKeys := map[string]string{}

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
	if len(providers) == 0 {
		return nil, nil
	}

	tool, err := agent.NewWebSearchTool(agent.WebSearchToolOptions{
		Config:         agent.WebSearchConfig{Providers: providers},
		APIKeys:        apiKeys,
		Timeout:        time.Duration(settings.Int(webSearchSettingTotalTimeout, 35)) * time.Second,
		MaxOutputChars: agent.DefaultMaxToolOutputChars,
		Client:         p.client,
	})
	if err != nil {
		return nil, err
	}
	return []agent.Tool{tool}, nil
}
