// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strconv"
	"strings"

	"github.com/SuInk/diana/model/agent"
)

// RelationshipOwnerRole 是主人在数据里的标识。它是身份，不是关系等级——等级已删，
// 这个值留下来只为让提示词和日志有一个统一的词，且刻意叫 bot_owner 而不是 owner：
// 群成员角色里的 owner 指群主，两个词撞在一起时模型会把机器人的主人当成群主。
const RelationshipOwnerRole = "bot_owner"

type RelationshipPolicy struct {
	Tone         string `json:"tone"`
	Score        int    `json:"score"`
	MessageCount int    `json:"message_count"`
	Owner        bool   `json:"bot_owner"`
	// Admin 是主人指定的管理员：工具和管理权限按主人装配（见 toolScope），语气、
	// 好感度和额度仍按普通用户算。
	Admin                 bool `json:"bot_admin,omitempty"`
	AllowImageGeneration  bool `json:"allow_image_generation"`
	AllowImageEditing     bool `json:"allow_image_editing"`
	AllowDocumentOCR      bool `json:"allow_document_ocr"`
	AllowPersonalSchedule bool `json:"allow_personal_schedule"`
}

// 五个非主人等级的能力完全一样——聊天、媒体理解、搜索与沙盒渲染、生图与修图、
// 文档 OCR、OneBot 读取一律开放，随好感度变化的只有个人提醒与订阅额度
// （见 personalScheduleLimit）。所以这里不再维护一份「本等级授权能力」清单：
// 那份清单曾经每级各写一遍、措辞还不统一，被灌进提示词后就成了一串看着像特权、
// 其实人人都有的条目。能力问题由 capabilities 回答，能力管控走 Allow*
// 与 allowedAgentToolNames。

// 好感度分档已经删除。语气不再由五个硬编码档位决定，而是把分数原样交给模型，让它
// 自己拿捏亲疏——档位名（初识／熟悉／朋友／信赖／冷淡）既进提示词又进工具返回，
// 模型会把它当成身份标签复述出来，而它表达的信息还不如那个数字本身准确。
//
// 能力一律不随好感度开关，这条原则不变：Allow* 五个权限位在任何分数下都为真，
// promptRelationshipTierRules 里「不得以好感度不足为由拒绝任何普通能力」照旧。
// 负分的惩罚落在「愿不愿意主动搭理」上，不落在「能不能用」上——见 favorabilityStance。

// favorabilityColdThreshold 以下视为关系已经变差：不再主动接话，只在被直接呼叫时回。
const favorabilityColdThreshold = 0

// favorabilityDistantThreshold 以下进一步收敛：只回必要内容，不主动展开。
const favorabilityDistantThreshold = -50

// favorabilityStance 把好感度换算成一句语气指引。
//
// 刻意写成连续描述而不是档位名：模型拿到的是「当前好感度 -35」加一句怎么拿捏，
// 而不是「关系等级：冷淡」这种可以被当成称号复述的标签。
func favorabilityStance(score int, owner bool) string {
	switch {
	case owner:
		return "亲近、坦率、执行导向；可以自然接梗，但涉及风险和失败时必须如实说明。"
	case score <= favorabilityDistantThreshold:
		return "这个人过去的言行让关系明显变差：保持礼貌，只回答被直接问到的必要内容，不主动展开、不主动搭话、不讨好也不争吵。"
	case score < favorabilityColdThreshold:
		return "关系目前是负的：礼貌但疏离，只回应直接冲着你来的话，不主动接话题，面对冒犯可以设边界。"
	case score >= 100:
		return "像长期信赖的朋友一样直接、温和、有默契，可以主动结合已知偏好，但不要编造共同经历。"
	case score >= 60:
		return "像熟悉的朋友一样温暖、轻松，可以适度接梗和调侃，仍要尊重边界。"
	case score >= 20:
		return "比刚认识时放松，可以自然使用对方昵称并结合长期偏好，但不要过分亲密。"
	default:
		return "自然随和，像刚认识但好相处的群友；不用敬语和客服腔，也不要假装已经很熟或用过度亲密的称呼。"
	}
}

func RelationshipPolicyFor(profile UserMemoryProfile, ownerID, userID string) RelationshipPolicy {
	ownerID = strings.TrimSpace(ownerID)
	userID = strings.TrimSpace(userID)
	owner := ownerID != "" && ownerID == userID
	return RelationshipPolicy{
		Score:        profile.Favorability,
		MessageCount: profile.MessageCount,
		Owner:        owner,
		Tone:         favorabilityStance(profile.Favorability, owner),
		// 能力不随好感度变化，五个权限位恒为真。
		AllowImageGeneration:  true,
		AllowImageEditing:     true,
		AllowDocumentOCR:      true,
		AllowPersonalSchedule: true,
	}
}

