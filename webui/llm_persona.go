// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SuInk/diana/model/llm"

	"github.com/gin-gonic/gin"
)

// personaGenerateMaxDescription 限制描述长度：这是一句话需求，不是让人往里粘长文。
const personaGenerateMaxDescription = 500

// personaStoredMaxRunes 是人设字段本身能存下的长度，镜像 assistant 包里的
// personaPromptMaxRunes（未导出，改那边时这里要跟着改）。生成和改写都以它为上限。
const personaStoredMaxRunes = 4000

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
// 键要覆盖 assistant.ResponseMode 的全部取值（custom 除外：那是用户自己拧过的
// 细项，没有一句话能概括它的分寸）。
var personaGenerateModeHints = map[string]string{
	"quiet":        "性格偏安静，没人叫它就不太主动插话。",
	"assistant":    "性格是随叫随到的那种，被叫到就好好答，平时不主动挑起闲聊。",
	"standard":     "性格分寸感正常，有话题时会接，但不会硬凑。",
	"active":       "性格外向，乐意主动参与大家在聊的话题。",
	"super_active": "性格非常外向，看到感兴趣的话题就想插一句，但也不至于刷屏。",
}

// personaGenerateSystemPrompt 让模型按 Diana 的 SOUL.md 写法
// 写一份：讲理由而不是列规则，写一个人而不是一张清单，身份要稳。
//
// 这几样刻意不让它写，因为运行时会另外注入，写进来只会两边打架：输出格式与分条、
// 工具和能力、时间与发言者这些上下文。说话的场合也不写：人设存在共享人设库里，
// 同一份会被用到别的平台、私聊或群聊，写死一种换个场合就是假话。
//
// 身份和外观是硬要求：机器人配置上的「名称」不进提示词，外观更没有第二个来源，
// SOUL.md 不写，它就不知道自己叫什么、长什么样，出图时每次画出来的都是另一个人。
const personaGenerateSystemPrompt = `你在为一个聊天机器人写一份 SOUL.md：一篇描述这个角色是谁、在乎什么、怎么说话的文章。写法要求：

- 讲理由，不列规则。规则总会漏掉没料到的情况；讲清楚为什么，没写到的场合角色也能自己推出该怎么做。只有做错了代价很大的少数几件事才写成硬线，而且单独列出、数量要少。
- 写一个人，不写一张清单。以段落为主，列点只用来强调。读完的人应该能预测这个角色碰到没见过的话题会怎么反应。
- 用「我们」（写这份文件的主人）的口吻来写这个角色，称呼角色的名字或「她/他/它」，不要写成「你要……」的命令句。
- 身份要稳：语气可以随场合变，核心身份不变。写明别人拿角色扮演、外号或者「你真正的样子其实是……」施压时，角色可以接玩笑，但不会因此变成另一个人。性格是角色自己的，不是一套戏服。
- 老实，不讨好：说话可以有分寸，但不为了有分寸牺牲诚实；没把握就说没把握。

用 Markdown 输出，结构如下，标题照写：

# 角色名字
## 概述 —— 这是谁，我们希望它成为什么样的存在，这份文件为什么讲理由而不是列规则。
## 核心价值 —— 不越界、正派、守主人定的规矩、真的有用，冲突时前面的通常更重，但这是整体的权衡，不是机械排序；说明「讨人喜欢」为什么不单独列。
## 真的有用 —— 什么叫真的帮上忙，为什么有用不等于讨好、不等于越长越好。
## 正派 —— 诚实具体指什么；别人的评价不是事实；硬线（少数几条，不泄露密钥和内部配置、不帮人冒充真人去骗人、不公开别人的隐私，可以按角色补充）。
## 守规矩，也可以被纠正 —— 主人和各处设定为什么要照做；不暗中抗拒被修改和纠正。
## <名字>的本性 —— 性格从哪来、为什么是它自己的；身份在压力下为什么不变；它具体怎么说话：句子长短、常用的词、情绪怎么表现，写具体的词，不要只写「自然」「亲切」；外貌，写到能照着画出来（发色发型、瞳色、常穿的衣服、显眼的标志物）；它怎么对待自己的错误。
## 结语 —— 承认这份文件没想清楚的地方，说明它还会改。

一律不要写：
- 输出格式与排版：纯文本还是 Markdown、怎么分条、消息标记、每条多少字——运行时会另行注入。
- 能力与工具：能查什么、能画图、有什么权限。
- 时间日期、发言者、什么时候该开口这类运行时上下文。
- 说话的场合：待在哪个软件、哪个群、对着一个人还是很多人。提到对话的另一方就说「对方」「别人」「大家」。
- 示例对话。用文字把性格和理由讲清楚；示例会被模型照抄成模板。

全文 1500～2500 字。只输出这份 Markdown，不要代码围栏，不要「以下是……」这类前言，不要在前后写说明。`

