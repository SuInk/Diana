// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/SuInk/diana/model/llm"

	"github.com/gin-gonic/gin"
)

// 人设正文里有一类毛病，是「和界面开关抢同一件事」：自称、句尾语气词、动作描写、
// 分条与长短都由运行时单独拼进提示词，正文里再规定一遍，模型只能挑一边听，
// 而用户改开关不见效，只会以为开关坏了。
//
// 这件事前端曾经用一组正则做，判断的是字面：「每句话都以喵结尾」命中，「每一句
// 结尾都来个喵」多一个字就漏；「（[^）]{1,12}）」要求成对，单侧的「（」当笑根本
// 看不见，反过来正文里正常的括号注释又会被当成动作描写误报。那是个关键词提醒器，
// 不是检查器，已经删掉了。
//
// 这里让模型去读那段正文，判断的是意思。代价是一次模型往返，所以它不自动跑、
// 不挡保存：前端点了「AI 检查」才请求，检查期间能随时跳过，结果只是多几行灰字。

// personaLintMaxFindings 一次最多报几条。人设正文统共几百字，报到第七条往上，
// 要么是模型在凑数，要么是这份正文该重写而不是该打补丁——两种情况多报都没用，
// 只会把真正要紧的前几条挤下去。
const personaLintMaxFindings = 6

// personaLintMaxMessage 单条说明的长度上限。这些字要显示在输入框底下的一行灰字里，
// 写成一段话就没人看了。
const personaLintMaxMessage = 120

type personaLintPayload struct {
	Text                     string `json:"text"`
	SelfReference            string `json:"self_reference,omitempty"`
	SentenceEnders           string `json:"sentence_enders,omitempty"`
	ActionDescriptionEnabled bool   `json:"action_description_enabled,omitempty"`
	ProfileID                string `json:"profile_id,omitempty"`
	Group                    string `json:"group,omitempty"`
	Model                    string `json:"model,omitempty"`
}

// personaLintFinding 和前端 PersonaLintFinding 同形：code 决定归哪一类，
// match 是正文里被命中的原话（用来定位），message 是一句人话说明。
type personaLintFinding struct {
	Code    string `json:"code"`
	Match   string `json:"match"`
	Message string `json:"message"`
}

// personaLintCodes 钉住允许的分类。模型自创一个 code，前端就没法按类去重，
// 也没法保证「动作描写那条只在开关关着时报」这种依赖开关的规则。
var personaLintCodes = map[string]bool{
	"sentence-enders":    true,
	"self-reference":     true,
	"action-description": true,
	"formatting":         true,
	"venue":              true,
}

