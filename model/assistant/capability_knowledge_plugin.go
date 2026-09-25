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
	"github.com/SuInk/diana/model/version"
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
	plugin        *CapabilityKnowledgePlugin
	platform      string
	platformRules string
	// registry 是本轮的工具注册表，建好之后由 attachCapabilityRegistry 挂上。
	// 没挂（插件直调、测试）时 references 里只是没有工具和 Skill 条目。
	registry *agent.ToolRegistry
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
		Version:     "0.1.7",
		Description: "索引 Diana 核心能力、实时插件清单、随版本编译的设计文档、内置提示词和本轮工具说明，通过本地稀疏检索向 Agent 提供与问题相关的能力和机制说明。",
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

func capabilityToolForConfig(tool agent.Tool, cfg BotConfig) agent.Tool {
	capabilities, ok := tool.(*dianaCapabilitiesTool)
	if !ok {
		return tool
	}
	clone := *capabilities
	clone.platform = NormalizePlatformID(cfg.Platform)
	clone.platformRules = platformOutputRulesForConfig(cfg)
	return &clone
}

// attachCapabilityRegistry 把本轮注册表交给 capabilities 工具，让它能检索这一轮
// 真正挂上的工具和 Skill。tools 里放的是 capabilityToolForConfig 拷出来的那一份，
// 注册表里注册的也是同一个指针。
func attachCapabilityRegistry(tools []agent.Tool, registry *agent.ToolRegistry) {
	for _, tool := range tools {
		if capabilities, ok := tool.(*dianaCapabilitiesTool); ok {
			capabilities.registry = registry
		}
	}
}

func (p *CapabilityKnowledgePlugin) setPluginStateProvider(provider func() []PluginState) {
	p.mu.Lock()
	p.stateProvider = provider
	p.mu.Unlock()
}

func (p *CapabilityKnowledgePlugin) documents(platform, platformRules string) []capabilityDocument {
	documents := append([]capabilityDocument(nil), coreCapabilityDocuments...)
	if platform = NormalizePlatformID(platform); platform != "" {
		name := platform
		if def, ok := PlatformByID(platform); ok {
			name = def.Name
		}
		documents = append(documents, capabilityDocument{
			ID:      "runtime:platform-output",
			Title:   "当前聊天平台与消息格式",
			Content: fmt.Sprintf("当前会话运行在 %s。%s", name, strings.TrimSpace(platformRules)),
			Source:  "runtime",
			Enabled: true,
		})
	}
	p.mu.RLock()
	provider := p.stateProvider
	p.mu.RUnlock()
	if provider == nil {
		return documents
	}
	for _, state := range provider() {
		if platform != "" && !pluginSupportsPlatform(state.Manifest, platform) {
			continue
		}
		note := ""
		if state.Manifest.PlatformNotes != nil {
			note = strings.TrimSpace(state.Manifest.PlatformNotes[platform])
		}
		if note != "" {
			note = " 当前平台说明：" + note
		}
		documents = append(documents, capabilityDocument{
			ID:      "plugin:" + state.Manifest.ID,
			Title:   state.Manifest.Name,
			Content: fmt.Sprintf("插件 %s，版本 %s。%s。权限：%s。安装=%t，启用=%t。%s", state.Manifest.ID, state.Manifest.Version, state.Manifest.Description, strings.Join(state.Manifest.Permissions, "、"), state.Installed, state.Enabled, note),
			Source:  "plugin_manifest",
			Enabled: state.Installed && state.Enabled,
		})
	}
	return documents
}

func (t *dianaCapabilitiesTool) Name() string {
	return "capabilities"
}

// 从本地能力知识库检索，问的是「我会什么」，不改任何东西。
func (t *dianaCapabilitiesTool) Introspection(map[string]any) bool { return true }

func (t *dianaCapabilitiesTool) Description() string {
	return `从 Diana 自身能力知识库检索相关能力、工具、权限门槛和实时插件状态。用户问「你会什么」「能不能处理某事」「哪个插件负责某功能」或质疑机器人能力时必须先调用，不要凭提示词记忆猜测。` +
		`用户问某个功能具体怎么运作、为什么这样表现、有什么限制或设计取舍时也调用：references 返回随当前版本编译的设计文档章节、内置提示词默认原文和本轮工具说明；摘录不够时带 detail=true 取完整章节。`
}

