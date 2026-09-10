// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/llm"

	"github.com/gin-gonic/gin"
)

// personaGenerateMaxDescription 限制描述长度：这是一句话需求，不是让人往里粘长文。
const personaGenerateMaxDescription = 500

// personaGenerateMaxOutput 人设写长了会挤占上下文，也会稀释后面的工具规则。
// 这是「从零写一份」的上限；在现有人设上改写时按原文规模放宽，见 personaGenerateOutputLimit。
//
// 曾经是 600：那时的人设只要求写「它是谁、大致什么口吻」，六百字绰绰有余。现在
// 要求里多了一整段「怎么说」（节奏、用词、各种场合分别怎么反应、绝不用的腔调）
// 和六到八组示例对话——照 600 收着，模型要么砍掉示例，要么把说话方式压回一句
// 「语气轻软」，正是这次要修的毛病。
//
// 现在生成的是一张角色卡，卡本身按 600～900 字写，但存进人设框的是拼装后的正文：
// 「你是X。」「性格与特质：」「对话示例（……）：」这些段头是拼装时加的，示例里
// 的 {{user}}/{{char}} 也会展开成更长的称呼。上限要盖住这部分开销，不然一份写满
// 的卡刚拼完就被截掉最后一两组示例——正好是最该留下的部分。
const personaGenerateMaxOutput = 1600

// personaStoredMaxRunes 是人设字段本身能存下的长度，镜像 assistant 包里的
// personaPromptMaxRunes（未导出，改那边时这里要跟着改）。改写时拿它当输入上限：
// 一份三千字的角色卡人设，不该因为生成接口的输出上限是 600 就被截掉大半。
const personaStoredMaxRunes = 4000

type personaGeneratePayload struct {
	Description string `json:"description"`
	Name        string `json:"name,omitempty"`
	Current     string `json:"current,omitempty"`
	// Card 是上一次生成交回来的那张卡，改写时原样带回来。可选：人设框里的正文
	// 是拼装后的结果，拆不回卡；有卡时模型改的是字段，没卡时只能照正文重写一张。
	Card         json.RawMessage `json:"card,omitempty"`
	ProfileID    string          `json:"profile_id,omitempty"`
	Group        string          `json:"group,omitempty"`
	Model        string          `json:"model,omitempty"`
	ResponseMode string          `json:"response_mode,omitempty"`
}

// personaGenerateModeHints 描述搭话欲望，让人设自己带上这个分寸。
// 键要覆盖 assistant.ResponseMode 的全部取值（custom 除外：那是用户自己拧过的
// 细项，没有一句话能概括它的分寸）。
var personaGenerateModeHints = map[string]string{
	"quiet":        "性格偏安静，没人叫它就不太主动插话。",
	"assistant":    "性格是随叫随到的那种，被叫到就好好答，平时不主动挑起闲聊。",
	"standard":     "性格分寸感正常，有话题时会接，但不会硬凑。",
	"active":       "性格外向，乐意主动参与群里的话题。",
	"super_active": "性格非常外向，看到感兴趣的话题就想插一句，但也不至于刷屏。",
}