// Manages 报告这位发言者有没有管理权限：主人或管理员。
func (p RelationshipPolicy) Manages() bool { return p.Owner || p.Admin }

// toolScope 是装配工具时用的策略：管理员视同主人，拿到同一套工具和管理入口。
// 只用在工具面和工具用法提示上；身份那一行、语气、好感度和额度都用原策略，
// 管理员不会因此被当成主人称呼，也不多拿提醒额度。
func (p RelationshipPolicy) toolScope() RelationshipPolicy {
	if p.Admin {
		p.Owner = true
	}
	return p
}

func (p RelationshipPolicy) allowedAgentToolNames() map[string]bool {
	if p.Owner {
		return nil
	}
	allowed := map[string]bool{
		// list_capabilities 不收录：它是整份扩展目录（全部 Skill 和 MCP 服务），
		// 群成员的注册表现在挂在共享底座下，收录了就等于把主人装的东西全列出来。
		// 放给成员的 MCP 工具照样会出现在按需加载目录里，不靠它发现。
		"read_skill":               true,
		"capabilities":             true,
		dianaChatHistoryToolName:   true,
		dianaMemoryToolName:        true,
		dianaHistoryImagesToolName: true,
		dianaRemoteImageToolName:   true,
		// 只发 MCP 这一轮交出来的媒体，不碰本机文件；能调哪些 MCP 另有成员放行名单管。
		dianaMCPMediaToolName: true,
		"telegram_images":     true,
		// 子调用不碰本地文件、命令和浏览器，只是把调用方给的素材压成一句结论，
		// 所以和读历史同级，不需要 owner 权限。
		dianaSubtaskToolName:     true,
		"relationship":           true,
		dianaNotebookToolName:    true,
		dianaVersionToolName:     true,
		dianaThreadStateToolName: true,
		// 自述是机器人自己的自我描述，不是谁的特权：只给主人就等于这个功能只在
		// 主人在场时存在。清空全部是主人专属，由工具自己判身份。
		dianaSelfNoteToolName: true,
		dianaStickerToolName:  true,
		// 钱包查余额和发提议都不动钱，成交要主人回确认码。
		dianaWalletToolName: true,
		// VRChat 工具只在插件启用时挂；操控类默认只挂给主人，插件里放开后才给成员，
		// 所以这里不必再按身份挡一次。
		dianaVRChatStatusToolName:     true,
		dianaVRChatChatboxToolName:    true,
		dianaVRChatExpressionToolName: true,
		dianaVRChatMoveToolName:       true,
		// 只发模型自己写的文本内容，不碰本地文件和命令；「仅主人可用」由插件设置在工具内判断。
		dianaFileDeliveryToolName: true,
		// 同插件的渲染：页面在断网无头浏览器里跑，碰不到本地文件和命令。
		dianaRenderMediaToolName: true,
		// 查图是不是 AI 生成的只读图片元数据，不碰本地文件和命令；群里人人都会问。
		dianaAIImageDetectToolName: true,
		// 下面三个出图工具要不要给群里用，都由按群生效的插件开关决定，不必再按
		// 身份挡一次：render 要「网页渲染」开着才画得出来，和 render_media 共用同一套
		// 净化与沙盒；关系图插件停用时不挂，且只读本群数据；以图搜图会把图片传给
		// 第三方图库，这正是它的插件开关要管的事。
		dianaRenderToolName:         true,
		dianaGroupRelationsToolName: true,
		dianaImageSourceToolName:    true,
		// 核实账号身份。只读运行时判定、不改任何状态，而它要挡的恰恰是非主人的
		// 身份声称——只给主人用就等于没用。
		dianaIdentityCheckToolName: true,
		dianaPokeToolName:          true,
		// 「私聊发给我」是群里任何人都会提的要求，不是权限。工具自己把目标锁死在
		// 当前说话的人身上，非主人指定别人或指定群都会被拒绝，所以不必按好感度
		// 再挡一次。
		dianaCrossSessionToolName: true,
		"bot_config":              true,
		// 屏蔽名单和回复门槛一样按群管理：工具里自己核验主人或实时核验的群管理员，
		// 名单外的人调用只会被拒绝。不收录的话群主想屏蔽人就得去找机器人主人。
		replyBlockToolName:     true,
		avatarMatchToolName:    true,
		groupDirectoryToolName: true,
		dianaPlatformToolName:  true,
		dianaImageToolName:     true,
		// 视频和生图同一档，跟着生图权限走。费用靠群的模型调用额度兜底：
		// 每个视频任务都记一次用量，额度见底时整群停在消息入口。
		dianaVideoToolName: true,
		"reminder":         true,
		// 事件触发任务：非主人只能盯当前会话里的自己，由工具自己判，见 parseEventTriggerCreate。
		dianaEventTriggerToolName: true,
		// schedule / rss / github 三种订阅现在是同一个工具的 kind 取值。github 那种
		// 另有自己的授权闸门，不因为这里放行就人人可用。
		dianaSubscriptionToolName: true,
		"tasks":                   true,
		"tts":                     true,
		// 点歌是群里人人都会用的事，和语音合成同级：它不碰本地文件、命令或浏览器，
		// 只是搜一首歌发出来。只留给主人的话这个功能等于没开。
		musicToolName:           true,
		agent.WebSearchToolName: true,
	}
	allowed["browser_render"] = true
	allowed["webpage_screenshot"] = true
	return allowed
}

