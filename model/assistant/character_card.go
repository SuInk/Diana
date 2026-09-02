// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// SillyTavern 角色卡导入：把一张卡拆成 Diana 认识的两样东西——人设（它是谁、
// 怎么说话）和世界书条目（它活在什么样的世界里）。
//
// 卡有三种载体：V1 是顶层平铺的 JSON，V2/V3 把字段收进 data 里，另外一大半卡
// 是 PNG——JSON 以 base64 藏在图片的 tEXt 块里（V2 用关键字 chara，V3 用 ccv3）。
// 三种都认，用户从卡站下载什么就能导什么，不用先转格式。
//
// 折算的原则是「搬语义，不搬格式」：description/personality/scenario/示例对话
// 拼成一份人设正文，{{char}}/{{user}} 宏就地替换；character_book 走世界书那条
// 现成的导入路径。greeting 的多个备选、作者注释、post_history_instructions
// 这些和 Diana 的运行方式对不上的字段不硬塞——塞进人设正文只会稀释真正的设定。

// characterCardMaxDecodedBytes 限制解码后的卡体积。正常的卡几十 KB，PNG 里的
// JSON 也不过如此；超过这个数的不是卡，是拿导入接口当网盘。
const characterCardMaxDecodedBytes = 8 << 20

// CharacterCardImport 是一张角色卡解析出来的结果，尚未入库。
type CharacterCardImport struct {
	// Persona 由卡的文本字段拼成，ID 为空，由导入方分配。
	Persona Persona
	// BookNodes 是卡内嵌 character_book 的条目，可能为空。
	BookNodes []WorldBookNode
	// BookName 是内嵌世界书的名字，界面提示用。
	BookName string
}

// sillyTavernCardData 是卡的字段集。V1 在顶层，V2/V3 在 data 下，字段名一致。
type sillyTavernCardData struct {
	Name          string `json:"name,omitempty"`
	Description   string `json:"description,omitempty"`
	Personality   string `json:"personality,omitempty"`
	Scenario      string `json:"scenario,omitempty"`
	FirstMes      string `json:"first_mes,omitempty"`
	MesExample    string `json:"mes_example,omitempty"`
	SystemPrompt  string `json:"system_prompt,omitempty"`
	CharacterBook *struct {
		Name    string          `json:"name,omitempty"`
		Entries json.RawMessage `json:"entries,omitempty"`
	} `json:"character_book,omitempty"`
}

type sillyTavernCardEnvelope struct {
	Spec string               `json:"spec,omitempty"`
	Data *sillyTavernCardData `json:"data,omitempty"`
	sillyTavernCardData
}

// ParseSillyTavernCharacterCard 解析一张角色卡，输入可以是 JSON 或 PNG 原始字节。
func ParseSillyTavernCharacterCard(raw []byte) (CharacterCardImport, error) {
	if len(raw) > characterCardMaxDecodedBytes {
		return CharacterCardImport{}, errCharacterCardTooLarge
	}
	if bytes.HasPrefix(raw, pngSignature) {
		embedded, err := characterCardFromPNG(raw)
		if err != nil {
			return CharacterCardImport{}, err
		}
		raw = embedded
	}
	var envelope sillyTavernCardEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(raw), &envelope); err != nil {
		return CharacterCardImport{}, errCharacterCardUnrecognized
	}
	// V2/V3 的字段在 data 里；V1 在顶层。data 里有名字就信 data，两头都有时
	// data 是新版写入的那份。
	data := envelope.sillyTavernCardData
	if envelope.Data != nil && strings.TrimSpace(envelope.Data.Name) != "" {
		data = *envelope.Data
	}
	if strings.TrimSpace(data.Name) == "" && strings.TrimSpace(data.Description) == "" {
		return CharacterCardImport{}, errCharacterCardEmpty
	}

	result := CharacterCardImport{
		Persona: Persona{
			Name:         firstNonEmpty(strings.TrimSpace(data.Name), "未命名角色"),
			SystemPrompt: composeCharacterCardPrompt(data),
		},
	}
	if data.CharacterBook != nil && len(data.CharacterBook.Entries) > 0 {
		if nodes, ok := WorldBookNodesFromSillyTavern(data.CharacterBook.Entries); ok {
			result.BookNodes = nodes
			result.BookName = strings.TrimSpace(data.CharacterBook.Name)
		}
	}
	return result, nil
}