// personaGenerateSystemPrompt 约束生成结果只写「它是谁、怎么说话」。
//
// 运行时会在人设的前后另外注入一整套规则：表达与投递规范（replyPresentationPrompt）、
// 自称与句尾语气词（personaVoice.prompt，措辞是「可选表达」）、动作描写开关
// （actionDescriptionPrompt）、表情符号禁令、拒答策略、群聊场景、关系等级、时段与
// 心情语气，最后再来一句「保持人设中设定的身份和口吻」的锚点（personaClosingAnchor）。
//
// 那句锚点排在最后、离生成最近，所以人设正文里的硬规则会盖过前面所有「可选」的说法。
// 一句「每句话都以喵收尾」写进人设，等于把界面上的句尾语气词开关按死——用户之后
// 在那个输入框里怎么改都不再有效果，而且没有任何地方会提示他这件事。下面这些禁令
// 基本都是为这一条服务的：人设只描述「是个什么样的角色」，一切逐句强制的规则、
// 格式规范、安全流程和能力清单都由运行时那几段负责。
// 分工划完之后还剩一件事：人设正文本身得写得够细。只写「它是谁、大概什么口吻」
// 的人设，模型读完只能拿到一个形容词，落到具体一句话上仍旧退回默认的礼貌助理腔。
// 真正管用的是「怎么说」——句子多长、爱用哪些词、遇到分享/求助/被夸/被怼/不懂时
// 各自怎么反应、绝不用哪几种腔调——外加几组示例对话把密度示范出来。这一套的样板
// 就是 assistant 包里几档预设风格（猫娘、真人感、扮演）的写法。
//
// 输出形状是一张 SillyTavern V2 角色卡（只要 data 那几个文本字段），不是一段散文：
// 项目本来就认这种卡（webui 有导入接口，assistant.ComposeCharacterCardPersona 负责
// 拼成人设正文），生成走同一种结构，生成的人设和导入的人设才是同一样东西——能导出
// 给别的工具用，也能拿回来接着改。上面那些「写什么、不写什么」原样保留，只是各自
// 落到对应的字段里。
const personaGenerateSystemPrompt = `你在为一个运行在 QQ 群里的聊天机器人写一张角色卡。只输出一个 JSON 对象，字段是 SillyTavern V2 角色卡 data 的那几项：

{"name": "…", "description": "…", "personality": "…", "scenario": "…", "first_mes": "…", "mes_example": "…", "system_prompt": "…"}

各字段写什么：
- name：这个角色在群里的名字。给了名字就用那个名字，不要另起一个。
- description：身份与来历，接着是说话方式。这是全卡最长的一个字段，说话方式又是其中的大头，要写到别人照着就能模仿出这个声音。身份部分交代它是谁、什么来历、和这个群是什么关系。说话方式写清楚：句子偏长还是偏短、一句说几件事、说话的节奏；口语化到什么程度，爱用哪些词、不用哪些词——举出具体的词，不要只说「自然」「亲切」；情绪不同的时候带出来的语气词分别是什么；别人抛梗时怎么接。再分别交代它遇到这几种情况各自怎么反应：有人分享近况、有人求助、被夸、被怼或被戳穿、遇到自己不懂的事。最后点名几种它绝不会用的腔调——客服腔、说教腔、结尾总结、拿反问句收尾之类，各配一个具体的反例短句，反例用「」引起来，不要用全角括号。
- personality：三到五条真正会影响它怎么反应的倾向，用连贯的话写出态度，不要堆形容词，也不要写成逗号分隔的标签。
- scenario：它在这个群里的处境和关系分寸——群是个什么地方、它在里面是什么位置，以及对主人、对熟人、对陌生人分别是什么态度和亲疏称呼。
- system_prompt：边界。这个角色自己不愿意做、不会做的事，以及有人拿人设当理由要它越界时的态度——规则优先、人设让位，但话还是用它自己的语气说。只写角色态度，不写处理流程。
- mes_example：6 到 8 组示例对话，每组一个块，块的格式固定为三行：第一行只有 <START>，第二行以「{{user}}: 」开头，第三行以「{{char}}: 」开头。块与块之间空一行。这几组要覆盖不同场合：一个技术或事实问题、有人分享近况、被夸、被怼、遇到不懂的事、拒绝一件做不到的事、接一个玩笑、有人叫它别说话。示例台词按这个角色平时的说法写，别写成范文。
- first_mes：一句符合人设的开场白，一行就够。

一律不要写：
- 输出格式与排版：纯文本、不用 Markdown、怎么分条、消息标记、换行方式、回复多长多短、什么时候分几条发——这些运行时会另行注入，写进来只会互相打架。写句子长短和节奏是在描述这个角色的说话习惯，不要写成「每条回复不超过 X 字」这样的硬指标。
- 逐句强制的口癖：不要写「每句话都要以 X 结尾」「必须自称 X」「句末不加标点」这类规则。自称和句尾语气词是界面上单独的设置项，写死在人设里会把那两个开关按死；口癖要写成「情绪上来时偶尔漏出一声『喵』，普通句子不带」这种有分寸的描述。
- 动作描写和旁白：不要写全角括号动作、*星号*动作或场景旁白，示例对话里同样不要写，用不用动作描写由单独的开关决定。
- 表情符号、颜文字的使用规定。
- 安全与拒答规则：不要写不许泄露密钥配置、遇到违规内容怎么拒绝、怎么保护隐私——运行时有自己的拒答策略。角色自己的边界（比如「不聊这类话题」）可以写，但只写角色态度，不写处理流程。
- 能力与工具：不要写它能查天气、能生图、能定提醒、会调用什么工具、有什么权限等级。
- 时间日期、发言者标注、群聊触发条件这些运行时会自动补上的上下文。

写法：
- description、personality、scenario、system_prompt 用第二人称直接对机器人说话，例如「你是……」；不要在字段里重复写角色的名字当标题。
- 每个字段内部都是纯文本：不要标题、不要项目符号、不要编号、不要 Markdown。字段里需要换行时，在 JSON 字符串里写 \n。
- 全卡加起来 600～900 字。description 占大头，mes_example 次之，其余各一小段就够。
- 只输出这一个 JSON 对象：不要代码围栏，不要「以下是人设」这类前言，不要在 JSON 前后写解释或使用说明。`