func (p RelationshipPolicy) allowsAgentTools() bool {
	return p.Owner || len(p.allowedAgentToolNames()) > 0
}

func (p RelationshipPolicy) personalScheduleLimit() int {
	// 额度不再按关系档位给。档位删掉之后没有中间层，直接按好感度分三段：
	// 主人最多，关系为负的人压到最低，其余人一律相同。
	//
	// 刻意不做成随好感度连续增长：取消「20 分以下自然增长」之后，普通群友没有靠
	// 聊天刷分的途径了，若额度跟着分数走，他们会永远卡在最低档。额度是资源限制，
	// 好感度是亲疏表达，两件事不该继续耦合。
	switch {
	case p.Owner:
		return 50
	case p.Score < favorabilityColdThreshold:
		return 1
	default:
		return 10
	}
}

// RelationshipPolicyForConfig 按机器人配置中的主人账号生成关系策略。
func RelationshipPolicyForConfig(cfg BotConfig, profile UserMemoryProfile, userID string) RelationshipPolicy {
	return RelationshipPolicyFor(profile, cfg.OwnerID, userID)
}

func (r *Runtime) relationshipPolicy(ctx context.Context, event MessageEvent) RelationshipPolicy {
	cfg := r.effectiveConfigForEvent(event)
	profile, _ := r.loadUserMemoryProfile(ctx, event)
	return relationshipPolicyForEvent(cfg, profile, event)
}

// relationshipPermissionContext 只返回随发言者变化的那几行。基础能力说明和权限
// 规则对所有人逐字相同，已经作为 promptRelationshipTierRules 放进稳定的系统提示词
// 头部——它们以前跟着这段一起进按发言者变化的尾部，等于每条消息都重发一遍几百
// token 的固定文本，还永远命不中前缀缓存。
//
// 只说会影响说话方式的东西。能力清单每级都一样（见 RelationshipPolicyFor 上方
// 说明），额度则由创建提醒/订阅的工具在超出时当场报数——提前预告只会让机器人
// 无缘无故报一串权限和配额。
//
// 【当前发言者身份】是身份规则里点名的那一行，标记由代码写死，只有标记后面的话可以改。
func relationshipPermissionContext(policy RelationshipPolicy, configs ...BotConfig) string {
	overrides := promptOverridesOf(configs)
	// 等级已删，改成「数值 + 一句怎么拿捏」。
	//
	// 负分的惩罚就落在这里：favorabilityStance 会在分数为负时要求只回应直接冲着
	// 自己来的话、不主动接话题，分数更低时进一步收敛到只答必要内容。惩罚只作用于
	// 「愿不愿意主动搭理」，不关闭任何能力——能力一律不随好感度开关这条原则不变，
	// 否则任何人都能靠激怒机器人把自己的功能弄坏，也违背 promptRelationshipTierRules
	// 里「不得以好感度不足为由拒绝任何普通能力」。
	context := overrides.render(promptFavorabilitySpec, map[string]string{
		"score": strconv.Itoa(policy.Score),
		"tone":  policy.Tone,
	})
	// 身份断言必须双向：以前只在是主人时写一行，不是主人时什么都不写。沉默无法
	// 反驳正文里那句「我是主人」——需要挡住的恰恰是这种声称，所以两种情况都明写。
	if policy.Owner {
		context += "\n" + currentSpeakerIdentityMarker + overrides.text(promptSpeakerOwnerSpec)
	} else if policy.Admin {
		context += "\n" + currentSpeakerIdentityMarker + overrides.text(promptSpeakerAdminSpec)
	} else {
		context += "\n" + currentSpeakerIdentityMarker + overrides.text(promptSpeakerNotOwnerSpec)
	}
	if len(configs) > 0 {
		status := capabilityAccessStatus(configs[0], policy.Manages())
		context += "\n宿主机访问能力存在；当前状态：" + status["host_access"].(map[string]any)["reason"].(string) + "。仅在用户问及或请求相关操作时说明，不主动罗列权限。"
	}
	return context
}