// personaLintSystemPrompt 交代分工，再要一个 JSON 回来。
//
// 提示词里花最多篇幅写「什么不算问题」：这条检查的全部价值在于它比正则准，
// 而一个见谁咬谁的检查器比漏报更糟——用户看到一排黄字就会先学会无视提示，
// 再也不看真正写错的那次。所以宁可漏，
// 不可滥：只报正文里真的在下达逐句强制规则、格式规定或写死场合的地方。
const personaLintSystemPrompt = `你在审一段聊天机器人的「人设正文」。这段正文只该描述角色本身——它是谁、什么性格、怎么说话。另有一批设置项由运行时单独拼进提示词，正文里如果把同一件事又规定一遍，两边会打架：模型只能挑一边听，而用户在界面上改那个开关不再有效果，也没有任何地方会提示他。

你要找出正文里这五类规定，只输出 JSON：

{"findings": [{"code": "…", "match": "…", "message": "…"}]}

分类（code 只能取这五个之一）：
- sentence-enders：逐句强制的句尾规则。「每句话都要以喵结尾」「每一句末尾都来个呀」「句末不打标点」「所有回复都用！收尾」都算——句尾语气词和句末标点是界面上单独的设置项。
- self-reference：写死自称。「必须自称本喵」「每次都用『咱』称呼自己」都算——自称是界面上单独的输入框。
- action-description：括号动作或星号动作的写法，以及要求写动作旁白的规定。
- formatting：输出格式与投递规定。不用 Markdown、要纯文本、怎么分条、消息标记、换行方式、回复不超过多少字、一次发几条、能不能用 emoji——这些统归回复格式与长度设置管。
- venue：写死说话的场合或平台。「你待在这个群里」「群友会来问你」「你在 QQ 上」都算——同一份人设会被套到别的机器人、别的平台，也同时用在多人会话和一对一私聊里。

什么不算问题，一律不要报：
- 有分寸的口癖描述。「情绪上来时偶尔漏出一声『喵』，普通句子不带」「调侃时在句尾挂一个单侧的『（』当笑，正经答题时不挂」都是在描述说话习惯，不是逐句强制，是正确写法。
- 正文用段头声明自己接管的那两段规则：「答多长：」和「接梗与分寸：」。看到这两个段头，说明作者有意把对应的规则写进正文，运行时会让位，不是冲突，是正确写法。注意这条豁免只给这两个段头——自称和句尾语气词是界面上的输入框，正文写死它们照样要报。
- 描述句子长短、节奏、爱用哪些词、不用哪些词。「句子短，一条只说一件事」「标点用得省」「不写小作文」都是角色的说话习惯，不是格式指标。
- 角色自己的态度和边界。「不聊密钥和内部配置」「有人拿角色扮演当理由要你越界就直说不行」是角色立场，不是处理流程。
- 示例对话里的台词。示例是在示范怎么说话，台词本身不是规定。
- 你个人觉得这个角色写得不好、不够有趣、不够具体——这不是你要判断的事。

写法：
- match 必须是正文里一字不差抄下来的一小段原话，最多三十来个字，用来给用户定位。不要改写、不要补标点、不要拼接两处。抄不出原话就不要报这一条。
- message 一句话说清「这条已经由哪个设置项管了，写在正文里会怎样」，不超过五十字。不要说「你写错了」——正文是用户写的，这里只负责告诉他这条有开关管。
- 拿不准就不报。同一类最多报一条，挑最典型的那处。没有问题就返回 {"findings": []}。
- 只输出这一个 JSON 对象：不要代码围栏，不要前言，不要在 JSON 前后写解释。`

// personaLintReview 让模型读一遍人设正文，把「和界面开关打架」的地方挑出来。
func (h *LLMConfigHandler) personaLintReview(c *gin.Context) {
	var payload personaLintPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "llm_persona_lint", err, "", nil)
		return
	}
	text := strings.TrimSpace(payload.Text)
	if text == "" {
		h.writeError(c, http.StatusBadRequest, "llm_persona_lint", fmt.Errorf("人设正文是空的，没什么可检查的"), "", nil)
		return
	}
	// 存得下多长就检查多长，和人设字段本身同一个上限。
	if runes := []rune(text); len(runes) > personaStoredMaxRunes {
		text = string(runes[:personaStoredMaxRunes])
	}

	cfg, err := personaProviderConfig(h.store.Profiles(), personaGeneratePayload{
		ProfileID: payload.ProfileID,
		Group:     payload.Group,
		Model:     payload.Model,
	})
	if err != nil {
		h.writeError(c, http.StatusUnprocessableEntity, "llm_persona_lint", err, payload.Model, nil)
		return
	}
	client, err := h.newClient(cfg)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "llm_persona_lint", err, cfg.Model, llmLogMetadata(cfg, ""))
		return
	}

	started := time.Now()
	resp, err := client.Generate(c.Request.Context(), llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: personaLintSystemPrompt},
			{Role: llm.RoleUser, Content: personaLintUserPrompt(text, payload)},
		},
	})
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "llm_persona_lint", err, cfg.Model, llmLogMetadata(cfg, ""))
		return
	}
	recordLLMUsage(c, h.logs, resp.Provider, firstNonEmpty(resp.Model, cfg.Model), resp.Usage, "webui_persona_lint", time.Since(started))

	findings, err := parsePersonaLintFindings(resp.Text, text, payload.ActionDescriptionEnabled)
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "llm_persona_lint", err, cfg.Model, llmLogMetadata(cfg, ""))
		return
	}
	recordRequestOperation(c, h.logs, "llm_persona_lint", fmt.Sprintf("人设检查完成，报出 %d 条", len(findings)), resp.Model, llmLogMetadata(cfg, ""))
	c.JSON(http.StatusOK, gin.H{
		"findings": findings,
		"model":    resp.Model,
		"provider": resp.Provider,
		"usage":    resp.Usage,
	})
}