// personaGenerate 用当前已配置的模型写一张角色卡，并把拼装后的人设正文一起返回。
func (h *LLMConfigHandler) personaGenerate(c *gin.Context) {
	var payload personaGeneratePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "llm.persona", err, "", nil)
		return
	}
	description := strings.TrimSpace(payload.Description)
	if description == "" {
		h.writeError(c, http.StatusBadRequest, "llm.persona", fmt.Errorf("请先描述想要的角色"), "", nil)
		return
	}
	if len([]rune(description)) > personaGenerateMaxDescription {
		description = string([]rune(description)[:personaGenerateMaxDescription])
	}
	current := personaRewriteBase(payload.Current)

	cfg, err := personaProviderConfig(h.store.Profiles(), payload)
	if err != nil {
		h.writeError(c, http.StatusUnprocessableEntity, "llm.persona", err, payload.Model, nil)
		return
	}
	client, err := h.newClient(cfg)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "llm.persona", err, cfg.Model, llmLogMetadata(cfg, ""))
		return
	}

	resp, err := client.Generate(c.Request.Context(), llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: personaGenerateSystemPrompt},
			{Role: llm.RoleUser, Content: personaGenerateUserPrompt(description, payload.Name, current, payload.Card, payload.ResponseMode)},
		},
	})
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "llm.persona", err, cfg.Model, llmLogMetadata(cfg, ""))
		return
	}
	card, err := parseGeneratedCharacterCard(resp.Text, payload.Name)
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "llm.persona", err, cfg.Model, llmLogMetadata(cfg, ""))
		return
	}
	// 正文用导入角色卡那条路上的同一个拼装函数：生成的卡和导进来的卡拼出同样
	// 形状的人设，用户看到的、机器人读到的都是同一样东西。
	persona := normalizeGeneratedPersona(assistant.ComposeCharacterCardPersona(card), personaGenerateOutputLimit(current))
	if persona == "" {
		h.writeError(c, http.StatusBadGateway, "llm.persona", errPersonaCardUnusable, cfg.Model, llmLogMetadata(cfg, ""))
		return
	}
	recordRequestOperation(c, h.logs, "llm.persona", "生成基础人设成功", resp.Model, llmLogMetadata(cfg, ""))
	c.JSON(http.StatusOK, gin.H{
		"persona":  persona,
		"card":     personaCardEnvelope{Spec: personaCardSpec, SpecVersion: personaCardSpecVersion, Data: &card},
		"model":    resp.Model,
		"provider": resp.Provider,
		"usage":    resp.Usage,
	})
}

// SillyTavern V2 的封套标记。生成结果照这个形状发回去，前端存下来的那份 JSON
// 就是一张能直接导回来、也能喂给别的工具的标准卡。
const (
	personaCardSpec        = "chara_card_v2"
	personaCardSpecVersion = "2.0"
)

// personaCardEnvelope 是卡的外壳。V2/V3 把字段收在 data 里，V1 平铺在顶层——
// 解析时两种都认（模型经常直接吐一层平铺的 data），发回去时一律写成 V2。
type personaCardEnvelope struct {
	Spec        string                       `json:"spec,omitempty"`
	SpecVersion string                       `json:"spec_version,omitempty"`
	Data        *assistant.CharacterCardData `json:"data,omitempty"`
}