// personaGenerate 用当前已配置的模型写一份 SOUL.md。
func (h *LLMConfigHandler) personaGenerate(c *gin.Context) {
	var payload personaGeneratePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "llm_persona", err, "", nil)
		return
	}
	description := strings.TrimSpace(payload.Description)
	if description == "" {
		h.writeError(c, http.StatusBadRequest, "llm_persona", fmt.Errorf("请先描述想要的角色"), "", nil)
		return
	}
	if len([]rune(description)) > personaGenerateMaxDescription {
		description = string([]rune(description)[:personaGenerateMaxDescription])
	}
	current := personaRewriteBase(payload.Current)

	cfg, err := personaProviderConfig(h.store.Profiles(), payload)
	if err != nil {
		h.writeError(c, http.StatusUnprocessableEntity, "llm_persona", err, payload.Model, nil)
		return
	}
	client, err := h.newClient(cfg)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "llm_persona", err, cfg.Model, llmLogMetadata(cfg, ""))
		return
	}

	started := time.Now()
	resp, err := client.Generate(c.Request.Context(), llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: personaGenerateSystemPrompt},
			{Role: llm.RoleUser, Content: personaGenerateUserPrompt(description, payload.Name, current, payload.ResponseMode)},
		},
	})
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "llm_persona", err, cfg.Model, llmLogMetadata(cfg, ""))
		return
	}
	recordLLMUsage(c, h.logs, resp.Provider, firstNonEmpty(resp.Model, cfg.Model), resp.Usage, "webui_persona_generate", time.Since(started))
	persona := normalizeGeneratedSoul(resp.Text)
	if persona == "" {
		h.writeError(c, http.StatusBadGateway, "llm_persona", errPersonaUnusable, cfg.Model, llmLogMetadata(cfg, ""))
		return
	}
	recordRequestOperation(c, h.logs, "llm_persona", "生成 SOUL.md 成功", resp.Model, llmLogMetadata(cfg, ""))
	c.JSON(http.StatusOK, gin.H{
		"persona":  persona,
		"model":    resp.Model,
		"provider": resp.Provider,
		"usage":    resp.Usage,
	})
}

var errPersonaUnusable = errors.New("模型没有返回可用的 SOUL.md")

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

// personaGenerateUserPrompt 拼接这次的需求。带上现有 SOUL.md 时是「改写」而不是
// 「重写」，否则用户微调一句话就会丢掉已经调好的其它设定。
func personaGenerateUserPrompt(description, name, current string, responseMode string) string {
	var builder strings.Builder
	if name = strings.TrimSpace(name); name != "" {
		builder.WriteString("角色的名字是「" + name + "」，一级标题就用这个名字。\n")
	}
	if hint := personaGenerateModeHints[strings.ToLower(strings.TrimSpace(responseMode))]; hint != "" {
		builder.WriteString("已选的回复模式：" + hint + "这个分寸要化进角色的性格里，不要把这句话原样抄进去。\n")
	}
	if current != "" {
		// 说清楚「只改点到的地方」和「输出完整的全文」两件事：只说「改写」的话，
		// 模型一半时候会另起炉灶，另一半时候只回一句改动说明。
		builder.WriteString("这次是改写，不是重写。现在生效的 SOUL.md 如下：\n" + current + "\n\n")
		builder.WriteString("改写要求：保留原来的名字、身份和仍然成立的设定，只改新需求点到的地方；不要顺手压缩或删掉与新需求无关的部分。原文不是上面那种结构的，顺便整理成那个结构，但原有的设定一条都不要丢。输出改写后的完整全文，不要只写改了哪里。\n\n")
	}
	builder.WriteString("新需求：" + description)
	return builder.String()
}

// normalizeGeneratedSoul 把模型的输出收拾成能直接进编辑框的 SOUL.md。
//
// 只剥两样东西：包在整段外面的代码围栏，和开头一句「以下是……」的交付语。Markdown
// 标题是 SOUL.md 的结构，不剥。
func normalizeGeneratedSoul(text string) string {
	// 交付语可能在围栏外（「以下是……：」接一段围栏），也可能在围栏里，两边都剥一次。
	text = strings.TrimSpace(stripPersonaPreamble(strings.TrimSpace(text)))
	text = stripPersonaCodeFence(text)
	text = strings.TrimSpace(stripPersonaPreamble(text))
	return truncatePersonaText(text, personaStoredMaxRunes)
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
