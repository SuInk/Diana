// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/SuInk/diana/model/agent"
)

const capabilityKnowledgePluginID = "official.capability-knowledge-rag"

const (
	// 检索条数上下限。schema 文案和下面的夹取引用同一份常量。
	defaultCapabilityResultLimit = 5
	maximumCapabilityResultLimit = 8
)

type CapabilityKnowledgePlugin struct {
	mu            sync.RWMutex
	stateProvider func() []PluginState
}

type dianaCapabilitiesTool struct {
	plugin *CapabilityKnowledgePlugin
}

type capabilityDocument struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Content  string `json:"content"`
	Source   string `json:"source"`
	Enabled  bool   `json:"enabled"`
	Required string `json:"required_relationship,omitempty"`
}

type capabilitySearchHit struct {
	capabilityDocument
	Score float64 `json:"score"`
}

func NewCapabilityKnowledgePlugin() *CapabilityKnowledgePlugin {
	return &CapabilityKnowledgePlugin{}
}

func (p *CapabilityKnowledgePlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:          capabilityKnowledgePluginID,
		Name:        "能力知识库",
		Version:     "0.1.1",
		Description: "索引 Diana 核心能力和实时插件清单，通过本地稀疏检索向 Agent 提供与问题相关的能力说明。",
		Official:    true,
		BuiltIn:     true,
		// 只对外提供一个 Agent 工具，Handle 不做事：它是否起作用完全由机器人
		// 配置里的「智能体」开关决定，再单独摆一个插件开关只是第二个语义相同
		// 的按钮。
		Internal:    true,
		Permissions: []string{"agent:tool", "knowledge:read", "plugin:list"},
	}
}

func (p *CapabilityKnowledgePlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

func (p *CapabilityKnowledgePlugin) AgentTools() []agent.Tool {
	return []agent.Tool{&dianaCapabilitiesTool{plugin: p}}
}

func (p *CapabilityKnowledgePlugin) setPluginStateProvider(provider func() []PluginState) {
	p.mu.Lock()
	p.stateProvider = provider
	p.mu.Unlock()
}

func (p *CapabilityKnowledgePlugin) documents() []capabilityDocument {
	documents := append([]capabilityDocument(nil), coreCapabilityDocuments...)
	p.mu.RLock()
	provider := p.stateProvider
	p.mu.RUnlock()
	if provider == nil {
		return documents
	}
	for _, state := range provider() {
		documents = append(documents, capabilityDocument{
			ID:      "plugin:" + state.Manifest.ID,
			Title:   state.Manifest.Name,
			Content: fmt.Sprintf("插件 %s，版本 %s。%s。权限：%s。安装=%t，启用=%t。", state.Manifest.ID, state.Manifest.Version, state.Manifest.Description, strings.Join(state.Manifest.Permissions, "、"), state.Installed, state.Enabled),
			Source:  "plugin_manifest",
			Enabled: state.Installed && state.Enabled,
		})
	}
	return documents
}

func (t *dianaCapabilitiesTool) Name() string {
	return "diana.capabilities"
}

func (t *dianaCapabilitiesTool) Description() string {
	return `从 Diana 自身能力知识库检索相关能力、工具、权限门槛和实时插件状态。用户问「你会什么」「能不能处理某事」「哪个插件负责某功能」或质疑机器人能力时必须先调用，不要凭提示词记忆猜测。`
}

func (t *dianaCapabilitiesTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"query"}, map[string]any{
		"query": toolStringParam("用户关于能力的问题，原样或稍加归纳后传入。"),
		"limit": toolIntParam("返回条数，默认 "+itoa(defaultCapabilityResultLimit)+"。", 1, maximumCapabilityResultLimit),
	})
}