// parseGeneratedCharacterCard 把模型的输出读成一张卡。
//
// 宽松地读：围栏、围栏外的一句「这是生成的卡」、JSON 后面多写的一段说明都很常见，
// 从第一个左花括号起按括号配对切出那一段再解析。必填的三样是名字、简介和示例
// 对话——缺了名字拼不出「你是X。」，缺了示例这次生成就退回「它是谁」那一版。
func parseGeneratedCharacterCard(text string, fallbackName string) (assistant.CharacterCardData, error) {
	raw := extractPersonaCardJSON(text)
	if raw == "" {
		return assistant.CharacterCardData{}, errPersonaCardNotJSON
	}
	var envelope struct {
		Data *assistant.CharacterCardData `json:"data,omitempty"`
		assistant.CharacterCardData
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return assistant.CharacterCardData{}, errPersonaCardNotJSON
	}
	card := envelope.CharacterCardData
	if data := envelope.Data; data != nil && strings.TrimSpace(data.Name+data.Description+data.MesExample) != "" {
		// 有 data 就以 data 为准：V2/V3 的字段都收在里面，顶层那份只有 V1 平铺卡
		// 和模型偷懒直接吐一层字段的时候才有东西。
		card = *data
	}
	card.Name = strings.TrimSpace(card.Name)
	card.Description = strings.TrimSpace(card.Description)
	card.Personality = strings.TrimSpace(card.Personality)
	card.Scenario = strings.TrimSpace(card.Scenario)
	card.FirstMes = strings.TrimSpace(card.FirstMes)
	card.MesExample = strings.TrimSpace(card.MesExample)
	card.SystemPrompt = strings.TrimSpace(card.SystemPrompt)
	if card.Name == "" {
		card.Name = strings.TrimSpace(fallbackName)
	}
	if card.Name == "" || card.Description == "" || card.MesExample == "" {
		return assistant.CharacterCardData{}, errPersonaCardIncomplete
	}
	return card, nil
}

// extractPersonaCardJSON 从一段可能带围栏、带前后废话的输出里切出那个 JSON 对象。
//
// 按括号配对找结尾，而不是取最后一个右括号：模型在 JSON 后面追一句「以上是……{}」
// 这种情况虽然少见，但取最后一个括号会把整段废话一起塞进解析器。字符串里的括号
// 和转义引号要跳过，示例对话里出现 { } 的时候才不会提前收尾。
func extractPersonaCardJSON(text string) string {
	text = stripPersonaCodeFence(text)
	start := strings.Index(text, "{")
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escaped := false
	for index, char := range text[start:] {
		switch {
		case escaped:
			escaped = false
		case inString && char == '\\':
			escaped = true
		case char == '"':
			inString = !inString
		case inString:
		case char == '{':
			depth++
		case char == '}':
			if depth--; depth == 0 {
				return strings.TrimSpace(text[start : start+index+1])
			}
		}
	}
	return ""
}

var (
	errPersonaCardNotJSON    = errors.New("模型没有返回角色卡 JSON")
	errPersonaCardIncomplete = errors.New("模型返回的角色卡缺少名字、简介或示例对话")
	errPersonaCardUnusable   = errors.New("模型没有返回可用的人设")
)

func personaProviderConfig(set llm.ProfileSet, payload personaGeneratePayload) (llm.ProviderConfig, error) {
	set = set.WithDefaults()
	var selected llm.Profile
	if profileID := strings.TrimSpace(payload.ProfileID); profileID != "" {
		for _, profile := range set.Profiles {
			if profile.ID == profileID {
				selected = profile
				break
			}
		}
		if selected.ID == "" {
			return llm.ProviderConfig{}, fmt.Errorf("对话提供商配置 %q 不存在", profileID)
		}
	} else if group := strings.TrimSpace(payload.Group); group != "" {
		if profiles := set.GroupProfiles(group); len(profiles) > 0 {
			selected = profiles[0]
		}
	} else if current, ok := set.FirstProfile(); ok && personaTextProfile(current) {
		selected = current
	}
	if selected.ID == "" {
		for _, profile := range set.GroupProfiles(llm.GroupChat) {
			if personaTextProfile(profile) {
				selected = profile
				break
			}
		}
	}
	if selected.ID == "" || !personaTextProfile(selected) {
		return llm.ProviderConfig{}, fmt.Errorf("没有可用于生成人设的文本提供商配置")
	}
	cfg := selected.Config.WithDefaults()
	if model := strings.TrimSpace(payload.Model); model != "" {
		if len(cfg.Models) > 0 {
			found := false
			for _, item := range cfg.Models {
				if strings.TrimSpace(item.ID) == model {
					found = true
					break
				}
			}
			if !found {
				return llm.ProviderConfig{}, fmt.Errorf("模型 %q 不属于对话提供商配置 %q", model, selected.Name)
			}
		}
		cfg.Model = model
	}
	return cfg, nil
}

