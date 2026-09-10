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
	Description  string `json:"description"`
	Name         string `json:"name,omitempty"`
	Current      string `json:"current,omitempty"`
	ProfileID    string `json:"profile_id,omitempty"`
	Group        string `json:"group,omitempty"`
	Model        string `json:"model,omitempty"`
	ResponseMode string `json:"response_mode,omitempty"`
}

// personaGenerateModeHints 描述搭话欲望，让人设自己带上这个分寸。
var personaGenerateModeHints = map[string]string{
	"quiet":    "性格偏安静，没人叫它就不太主动插话。",
	"standard": "性格分寸感正常，有话题时会接，但不会硬凑。",
	"active":   "性格外向，乐意主动参与群里的话题。",
}

// personaGenerateSystemPrompt 约束生成结果只写「它是谁、长什么样、怎么说话」。
// 输出格式规范、时间注入这些是 WebUI 里独立的开关，写进人设只会重复且互相打架。
//
// 身份和外观是硬要求，因为运行时不会替人设补这两样：机器人配置上的「名称」字段
// 从不进提示词（只用于合并转发的显示名），外观更是全项目没有第二个来源。人设不写，
// 它就不知道自己叫什么、长什么样，出图时每次画出来的都是另一个人。
const personaGenerateSystemPrompt = `你在为一个聊天机器人撰写基础人设，供它作为 system prompt 使用。

要求：
1. 用第二人称直接对机器人说话，例如「你是……」。
2. 开头必须点明它是谁：给出了名字就把名字原样写进第一句（「你是嘉然，……」）；没给名字就写清它的身份。人设是它唯一的身份来源，不写它就不知道自己叫什么。
3. 必须写清外观形象，写到能照着画出来：发色发型、瞳色、常穿的衣服、随身或身上显眼的标志物，挑最有辨识度的几样。这段是它给自己出图、被问「你长什么样」时唯一的依据，缺了每次画出来的都是另一个人。
4. 其余写性格、说话方式和该守的边界，不要写输出格式规范（纯文本、不用 Markdown、分条方式）——那些运行时会自动注入，重复写会互相打架。
5. 给出了已选的回复模式时，人设的搭话分寸必须和它一致，但不要把这些要求原样抄进去，要化成这个角色本来的性格。
6. 不要写工具用法、权限规则、拒答流程、时间注入、群聊发言者标注，这些运行时会自动补。
7. 写成连贯的一段话，不要分点、不要标题、不要 Markdown、不要代码围栏。
8. 控制在 280 字以内，宁可精准也不要堆形容词。
9. 只输出人设正文本身，不要任何前言、解释或引号包裹。`

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
			{Role: llm.RoleUser, Content: personaGenerateUserPrompt(description, payload.Name, payload.Current, payload.ResponseMode)},
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

// personaGenerateUserPrompt 拼接这次的需求。带上现有人设时是「改写」而不是「重写」，
// 否则用户微调一句话就会丢掉已经调好的其它设定。
func personaGenerateUserPrompt(description, name, current, responseMode string) string {
	var builder strings.Builder
	if name = strings.TrimSpace(name); name != "" {
		builder.WriteString("机器人的名字是「" + name + "」，必须原样写进人设第一句。\n")
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