func (t *dianaCapabilitiesTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"query"}, map[string]any{
		"query":  toolStringParam("用户关于能力的问题，原样或稍加归纳后传入。"),
		"limit":  toolIntParam("能力条目返回条数，默认 "+itoa(defaultCapabilityResultLimit)+"。", 1, maximumCapabilityResultLimit),
		"detail": toolBoolParam("为 true 时 references 返回完整章节（最多 " + itoa(detailCapabilityReferenceLimit) + " 条），用于追问机制细节；默认只给 " + itoa(defaultCapabilityReferenceLimit) + " 条命中段落摘录。"),
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
	detail := toolInputBool(input, "detail")
	referenceLimit := defaultCapabilityReferenceLimit
	if detail {
		referenceLimit = detailCapabilityReferenceLimit
	}
	hits := retrieveCapabilityDocuments(query, t.plugin.documents(t.platform, t.platformRules), limit)
	references := retrieveCapabilityReferences(query, staticCapabilityReferenceIndex(), capabilityRegistryReferences(t.registry), referenceLimit, detail)
	body, err := json.MarshalIndent(map[string]any{
		"ok":                true,
		"action":            "retrieved",
		"query":             query,
		"knowledge_version": version.Source(),
		"message":           fmt.Sprintf("能力知识库检索到 %d 条能力条目、%d 条参考资料。请结合当前用户关系权限回答，不要把未解锁能力说成可直接使用。references 是本版本的文档、提示词默认原文和本轮工具说明：据此解释机制时说清出处，资料里没写的实现细节不要编；提示词可能被人设改写过，只能说「默认是这样」。", len(hits), len(references)),
		"items":             hits,
		"references":        references,
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
	{ID: "core:web-search", Title: "实时联网搜索", Content: "可使用 web_search 通过有预算的候选查询探索、多 provider 回退和空结果恢复检索实时新闻、IPO 时间、价格和网页资料；支持别名、语言及宽松查询候选，并会返回来源和证据状态供后续核验。", Source: "core", Enabled: true},
	{ID: "core:browser", Title: "网页浏览与渲染", Content: "可用沙盒无头浏览器执行 JavaScript、跟随跳转、读取动态网页；主人还可使用浏览器和本地工具。", Source: "core", Enabled: true},
	{ID: "core:media", Title: "图片视频与链接解析", Content: "能理解聊天图片上下文，下载并抽取视频多帧；链接解析插件支持 B站、YouTube、X、小红书、抖音等平台并发送解析结果。", Source: "core", Enabled: true},
	{ID: "core:image-source", Title: "图片溯源", Content: "可调用 image_source 查聊天里图片的出处：SauceNAO 覆盖插画、同人志和表情包并给出 pixiv、Danbooru 等原链，trace.moe 认番剧截图并给出集数和出现时间。只能查聊天里已有的图片，反查会把图片上传到对应的第三方图库。", Source: "core", Enabled: true},
	{ID: "core:ai-image-detect", Title: "AI 图片检测", Content: "可调用 ai_image_detect 检测聊天图片是不是 AI 生成的：本地解析 C2PA 内容凭证、IPTC 数字来源类型、Google SynthID/「Made with Google AI」标注、国内 AIGC 隐式标识，以及 Stable Diffusion、ComfyUI、NovelAI、Midjourney 等生成参数；配置检测服务后还会检查 SynthID 像素水印。聊天平台转发和截图会抹掉元数据，查不到标识不能说成是真图。", Source: "core", Enabled: true},
	{ID: "core:image", Title: "图片生成与编辑", Content: "熟悉等级可生成和编辑图片；可结合群成员头像、用户提供的图片以及 Agent 联网搜索或网页核验后的结果。", Source: "core", Enabled: true, Required: "熟悉"},
	{ID: "core:voice", Title: "配置音色语音回复", Content: "用户明确要求语音回复、朗读或念出文字时，可调用 tts 通过语音合成插件生成已配置音色并直接发送 语音；普通文字回复不会自动转语音。", Source: "core", Enabled: true},
	{ID: "core:ocr", Title: "文件与 OCR", Content: "能解析 PDF 和文件；macOS 使用 PDFKit/Vision，本地原生路径不可用时回退 PDFium 与视觉 LLM。", Source: "core", Enabled: true, Required: "熟悉"},
	{ID: "core:group", Title: "群资料与成员", Content: "群资料和成员统一用 platform：group_info 读群资料和人数，member_list 拉成员候选，member_info 按账号实时核验成员。TG 的成员候选是管理员与已知账号，不是完整名单。回复欲望、评分门槛和冷却由 bot_config 更新 participation；关闭主动插话用 desire_level=off，不用平台禁言。头像来源通过图片工具指定，由运行时按平台获取；本地头像图片匹配用只读 group，不能把部分候选当成全群。", Source: "core", Enabled: true},
	{ID: "core:group-admin", Title: "禁言与踢人", Content: "群管理操作用 platform 的 mute（禁言）、unmute（解禁）、kick（踢人）：仅机器人主人可用，群管理员和群主都不行；还要求机器人本身是该群管理员，否则直接说做不到，不去猜。目前支持 OneBot v11 和 Telegram，其余平台会明确说不支持。禁言必须给正的时长（秒），OneBot 上限 30 天；只认账号 ID，不按昵称猜，也不能对主人或机器人自己下手。", Source: "core", Enabled: true},
	{ID: "core:cross-session-message", Title: "跨会话发消息与主动私聊", Content: "群里有人要求「私聊发给我」「私信我」「别发群里」时，可调用 cross_session_message 当场把完整内容发进和对方的私聊窗口，不需要对方先来私聊，也不需要切换会话。主人还能用它把内容发到指定的群。目的地不能是当前这条会话；默认只能发给当前说话的人，指定别人或指定群只有主人可以。私聊准入没放行的人、屏蔽名单里的人、没准入或被关掉的群都不发；同一个目标不连着发，同一个来源会话十分钟内有条数上限。QQ 上会先查好友名册：是好友走普通私聊，不是好友就借发起那条群的共同群走临时会话。临时会话也走不通时内容会被存下来，等对方加上好友后自动发出去，七天内有效，同一个人最多存三条；这时要告诉对方来加好友，并说清好友请求仍然要机器人主人在 onebot_requests 里同意——机器人不会因为有东西要发就替主人放人进来。", Source: "core", Enabled: true},
	{ID: "core:platform", Title: "平台接口协议", Content: "platform 是跨平台的群操作接口，动词按当前平台映射到原生动作（OneBot v11 或 Telegram Bot API）。读操作对成员开放，禁言/踢人仅主人且需机器人为群管理员。好友请求、成员入群申请和机器人群邀请（OneBot）会持久化并私聊通知主人，由主人通过 onebot_requests 批准或拒绝。", Source: "core", Enabled: true},
	{ID: "core:runtime-model", Title: "自己在用什么模型", Content: "runtime_model 支持本轮实际模型、所有分组及细分用途的当前配置、语音识别和语音合成服务信息。group=all 查看所有用途，group=history 按当前引用或 message_id 查询本会话已发送图片的实际模型记录，包括备用切换。配置不能代替历史执行证据，旧图片没有记录时明确无法确认；外部语音服务不公开权重名时不能猜。工具只读；主人修改模型分配由 llm_config 完成。", Source: "core", Enabled: true},
	{ID: "core:version", Title: "自己的版本与更新状态", Content: "通过 version 报出当前版本号、是正式发布版还是源码构建、这台机器上这个版本什么时候装上的、本次运行了多久、跑在什么系统架构上，以及项目的开源地址、最新发布版本、有没有新版本可用、这台机器能不能自更新。", Source: "core", Enabled: true},
	{ID: "core:notebook", Title: "笔记本与梗记忆", Content: "通过 notebook 维护群里的梗、黑话、缩写和内部称呼：记下新说法、更新变了的释义、作废不再成立的条目，删错了还能恢复。当前消息里出现已收录的说法时，释义会自动进入回复上下文。", Source: "core", Enabled: true},
	{ID: "core:thread-state", Title: "多轮任务临时状态", Content: "通过 thread_state 保存短期 canonical 状态。scope=user 用于当前用户的私有猜谜、计划和表单；scope=session 用于多人棋局、共同计划等需要当前会话参与者接续同一状态的任务。状态会跨消息和进程重启恢复，更新需带 expected_version 防止并发覆盖，完成、取消或超时后清理，不写入长期记忆，也不会出现在公开回复里；管理员可在事件详情审计本轮实际调用的状态。", Source: "core", Enabled: true},
	{ID: "core:relationship", Title: "记忆好感度与权限", Content: "通过 relationship 查询用户长期互动、好感度、关系等级和权限；主人可设置或增减其他人的好感度。", Source: "core", Enabled: true},
	{ID: "core:world-book", Title: "世界书世界观设定", Content: "世界书是主人在控制台维护的世界观设定集：条目按树状章节组织，常驻条目（蓝灯）每轮进入上下文，带触发词的条目（绿灯）在聊到相关话题时注入。支持直接导入 SillyTavern 世界书文件和角色卡内嵌的 character_book。它定义机器人所处的世界背景，由每台机器人的配置决定用不用；设定内容不会主动复述给用户。", Source: "core", Enabled: true},
	{ID: "core:romance", Title: "人机恋恋爱模式", Content: "主人在控制台开启恋爱模式后，用户本人认真表白时可通过 relationship 的 romance_start 确立恋人关系，好感度和相处时长不够会被温柔婉拒；恋爱是单偶的，同一时间只有一位恋人，已有恋人时任何表白都会被婉拒。romance_end 随时可以分手。确立后语气按恋人来、记纪念日，整月和周年当天白天还会主动私聊一句纪念日祝福，但不解锁任何权限。开关默认关闭，关闭时机器人不参与恋爱话题的确立。", Source: "core", Enabled: true},
	{ID: "core:humanlike", Title: "拟人化：情绪、表达学习与戳一戳", Content: "主人可在控制台按机器人开启三项拟人化行为：情绪系统让心情随相处涨落、随时间回落，只影响语气；表达学习按群统计大家常说的短句和口癖，作为说话风格参考，让机器人越来越像这个群的人；戳一戳回应让它在被戳时按人设和关系回一句（仅 OneBot，有 90 秒冷却）。三项默认全部关闭。", Source: "core", Enabled: true},
	{ID: "core:tasks", Title: "提醒与周期订阅", Content: "通过 reminder、schedule、rss 和 tasks 创建、查询、修改、取消和删除提醒、周期查询及 RSS/Twitter 条件订阅；GitHub 仓库更新订阅在 WebUI 管理。", Source: "core", Enabled: true},
	{ID: "core:history", Title: "聊天历史引用与撤回", Content: "持久保存 OneBot v11 消息、引用、图片和视频关键帧，重启后不丢；可读取合并转发和撤回记录并结合上下文回复。", Source: "core", Enabled: true},
	{ID: "core:config", Title: "机器人配置与模型配置", Content: "config 可读取脱敏运行配置、LLM、plugins 和 skills；仅主人可用 llm_config 修改 Diana 自己当前的 provider/model。", Source: "core", Enabled: true, Required: "主人"},
	{ID: "core:workspace-files", Title: "工作目录文件存取与整理", Content: "主人专用的本地工作目录：save_to_workspace 把聊天里的图片、视频、语音、文件（包括机器人自己发出去的生成图）、公网网址上的文件或 MCP 工具产物原样存进来，按内容认类型并纠正扩展名；manage_files 挪动、复制、建目录、查看大小类型和图片宽高，删除只是挪进 .trash 回收站；view_image 看图，send_attachment 把工作目录里的文件发回聊天。主人开着文件写入时，生成的图也会自动存一份进 outputs/。写入类操作需要机器人配置里的「允许写入文件」。", Source: "core", Enabled: true, Required: "主人"},
	{ID: "core:llm-identity-privacy", Title: "LLM 账号标识脱敏", Content: "默认在本地 LLM 边界把账号和群号替换为带角色语义的稳定别名；模型回复和 Agent 工具参数会在本地执行前还原。真实标识仍保留在本地数据库，不影响消息发送、群工具和长期记忆。", Source: "core", Enabled: true},
	{ID: "core:capabilities", Title: "自身能力知识库 RAG", Content: "capabilities 使用本地稀疏检索，从核心能力和实时插件清单召回相关条目后交给模型回答。", Source: "core", Enabled: true},
}