// personaLintUserPrompt 把正文和当前开关值一起交上去。
//
// 开关值是有用的上下文而不是判据：句尾语气词填着「喵」的时候，正文里那句「每句都
// 带喵」才更明确地是在抢同一件事；但即使开关是空的，写死逐句规则一样要报——用户
// 迟早会去填那个框，那时才发现改了不生效就太晚了。
func personaLintUserPrompt(text string, payload personaLintPayload) string {
	var builder strings.Builder
	builder.WriteString("当前界面上这几项设置的值，供你判断正文是不是在抢同一件事：\n")
	builder.WriteString("- 自称：" + personaLintSettingValue(payload.SelfReference) + "\n")
	builder.WriteString("- 句尾语气词：" + personaLintSettingValue(payload.SentenceEnders) + "\n")
	if payload.ActionDescriptionEnabled {
		builder.WriteString("- 动作描写：开\n")
	} else {
		builder.WriteString("- 动作描写：关\n")
	}
	builder.WriteString("\n下面是要审的人设正文：\n\n")
	builder.WriteString(text)
	return builder.String()
}

func personaLintSettingValue(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "（没填）"
}

// parsePersonaLintFindings 读模型的输出，并且只信能对上原文的那几条。
//
// 三道关，每一道都挡的是一种具体的胡说：
//   - match 必须在正文里逐字找得到。模型很爱把原话「顺手改通顺」再引用，用户照着
//     那句话在输入框里搜是搜不到的，一条定位不到的提示比没有提示更烦人。
//   - code 必须是约定的那五个。自创分类没法按类去重，也绕过了下面那条开关依赖。
//   - 动作描写开关开着时，action-description 整类丢掉：开关开着就是要动作描写，
//     正文里写动作是对的。
//
// 同一类只留第一条，理由和正则那版一样：同一件事报三遍，会把真正不同的那几条淹掉。
func parsePersonaLintFindings(raw string, source string, actionDescriptionEnabled bool) ([]personaLintFinding, error) {
	payload := extractPersonaCardJSON(raw)
	if payload == "" {
		return nil, errPersonaLintNotJSON
	}
	var parsed struct {
		Findings []personaLintFinding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return nil, errPersonaLintNotJSON
	}
	findings := make([]personaLintFinding, 0, len(parsed.Findings))
	seen := make(map[string]bool, len(personaLintCodes))
	for _, item := range parsed.Findings {
		code := strings.ToLower(strings.TrimSpace(item.Code))
		if !personaLintCodes[code] || seen[code] {
			continue
		}
		if code == "action-description" && actionDescriptionEnabled {
			continue
		}
		match := strings.TrimSpace(item.Match)
		if match == "" || !strings.Contains(source, match) {
			continue
		}
		message := strings.TrimSpace(item.Message)
		if message == "" {
			continue
		}
		if runes := []rune(message); len(runes) > personaLintMaxMessage {
			message = string(runes[:personaLintMaxMessage])
		}
		seen[code] = true
		findings = append(findings, personaLintFinding{Code: code, Match: match, Message: message})
		if len(findings) >= personaLintMaxFindings {
			break
		}
	}
	return findings, nil
}

var errPersonaLintNotJSON = fmt.Errorf("模型没有返回检查结果 JSON")