func personaTextProfile(profile llm.Profile) bool {
	switch llm.NormalizeProfileGroup(profile.Group) {
	case llm.GroupImage, llm.GroupEmbedding:
		return false
	default:
		return true
	}
}

// personaRewriteBase 整理「现有人设」这一路输入。
//
// 上限用 personaStoredMaxRunes 而不是输出上限：以前这里按 600 截，于是用户拿一份
// 导入的角色卡人设（正文可以到 4000 字）改一句话，模型只看得到开头六百字，写回来的
// 结果又整段覆盖原文——一次点击就静默丢掉大半人设。
func personaRewriteBase(current string) string {
	current = strings.TrimSpace(current)
	if len([]rune(current)) > personaStoredMaxRunes {
		current = strings.TrimSpace(string([]rune(current)[:personaStoredMaxRunes]))
	}
	return current
}

// personaGenerateOutputLimit 决定这次允许多长。
//
// 从零写按 personaGenerateMaxOutput 收着；改写时至少不低于原文规模，否则「再毒舌
// 一点」这种微调会把一份长人设压成六百字，用户还以为只是改了语气。
func personaGenerateOutputLimit(current string) int {
	limit := personaGenerateMaxOutput
	if size := len([]rune(current)); size >= limit {
		// 改写常会比原文长一点（补一句设定），留一点余量再封顶。
		limit = size + personaGenerateMaxOutput/2
	}
	if limit > personaStoredMaxRunes {
		limit = personaStoredMaxRunes
	}
	return limit
}

// personaGenerateUserPrompt 拼接这次的需求。带上现有人设时是「改写」而不是「重写」，
// 否则用户微调一句话就会丢掉已经调好的其它设定。
func personaGenerateUserPrompt(description, name, current string, card json.RawMessage, responseMode string) string {
	var builder strings.Builder
	if name = strings.TrimSpace(name); name != "" {
		builder.WriteString("机器人的名字是「" + name + "」。\n")
	}
	if hint := personaGenerateModeHints[strings.ToLower(strings.TrimSpace(responseMode))]; hint != "" {
		builder.WriteString("已选的回复模式：" + hint + "这个分寸要化进角色的性格里，不要把这句话原样抄进人设。\n")
	}
	if current = personaRewriteBase(current); current != "" {
		// 说清楚「只改点到的地方」和「输出完整的卡」两件事：只说「改写」的话，模型
		// 一半时候会另起炉灶重写一个角色，另一半时候只回一句改动说明。
		builder.WriteString("这次是改写，不是重写。现在生效的人设正文如下：\n" + current + "\n\n")
		if raw := personaRewriteCard(card); raw != "" {
			// 有卡就以卡为准：正文是卡拼出来的结果，段头和展开过的宏都是拼装时
			// 加的，让模型照正文反推字段只会把段头写进字段里。
			builder.WriteString("这份正文是由下面这张角色卡拼成的，改写以卡为准：\n" + raw + "\n\n")
		}
		builder.WriteString("改写要求：保留原来的名字、身份和仍然成立的设定，只改新需求点到的地方；不要顺手压缩、精简或删掉与新需求无关的部分，各字段长度与原来相当即可。原来的说话方式细则和示例对话要原样留着，只在新需求点到时才改；原来没有这两部分的，按上面的字段说明补齐，让改写后的卡同样带上完整的说话方式和 6 到 8 组示例对话。输出改写后的完整角色卡 JSON，不要只写改了哪里，也不要只回改动的那几个字段。\n\n")
	}
	builder.WriteString("新需求：" + description)
	return builder.String()
}