// composeCharacterCardPrompt 把卡的文本字段拼成人设正文。
//
// 按重要性排：卡自带的 system_prompt、名字加简介、性格、场景、示例对话、开场白。
// 正文有长度上限（personaPromptMaxRunes），装不下时整段整段地放弃后面的——
// 从中间截断一段示例对话，比干脆没有示例更误导。
func composeCharacterCardPrompt(data sillyTavernCardData) string {
	name := firstNonEmpty(strings.TrimSpace(data.Name), "未命名角色")
	expand := func(text string) string {
		return expandCharacterCardMacros(text, name)
	}
	sections := make([]string, 0, 6)
	if prompt := strings.TrimSpace(data.SystemPrompt); prompt != "" {
		sections = append(sections, expand(prompt))
	}
	intro := "你是" + name + "。"
	if description := strings.TrimSpace(data.Description); description != "" {
		intro += "\n" + expand(description)
	}
	sections = append(sections, intro)
	if personality := strings.TrimSpace(data.Personality); personality != "" {
		sections = append(sections, "性格与特质："+expand(personality))
	}
	if scenario := strings.TrimSpace(data.Scenario); scenario != "" {
		sections = append(sections, "场景与背景："+expand(scenario))
	}
	if examples := formatCharacterCardExamples(data.MesExample, name); examples != "" {
		sections = append(sections, "对话示例（只用来把握语气和口癖，不要照抄）：\n"+examples)
	}
	if firstMes := strings.TrimSpace(data.FirstMes); firstMes != "" {
		sections = append(sections, "开场白参考（初次对话时可以化用它的语气，不必逐字复述）：\n"+expand(firstMes))
	}

	var builder strings.Builder
	for _, section := range sections {
		candidate := section
		if builder.Len() > 0 {
			candidate = "\n\n" + section
		}
		if len([]rune(builder.String()+candidate)) > personaPromptMaxRunes {
			// 连「名字加简介」都装不下的卡只能硬裁；后面的段落整段放弃。
			if builder.Len() == 0 {
				return truncateRunesPlain(section, personaPromptMaxRunes)
			}
			break
		}
		builder.WriteString(candidate)
	}
	return builder.String()
}

// formatCharacterCardExamples 整理示例对话：去掉 <START> 分隔符、替换宏。
func formatCharacterCardExamples(examples string, name string) string {
	examples = strings.TrimSpace(examples)
	if examples == "" {
		return ""
	}
	examples = characterCardStartMarker.ReplaceAllString(examples, "\n")
	examples = expandCharacterCardMacros(examples, name)
	lines := make([]string, 0, 16)
	for _, line := range strings.Split(examples, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

var (
	characterCardStartMarker = regexp.MustCompile(`(?i)<START>`)
	characterCardCharMacro   = regexp.MustCompile(`(?i)\{\{char\}\}`)
	characterCardUserMacro   = regexp.MustCompile(`(?i)\{\{user\}\}`)
)

// expandCharacterCardMacros 展开酒馆宏：{{char}} 是角色自己，{{user}} 在群聊
// 语境下没有固定对象，统一写成「对方」。其他宏（{{random}}、{{time}} 之类）
// 出现频率低且没有对应机制，原样保留比错误展开安全。
func expandCharacterCardMacros(text string, name string) string {
	text = characterCardCharMacro.ReplaceAllString(text, name)
	return characterCardUserMacro.ReplaceAllString(text, "对方")
}

var pngSignature = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

// characterCardFromPNG 从 PNG 的 tEXt 块里取出内嵌的卡 JSON。
//
// V3 的 ccv3 优先于 V2 的 chara：两块同时存在时 ccv3 是新版写入的那份。
// 只按块结构线性扫过去，不解码像素——坏图或非标准块只会导致找不到卡，不会崩。
func characterCardFromPNG(raw []byte) ([]byte, error) {
	texts := map[string][]byte{}
	offset := len(pngSignature)
	for offset+12 <= len(raw) {
		length := int(binary.BigEndian.Uint32(raw[offset : offset+4]))
		chunkType := string(raw[offset+4 : offset+8])
		dataStart := offset + 8
		if length < 0 || dataStart+length+4 > len(raw) {
			break
		}
		if chunkType == "tEXt" {
			data := raw[dataStart : dataStart+length]
			if separator := bytes.IndexByte(data, 0); separator > 0 {
				keyword := strings.ToLower(string(data[:separator]))
				if keyword == "chara" || keyword == "ccv3" {
					texts[keyword] = data[separator+1:]
				}
			}
		}
		if chunkType == "IEND" {
			break
		}
		offset = dataStart + length + 4
	}
	encoded, ok := texts["ccv3"]
	if !ok {
		encoded, ok = texts["chara"]
	}
	if !ok {
		return nil, errCharacterCardPNGWithoutCard
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		// 个别写卡工具不带填充，宽松解一次再放弃。
		decoded, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
		if err != nil {
			return nil, fmt.Errorf("角色卡内嵌数据解码失败: %w", err)
		}
	}
	if len(decoded) > characterCardMaxDecodedBytes {
		return nil, errCharacterCardTooLarge
	}
	return decoded, nil
}

var (
	errCharacterCardUnrecognized   = errors.New("无法识别的角色卡文件：既不是角色卡 JSON，也不是内嵌卡的 PNG")
	errCharacterCardEmpty          = errors.New("这张卡没有名字和简介，无法生成人设")
	errCharacterCardPNGWithoutCard = errors.New("这张 PNG 里没有内嵌角色卡数据")
	errCharacterCardTooLarge       = errors.New("角色卡文件过大")
)
