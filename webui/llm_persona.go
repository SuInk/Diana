// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/SuInk/diana/model/llm"

	"github.com/gin-gonic/gin"
)

// personaGenerateMaxDescription 限制描述长度：这是一句话需求，不是让人往里粘长文。
const personaGenerateMaxDescription = 500

// personaGenerateMaxOutput 人设写长了会挤占上下文，也会稀释后面的工具规则。
const personaGenerateMaxOutput = 600

type personaGeneratePayload struct {
	Description string `json:"description"`
	Name        string `json:"name,omitempty"`
	Current     string `json:"current,omitempty"`
	ProfileID   string `json:"profile_id,omitempty"`
	Group       string `json:"group,omitempty"`
	Model       string `json:"model,omitempty"`
	// ReplyStyle 和 ResponseMode 是界面上和人设并列的两个选择。生成时带上它们，
	// 写出来的人设才会和已经选好的语气、搭话频率是同一个人，而不是各说各的。
	ReplyStyle   string `json:"reply_style,omitempty"`
	ResponseMode string `json:"response_mode,omitempty"`
}

// personaGenerateStyleHints 把界面上的风格选项翻译成一句写作要求。
var personaGenerateStyleHints = map[string]string{
	"groupmate": "说话像群里的普通朋友：短句、口语、不端着，不要用客服或助理腔。",
	"assistant": "说话像可靠的助理：条理清楚、用词稳，但不啰嗦。",
	"gentle":    "语气温柔耐心，多一点体谅和安抚，但不腻。",
	"lively":    "语气活泼跳脱，有梗有情绪，但不要吵闹到掩盖信息。",
	"concise":   "能一句说完就不说两句，砍掉所有寒暄和铺垫。",
	"catgirl":   "是一只会说话的猫娘，语气轻软亲人，每句话结尾都带「喵」且不打句号；但正事照样答准，不靠卖萌糊弄。",
	"roleplay":  "和对方演一段面对面的相处：消息是「（动作或神态）+ 台词」，动作一句以内，黏人主动、结尾留钩子；正事照样答准，亲密戏点到为止。",
}

// personaGenerateModeHints 描述搭话欲望，让人设自己带上这个分寸。
var personaGenerateModeHints = map[string]string{
	"quiet":    "性格偏安静，没人叫它就不太主动插话。",
	"standard": "性格分寸感正常，有话题时会接，但不会硬凑。",
	"active":   "性格外向，乐意主动参与群里的话题。",
}

// personaGenerateSystemPrompt 约束生成结果只写「它是谁、怎么说话」。
// 输出格式规范、时间注入这些是 WebUI 里独立的开关，写进人设只会重复且互相打架。
const personaGenerateSystemPrompt = `你在为一个运行在 QQ 里的聊天机器人撰写基础人设，供它作为 system prompt 使用。

要求：
1. 用第二人称直接对机器人说话，例如「你是……」。
2. 只写身份、性格、说话方式和该守的边界，不要写输出格式规范（纯文本、不用 Markdown、分条方式）——那些运行时会自动注入，重复写会互相打架。
2.1 给出了已选的表达风格或回复模式时，人设的语气和搭话分寸必须和它们一致，但不要把这些要求原样抄进去，要化成这个角色本来的性格。
3. 不要写工具用法、权限规则、拒答流程、时间注入、群聊发言者标注，这些运行时会自动补。
4. 写成连贯的一段话，不要分点、不要标题、不要 Markdown、不要代码围栏。
5. 控制在 200 字以内，宁可精准也不要堆形容词。
6. 只输出人设正文本身，不要任何前言、解释或引号包裹。`

// personaGenerate 用当前已配置的模型把一句话需求写成基础人设。
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
			{Role: llm.RoleUser, Content: personaGenerateUserPrompt(description, payload.Name, payload.Current, payload.ReplyStyle, payload.ResponseMode)},
		},
	})
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "llm.persona", err, cfg.Model, llmLogMetadata(cfg, ""))
		return
	}
	persona := normalizeGeneratedPersona(resp.Text)
	if persona == "" {
		h.writeError(c, http.StatusBadGateway, "llm.persona", fmt.Errorf("模型没有返回可用的人设"), cfg.Model, llmLogMetadata(cfg, ""))
		return
	}
	recordRequestOperation(c, h.logs, "llm.persona", "生成基础人设成功", resp.Model, llmLogMetadata(cfg, ""))
	c.JSON(http.StatusOK, gin.H{"persona": persona, "model": resp.Model, "provider": resp.Provider, "usage": resp.Usage})
}

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
			return llm.ProviderConfig{}, fmt.Errorf("对话 LLM 配置 %q 不存在", profileID)
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
		return llm.ProviderConfig{}, fmt.Errorf("没有可用于生成人设的文本 LLM 配置")
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
				return llm.ProviderConfig{}, fmt.Errorf("模型 %q 不属于对话 LLM 配置 %q", model, selected.Name)
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

// personaGenerateUserPrompt 拼接这次的需求。带上现有人设时是「改写」而不是「重写」，
// 否则用户微调一句话就会丢掉已经调好的其它设定。
func personaGenerateUserPrompt(description, name, current, replyStyle, responseMode string) string {
	var builder strings.Builder
	if name = strings.TrimSpace(name); name != "" {
		builder.WriteString("机器人的名字是「" + name + "」。\n")
	}
	if hint := personaGenerateStyleHints[strings.ToLower(strings.TrimSpace(replyStyle))]; hint != "" {
		builder.WriteString("已选的表达风格：" + hint + "\n")
	}
	if hint := personaGenerateModeHints[strings.ToLower(strings.TrimSpace(responseMode))]; hint != "" {
		builder.WriteString("已选的回复模式：" + hint + "\n")
	}
	if current = strings.TrimSpace(current); current != "" {
		if len([]rune(current)) > personaGenerateMaxOutput {
			current = string([]rune(current)[:personaGenerateMaxOutput])
		}
		builder.WriteString("现有人设如下，请在它的基础上按新需求改写，保留仍然成立的部分：\n" + current + "\n\n")
	}
	builder.WriteString("新需求：" + description)
	return builder.String()
}

// normalizeGeneratedPersona 去掉模型爱加的围栏和整体引号，并截到长度上限。
func normalizeGeneratedPersona(text string) string {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		if index := strings.Index(text, "\n"); index >= 0 {
			text = text[index+1:]
		}
		text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	}
	text = strings.TrimSpace(text)
	for _, pair := range [][2]string{{"“", "”"}, {"\"", "\""}, {"「", "」"}} {
		if strings.HasPrefix(text, pair[0]) && strings.HasSuffix(text, pair[1]) && len(text) > len(pair[0])+len(pair[1]) {
			text = strings.TrimSpace(text[len(pair[0]) : len(text)-len(pair[1])])
		}
	}
	if len([]rune(text)) > personaGenerateMaxOutput {
		text = strings.TrimSpace(string([]rune(text)[:personaGenerateMaxOutput]))
	}
	return text
}