func (t *dianaCapabilitiesTool) Run(_ context.Context, input map[string]any) (string, error) {
	if t == nil || t.plugin == nil {
		return "", fmt.Errorf("能力知识库未配置")
	}
	query := strings.TrimSpace(configToolString(input, "query"))
	if query == "" {
		return "", fmt.Errorf("query 不能为空")
	}
	limit := defaultCapabilityResultLimit
	if raw := strings.TrimSpace(configToolString(input, "limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > maximumCapabilityResultLimit {
		limit = maximumCapabilityResultLimit
	}
	hits := retrieveCapabilityDocuments(query, t.plugin.documents(), limit)
	body, err := json.MarshalIndent(map[string]any{
		"ok":      true,
		"action":  "retrieved",
		"query":   query,
		"message": fmt.Sprintf("能力知识库检索到 %d 条相关结果。请结合当前用户关系权限回答，不要把未解锁能力说成可直接使用。", len(hits)),
		"items":   hits,
	}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func retrieveCapabilityDocuments(query string, documents []capabilityDocument, limit int) []capabilitySearchHit {
	queryTerms := capabilityTerms(query)
	hits := make([]capabilitySearchHit, 0, len(documents))
	for _, document := range documents {
		titleTerms := capabilityTerms(document.Title)
		contentTerms := capabilityTerms(document.Content)
		score := 0.0
		for term, queryWeight := range queryTerms {
			if weight := titleTerms[term]; weight > 0 {
				score += queryWeight * weight * 3
			}
			if weight := contentTerms[term]; weight > 0 {
				score += queryWeight * weight
			}
		}
		if score > 0 {
			hits = append(hits, capabilitySearchHit{capabilityDocument: document, Score: score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ID < hits[j].ID
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

var capabilityASCIIToken = regexp.MustCompile(`[a-z0-9._:/-]+`)

func capabilityTerms(text string) map[string]float64 {
	text = strings.ToLower(text)
	terms := map[string]float64{}
	for _, token := range capabilityASCIIToken.FindAllString(text, -1) {
		if len(token) >= 2 {
			terms[token] = 2
		}
	}
	runes := []rune(text)
	for index, current := range runes {
		if !unicode.Is(unicode.Han, current) {
			continue
		}
		terms[string(current)] = 0.2
		if index+1 < len(runes) && unicode.Is(unicode.Han, runes[index+1]) {
			terms[string(runes[index:index+2])] = 1
		}
		if index+2 < len(runes) && unicode.Is(unicode.Han, runes[index+1]) && unicode.Is(unicode.Han, runes[index+2]) {
			terms[string(runes[index:index+3])] = 1.5
		}
	}
	return terms
}

var coreCapabilityDocuments = []capabilityDocument{
	{ID: "core:web-search", Title: "实时联网搜索", Content: "可使用 web_search.search 通过有预算的候选查询探索、多 provider 回退和空结果恢复检索实时新闻、IPO 时间、价格和网页资料；支持别名、语言及宽松查询候选，并会返回来源和证据状态供后续核验。", Source: "core", Enabled: true},
	{ID: "core:browser", Title: "网页浏览与渲染", Content: "可用沙盒无头浏览器执行 JavaScript、跟随跳转、读取动态网页；主人还可使用浏览器和本地工具。", Source: "core", Enabled: true},
	{ID: "core:media", Title: "图片视频与链接解析", Content: "能理解聊天图片上下文，下载并抽取视频多帧；链接解析插件支持 B站、YouTube、X、小红书、抖音等平台并发送解析结果。", Source: "core", Enabled: true},
	{ID: "core:image-source", Title: "图片溯源", Content: "可调用 diana.image_source 查聊天里图片的出处：SauceNAO 覆盖插画、同人志和表情包并给出 pixiv、Danbooru 等原链，trace.moe 认番剧截图并给出集数和出现时间。只能查聊天里已有的图片，反查会把图片上传到对应的第三方图库。", Source: "core", Enabled: true},
	{ID: "core:image", Title: "图片生成与编辑", Content: "熟悉等级可生成和编辑图片；可结合群成员头像、用户提供的图片以及 Agent 联网搜索或网页核验后的结果。", Source: "core", Enabled: true, Required: "熟悉"},
	{ID: "core:voice", Title: "配置音色语音回复", Content: "用户明确要求语音回复、朗读或念出文字时，可调用 diana.tts 通过语音合成插件生成已配置音色并直接发送 语音；普通文字回复不会自动转语音。", Source: "core", Enabled: true},
	{ID: "core:ocr", Title: "文件与 OCR", Content: "能解析 PDF 和文件；macOS 使用 PDFKit/Vision，本地原生路径不可用时回退 PDFium 与视觉 LLM。", Source: "core", Enabled: true, Required: "熟悉"},
	{ID: "core:group", Title: "群资料与成员", Content: "通过 diana.onebot_group 获取群名、群成员列表、成员昵称、群头像和成员头像，可查找并真实 @ 一名或多名成员。", Source: "core", Enabled: true},
	{ID: "core:onebot-v11", Title: "OneBot v11 协议技能", Content: "通过 diana.onebot_v11 调用当前连接的 OneBot v11 标准动作及实现扩展；主人拥有完整调用权限，普通成员只可调用后端固定的标准只读白名单，凭据、修改和未知动作默认拒绝。", Source: "core", Enabled: true},
	{ID: "core:runtime-model", Title: "自己在用什么模型", Content: "通过 diana.runtime_model 读出本轮实际生效的模型 ID、Provider 名称、接口协议和模型分组（对话/视觉理解等）；同一次对话里换了用途也会跟着变。它只读不改；主人要换模型时由 diana.llm_config 改模型分配里的对话、视觉理解、意图识别或图片生成四档之一。", Source: "core", Enabled: true},
	{ID: "core:version", Title: "自己的版本与更新状态", Content: "通过 diana.version 报出当前版本号、是正式发布版还是源码构建、这台机器上这个版本什么时候装上的、本次运行了多久、跑在什么系统架构上，以及项目的开源地址、最新发布版本、有没有新版本可用、这台机器能不能自更新。", Source: "core", Enabled: true},
	{ID: "core:notebook", Title: "笔记本与梗记忆", Content: "通过 diana.notebook 维护群里的梗、黑话、缩写和内部称呼：记下新说法、更新变了的释义、作废不再成立的条目，删错了还能恢复。当前消息里出现已收录的说法时，释义会自动进入回复上下文。", Source: "core", Enabled: true},
	{ID: "core:thread-state", Title: "私有多轮任务状态", Content: "通过 diana.thread_state 保存 Diana 自己为当前用户和会话创建的短期私有状态，例如猜谜目标、临时计划和表单进度。状态会跨消息和进程重启恢复，完成、取消或超时后清理，不写入用户长期记忆，也不会出现在公开回复和调试追踪里。", Source: "core", Enabled: true},
	{ID: "core:relationship", Title: "记忆好感度与权限", Content: "通过 diana.relationship 查询用户长期互动、好感度、关系等级和权限；主人可设置或增减其他人的好感度。", Source: "core", Enabled: true},
	{ID: "core:world-book", Title: "世界书世界观设定", Content: "世界书是主人在控制台维护的世界观设定集：条目按树状章节组织，常驻条目（蓝灯）每轮进入上下文，带触发词的条目（绿灯）在聊到相关话题时注入。支持直接导入 SillyTavern 世界书文件和角色卡内嵌的 character_book。它定义机器人所处的世界背景，由每台机器人的配置决定用不用；设定内容不会主动复述给用户。", Source: "core", Enabled: true},
	{ID: "core:romance", Title: "人机恋恋爱模式", Content: "主人在控制台开启恋爱模式后，用户本人认真表白时可通过 diana.relationship 的 romance_start 确立恋人关系，好感度和相处时长不够会被温柔婉拒；romance_end 随时可以分手。确立后语气按恋人来、记纪念日，但不解锁任何权限。开关默认关闭，关闭时机器人不参与恋爱话题的确立。", Source: "core", Enabled: true},
	{ID: "core:tasks", Title: "提醒与周期订阅", Content: "通过 diana.reminder、diana.schedule、diana.rss 和 diana.tasks 创建、查询、修改、取消和删除提醒、周期查询及 RSS/Twitter 条件订阅；GitHub 仓库更新订阅在 WebUI 管理。", Source: "core", Enabled: true},
	{ID: "core:history", Title: "聊天历史引用与撤回", Content: "持久保存 OneBot v11 消息、引用、图片和视频关键帧，重启后不丢；可读取合并转发和撤回记录并结合上下文回复。", Source: "core", Enabled: true},
	{ID: "core:config", Title: "机器人配置与模型配置", Content: "diana.config 可读取脱敏运行配置、LLM、plugins 和 skills；仅主人可用 diana.llm_config 修改 Diana 自己当前的 provider/model。", Source: "core", Enabled: true, Required: "主人"},
	{ID: "core:llm-identity-privacy", Title: "LLM 账号标识脱敏", Content: "默认在本地 LLM 边界把账号和群号替换为带角色语义的稳定别名；模型回复和 Agent 工具参数会在本地执行前还原。真实标识仍保留在本地数据库，不影响消息发送、群工具和长期记忆。", Source: "core", Enabled: true},
	{ID: "core:capabilities", Title: "自身能力知识库 RAG", Content: "diana.capabilities 使用本地稀疏检索，从核心能力和实时插件清单召回相关条目后交给模型回答。", Source: "core", Enabled: true},
}
