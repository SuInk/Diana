// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

// SOUL.md 之前，人设散在五六个地方：正文、品格层（soul）、自称、句尾语气词、
// 动作描写开关、填空题/接管档位。运行时再把它们各拼一段进提示词。
//
// 现在只剩一份 SOUL.md。旧配置里填过的值不能丢——用户设过「本喵」、开过动作描写，
// 升级后这些就该还在——所以读配置时把它们渲染成原来运行时注入的那几段文字，
// 并进正文，然后清掉旧字段。渲染用的就是原来那几段文字，所以同一份旧配置
// 升级前后模型看到的指令一致，只是从「运行时拼」变成了「写在正文里」。
//
// 这一步只在读到旧字段时发生；存回去之后旧字段就没了，不会重复并入。

// foldLegacyPersona 把旧的品格层、自称、句尾语气词、动作描写并进 SOUL.md 正文。
// 顺序照旧运行时的注入顺序：品格在最前，正文居中，表达偏好和动作描写在后。
func foldLegacyPersona(prompt string, soul *PersonaSoul, selfReference, sentenceEnders string, actions *bool) string {
	parts := make([]string, 0, 4)
	if rendered := soul.Render(); rendered != "" {
		parts = append(parts, rendered)
	}
	if prompt = strings.TrimSpace(prompt); prompt != "" {
		parts = append(parts, prompt)
	}
	if voice := legacyVoicePrompt(selfReference, sentenceEnders); voice != "" {
		parts = append(parts, voice)
	}
	if boolValue(actions, false) {
		parts = append(parts, legacyActionDescriptionPrompt)
	}
	return strings.Join(parts, "\n\n")
}

// legacyVoicePrompt 渲染旧的自称和句尾语气词。自称只填了「我」、又没有语气词时
// 什么都不写：那是旧版的默认值，渲染出来只是一句「自称偏好是我」的废话。
func legacyVoicePrompt(selfReference, sentenceEnders string) string {
	voice := personaVoiceFrom(selfReference, sentenceEnders)
	if voice.SelfReference == "我" && len(voice.Enders) == 0 {
		return ""
	}
	return voice.prompt()
}

// legacyActionDescriptionPrompt 是旧版「动作描写」开关打开时运行时注入的那段。
const legacyActionDescriptionPrompt = "动作描写：把动作或神态放在全角括号里，可以出现在台词前、中间或结尾；一条消息里有几次真实的动作或状态变化，就可以自然穿插几处，不必每句台词都机械配一个动作。括号里只写此刻看得见的动作、视线、姿势或语气变化，每处一句话以内；不写心理独白，不替对方决定动作或反应，不用动作顶替应回答的信息，也不要铺成小说场景。每条含自然语言的回复至少写一处短动作；纯代码、命令、链接或必须逐字保留的原文不加。动作描写只是一层呈现方式，性格、称呼、语气和亲疏不因此改变。"

// personaVoice 是旧版的自称和句尾语气词，只用来把旧值渲染进正文。
type personaVoice struct {
	SelfReference string
	Enders        []string
}

const (
	// personaVoiceMaxEnders 限制候选数量。清单太长模型会挑花，也没人真需要十几个。
	personaVoiceMaxEnders = 8
	// personaVoiceMaxRunes 限制单项长度：这两项填的是「本喵」「喵~」这种词。
	personaVoiceMaxRunes = 16
)

// parsePersonaEnders 解析逗号分隔的候选清单，中英文逗号都认。
func parsePersonaEnders(raw string) []string {
	seen := make(map[string]struct{}, personaVoiceMaxEnders)
	enders := make([]string, 0, personaVoiceMaxEnders)
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '，' || r == '\n' }) {
		ender := strings.TrimSpace(part)
		if ender == "" || len([]rune(ender)) > personaVoiceMaxRunes {
			continue
		}
		if _, ok := seen[ender]; ok {
			continue
		}
		seen[ender] = struct{}{}
		enders = append(enders, ender)
		if len(enders) >= personaVoiceMaxEnders {
			break
		}
	}
	return enders
}

func personaVoiceFrom(selfReference string, sentenceEnders string) personaVoice {
	selfReference = strings.TrimSpace(selfReference)
	if len([]rune(selfReference)) > personaVoiceMaxRunes {
		selfReference = ""
	}
	return personaVoice{SelfReference: selfReference, Enders: parsePersonaEnders(sentenceEnders)}
}

func (voice personaVoice) empty() bool {
	return voice.SelfReference == "" && len(voice.Enders) == 0
}

// prompt 是旧版运行时注入的那段表达偏好，原样保留。
func (voice personaVoice) prompt() string {
	if voice.empty() {
		return ""
	}
	lines := make([]string, 0, 4)
	if voice.SelfReference != "" {
		lines = append(lines, "自称偏好是「"+voice.SelfReference+"」：需要强调自己时可以优先使用，也可以自然地用「我」或省略主语；不要求每句重复自称，不要为了用上它额外加一句话。")
	}
	if len(voice.Enders) > 0 {
		lines = append(lines, "句尾语气词偏好是："+quotePersonaEnders(voice.Enders)+"。合适时按当下语气挑选，也可以不用，或选其他符合人设的自然语气词；只有一个候选也不必每句添加。别每句都用同一个，也别为了轮换硬凑，分成多条消息后同样不必每条都带语气词。")
		lines = append(lines, "问句、感叹句里语气词放在「？」「！」前面；代码、命令、链接、报错原文照原样写，不要往里面塞语气词。")
	}
	lines = append(lines, "具体偏好以这里为准，但这些是可选表达，不是逐句必选项；自称和语气词可以独立使用，也可以都省略，以自然、贴合语境为先。")
	return strings.Join(lines, "\n")
}

func quotePersonaEnders(enders []string) string {
	quoted := make([]string, 0, len(enders))
	for _, ender := range enders {
		quoted = append(quoted, "「"+ender+"」")
	}
	return strings.Join(quoted, "、")
}