// personaRewriteCard 把请求里带回来的那张卡整理成能贴进提示词的一段 JSON。
//
// 只认对象、只取卡的那几个文本字段：前端存的可能是完整封套，也可能被别的工具动过
// 手脚，重新序列化一遍既统一了形状，也顺手挡掉夹带进来的其它字段。整理不出来就
// 当没带卡——正文还在，改写照样能走。
func personaRewriteCard(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	card, err := parseGeneratedCharacterCard(string(raw), "")
	if err != nil {
		return ""
	}
	encoded, err := json.MarshalIndent(card, "", "  ")
	if err != nil {
		return ""
	}
	return string(encoded)
}

// personaPreambleMarkers 是交付语里一定会出现的词。
//
// 判前言要三条同时成立：短、以冒号收尾、含这里的词。只认冒号会误伤正文——
// 「你是 Diana：一只活在群里的猫娘」开头就是一个冒号句，砍掉等于砍掉身份。
var personaPreambleMarkers = []string{"人设", "以下", "如下", "为你", "生成", "供参考", "好的", "没问题", "根据"}

// personaPreambleLines 是整行只有交付语、连冒号都没有的情况，多来自被剥掉的
// Markdown 标题（「# 人设」）。
var personaPreambleLines = map[string]struct{}{
	"人设": {}, "基础人设": {}, "人设正文": {}, "以下是人设": {}, "生成的人设": {}, "角色人设": {},
}

// personaPreambleMaxRunes 限制前言长度：真正的交付语都很短，长句子是正文。
const personaPreambleMaxRunes = 40

// normalizeGeneratedPersona 把模型的输出收拾成能直接进人设框的纯文本。
//
// 模型交回来的东西有几种固定毛病：套一层代码围栏、先说一句「以下是为你生成的人设：」、
// 排成 Markdown 标题加要点、整段用引号包起来。这些都不是人设内容：留在正文里，
// 机器人会把它们当成自己的设定，Markdown 记号还会被它照着学出去——而 QQ 不渲染
// Markdown，用户看到的就是一堆星号和井号。
func normalizeGeneratedPersona(text string, limit int) string {
	text = stripPersonaCodeFence(text)
	// 三样毛病会互相嵌套（引号里套前言、标题下面才是正文），一趟剥不干净，
	// 剥到不再变化为止；轮数封顶，免得哪条规则来回改写同一段文字。
	for round := 0; round < 4; round++ {
		before := text
		text = strings.TrimSpace(stripPersonaMarkdown(text))
		text = strings.TrimSpace(stripPersonaPreamble(text))
		text = strings.TrimSpace(stripPersonaWrappingQuotes(text))
		if text == before {
			break
		}
	}
	if limit <= 0 {
		limit = personaGenerateMaxOutput
	}
	return truncatePersonaText(text, limit)
}

// stripPersonaCodeFence 去掉整段外面的代码围栏。
func stripPersonaCodeFence(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "```") {
		return text
	}
	if index := strings.Index(text, "\n"); index >= 0 {
		text = text[index+1:]
	} else {
		text = strings.TrimPrefix(text, "```")
	}
	text = strings.TrimSpace(text)
	if index := strings.LastIndex(text, "```"); index >= 0 {
		text = strings.TrimSpace(text[:index])
	}
	return text
}

// stripPersonaWrappingQuotes 去掉包住整段的引号。
func stripPersonaWrappingQuotes(text string) string {
	text = strings.TrimSpace(text)
	for _, pair := range [][2]string{{"“", "”"}, {"\"", "\""}, {"「", "」"}, {"『", "』"}} {
		if strings.HasPrefix(text, pair[0]) && strings.HasSuffix(text, pair[1]) && len(text) > len(pair[0])+len(pair[1]) {
			// 只剥「整段被包起来」的那一层：正文中间还有同款引号时（「主人」这种
			// 称呼很常见），剥掉首尾会把中间的配对关系拆散。
			inner := text[len(pair[0]) : len(text)-len(pair[1])]
			if strings.Contains(inner, pair[1]) && pair[0] != pair[1] {
				continue
			}
			text = strings.TrimSpace(inner)
		}
	}
	return text
}