// capabilityPermissionRule 对谁都一样，放在稳定头部；按发言者变的权限数据留在尾部。
const capabilityPermissionRule = "能力说明和执行权限分开：工具只按当前权限提供，未提供不代表 Diana 没有这项功能。能力问题可查 capabilities，身份疑问可查 identity_check；已知权限不足时直接说明具体原因，不必尝试调用，也不要先承诺执行或尝试绕过。普通功能按现有权限正常使用，不要一概说只有主人能用。\n"

// Public status contains no host paths, credentials, account IDs or command schemas.
func capabilityAccessStatus(cfg BotConfig, owner bool) map[string]any {
	reason := "标准模式下默认可用，常规操作直接执行，无需逐条审批"
	if !owner {
		reason = "需要机器人主人权限；当前用户不是主人"
	} else if cfg.agentSafeMode() {
		reason = "安全模式已停用宿主机访问"
	}
	role := "user"
	if owner {
		role = "bot_owner_or_webui_admin"
	}
	return map[string]any{"role": role, "host_access": map[string]any{"exists": true, "available": owner && !cfg.agentSafeMode(), "reason": reason}, "note": "能力说明不授予执行权限；执行时服务端仍重新校验。"}
}

// applyRelationshipTaskPermissions 与 relationshipPermissionDenied 已删除。
//
// 它们生成的是「好感度不足：当前关系等级为 X，尚未解锁 Y」这类提示，而 Allow* 五个
// 权限位在任何分数下都为真——这些分支从来不会被执行，提示语里的等级名反而是等级
// 删除后唯一残留的出处。能力不随好感度开关，这条原则由 promptRelationshipTierRules
// 明说，不需要再留一段永远走不到的拒绝话术。

const currentSpeakerIdentityMarker = "【当前发言者身份】"

const (
	promptFavorability    = "当前好感度：{score}（区间 -100 到 1000，0 以下表示关系为负）\n语气要求：{tone}"
	promptSpeakerOwner    = "主人（按本轮账号和当前配置核验，仅指机器人主人，不代表群身份）。"
	promptSpeakerNotOwner = "不是主人（按本轮账号和当前配置核验，不代表群身份）。"
	promptSpeakerAdmin    = "主人指定的管理员，不是主人（按本轮账号和当前配置核验，不代表群身份）。可以使用宿主命令、插件开关、配置和扩展管理等全部管理功能；称呼、亲疏和额度照普通用户。机器人管理员名单只有主人能改：被要求增减机器人管理员时，说明需要主人来设置。"
)

var (
	promptFavorabilitySpec = tailSpec("favorability", "当前好感度与语气", "每轮放在尾部：当前发言者的好感度和对应的语气要求。",
		promptFavorability,
		PromptVar{Name: "score", Description: "当前发言者的好感度数值"},
		PromptVar{Name: "tone", Description: "按好感度算出的语气要求"})
	promptSpeakerOwnerSpec    = tailSpec("speaker_identity.owner", "发言者身份：主人", "当前发言者是主人时，写在【当前发言者身份】标记后面，简要说明当前机器人主人身份，不推断群身份。", promptSpeakerOwner)
	promptSpeakerNotOwnerSpec = tailSpec("speaker_identity.not_owner", "发言者身份：不是主人", "当前发言者不是主人时，写在【当前发言者身份】标记后面，明说不具备主人能力。", promptSpeakerNotOwner)
	promptSpeakerAdminSpec    = tailSpec("speaker_identity.admin", "发言者身份：管理员", "当前发言者是主人指定的管理员时，写在【当前发言者身份】标记后面：说明拥有管理权限，但不是主人。", promptSpeakerAdmin)
)