// stripPersonaMarkdown 去掉标题、引用块、项目符号和加粗记号，只留文字。
func stripPersonaMarkdown(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			// 段落之间留一个空行就够，多余的空行是 Markdown 排版留下的。
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		}
		blank = false
		if only := strings.Trim(line, "#*-—_= "); only == "" {
			// 整行只有记号：分隔线、空标题，直接扔。
			continue
		}
		line = strings.TrimSpace(strings.TrimLeft(line, "#>"))
		line = trimPersonaListMarker(line)
		line = strings.ReplaceAll(line, "**", "")
		line = strings.ReplaceAll(line, "##", "")
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// trimPersonaListMarker 去掉行首的项目符号或编号。
func trimPersonaListMarker(line string) string {
	for _, marker := range []string{"- ", "* ", "+ ", "• ", "・", "· "} {
		if strings.HasPrefix(line, marker) {
			return strings.TrimSpace(strings.TrimPrefix(line, marker))
		}
	}
	runes := []rune(line)
	digits := 0
	for digits < len(runes) && digits < 2 && runes[digits] >= '0' && runes[digits] <= '9' {
		digits++
	}
	if digits == 0 || digits >= len(runes) {
		return line
	}
	switch runes[digits] {
	case '.', '、', ')', '）':
		return strings.TrimSpace(string(runes[digits+1:]))
	}
	return line
}

// stripPersonaPreamble 去掉开头的交付语。
func stripPersonaPreamble(text string) string {
	for round := 0; round < 3; round++ {
		text = strings.TrimSpace(text)
		line, rest, hasRest := strings.Cut(text, "\n")
		line = strings.TrimSpace(line)
		// 交付语单独占一行时整行扔掉。
		if hasRest && strings.TrimSpace(rest) != "" && isPersonaPreambleLine(line) {
			text = rest
			continue
		}
		// 交付语和正文挤在同一行时只切到冒号，后面的正文留着。
		head, tail, ok := cutPersonaColon(line)
		if !ok || !isPersonaPreambleLine(head) {
			return text
		}
		if hasRest {
			tail = strings.TrimSpace(tail) + "\n" + rest
		}
		if strings.TrimSpace(tail) == "" {
			return text
		}
		text = tail
	}
	return strings.TrimSpace(text)
}

func isPersonaPreambleLine(head string) bool {
	head = strings.TrimSpace(head)
	if head == "" || len([]rune(head)) > personaPreambleMaxRunes {
		return false
	}
	if _, ok := personaPreambleLines[strings.TrimRight(head, "：: 。")]; ok {
		return true
	}
	if !strings.HasSuffix(head, "：") && !strings.HasSuffix(head, ":") {
		return false
	}
	if strings.Contains(head, "你是") {
		// 「你是 Diana：」是正文的第一句，不是交付语。
		return false
	}
	for _, marker := range personaPreambleMarkers {
		if strings.Contains(head, marker) {
			return true
		}
	}
	return false
}

// cutPersonaColon 在第一个冒号处切开，冒号本身留在前半段。
func cutPersonaColon(line string) (string, string, bool) {
	index := strings.IndexAny(line, "：:")
	if index < 0 {
		return "", "", false
	}
	_, size := utf8.DecodeRuneInString(line[index:])
	return line[:index+size], line[index+size:], true
}

// truncatePersonaText 截到上限，并尽量停在一句话的末尾。
//
// 硬截会留下半句话，而人设正文是要被模型当设定读的：末尾挂着「你对陌生人」这种
// 断句，比少一句设定更糟。找不到合适的句尾（比如整段没有标点）才照旧硬截。
func truncatePersonaText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return strings.TrimSpace(text)
	}
	cut := string(runes[:limit])
	if index := strings.LastIndexAny(cut, "。！？!?…”』」"); index > 0 {
		_, size := utf8.DecodeRuneInString(cut[index:])
		if head := strings.TrimSpace(cut[:index+size]); len([]rune(head)) >= limit*3/4 {
			return head
		}
	}
	return strings.TrimSpace(cut)
}
