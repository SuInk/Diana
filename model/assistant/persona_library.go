// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// 人设库：把「它是谁、怎么说话」存成具名的几套，随时换。
//
// 这里存的是四项：基础人设正文、表达风格、自称、句尾语气词。它们合起来才是一个
// 角色——只存正文的话，换回猫娘还得自己记得把风格也调回去。
//
// 回复模式（搭话频率）不在里面：那是「这台机器人在这个群里多主动」，跟它是谁无关。
// 同一套人设放在办公群和水群，该有不同的搭话频率。
//
// 关键的一条：**运行时只看配置里存的值，不去库里取。** 选一套就把这些字段填进
// 配置，之后跑的就是配置里的值。反过来做（配置只存一个 persona_id，运行时再去库里
// 取）看着更"省事"，但会得到「界面上的人设框里写着 A、实际发出来是 B」这种既看
// 不见又在生效的状态——clearChatInFineTuning 和表达风格预设那两处都为同一件事
// 留过教训。
//
// 机器人和群可以绑定库里的一套（persona_id）：库保存的那一刻把新内容写进每个绑定
// 它的配置，所以「库里改了自动更新」和「配置里存的就是在跑的」两条同时成立。
// 在界面上改了人设内容就解除绑定。见 persona_link.go。

// PersonaLibraryMaxEntries 限制存多少套。这是给人翻的列表，不是数据表。
const PersonaLibraryMaxEntries = 50

const (
	personaNameMaxRunes   = 40
	personaPromptMaxRunes = 4000
)

// Persona 是一套具名人设。
type Persona struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	SystemPrompt string `json:"system_prompt,omitempty"`
	// Soul 是品格层：身份、价值、硬边界。正文说「怎么说话」，它说「是什么、
	// 珍视什么、为什么」。分群覆盖动不了它，见 persona_soul.go。
	Soul *PersonaSoul `json:"soul,omitempty"`
	// Voice 只在导入时出现：YAML 里把表达层写成一个块，清洗时摊平进下面那几个
	// 字段。摊平而不是新开一层，是为了让老人设和新写法在运行时完全一样。
	Voice *PersonaVoice `json:"voice,omitempty"`
	// PersonaMode 跟着正文走：一份接管模式的正文（带段头）只在接管档下成立，
	// 套到填空题档上，段头不生效而运行时照旧注入，同一件事说两遍。
	PersonaMode              PersonaMode `json:"persona_mode,omitempty"`
	ReplyStyle               ReplyStyle  `json:"reply_style,omitempty"`
	ActionDescriptionEnabled *bool       `json:"action_description_enabled,omitempty"`
	DaypartToneEnabled       *bool       `json:"daypart_tone_enabled,omitempty"`
	SelfReference            string      `json:"self_reference,omitempty"`
	SentenceEnders           string      `json:"sentence_enders,omitempty"`
	UpdatedAt                time.Time   `json:"updated_at,omitempty"`
}

// PersonaSet 是整个人设库。
type PersonaSet struct {
	Personas []Persona `json:"personas"`
}

func copyCustomPersona(persona *Persona) *Persona {
	if persona == nil {
		return nil
	}
	copy := *persona
	copy.ActionDescriptionEnabled = copyBoolPointer(persona.ActionDescriptionEnabled)
	copy.DaypartToneEnabled = copyBoolPointer(persona.DaypartToneEnabled)
	copy.SystemPrompt = migratePersonaStyle(copy.SystemPrompt, &copy.ReplyStyle, &copy.ActionDescriptionEnabled)
	copy.Soul = persona.Soul.Clone()
	return &copy
}

// PersonaVoice 是导入格式里的表达层。它不进运行时：Normalized 会把它摊平到
// SystemPrompt / SelfReference / SentenceEnders 上。
type PersonaVoice struct {
	Style                    string            `json:"style,omitempty"`
	SelfReference            string            `json:"self_reference,omitempty"`
	SentenceEnders           string            `json:"sentence_enders,omitempty"`
	ActionDescriptionEnabled *bool             `json:"action_description_enabled,omitempty"`
	Examples                 []PersonaExchange `json:"examples,omitempty"`
}

// PersonaExchange 是一组示例对话。示例比形容词管用：「说话简短」不如给一句
// 真的简短的回答。
type PersonaExchange struct {
	User  string `json:"user"`
	Reply string `json:"reply"`
}

// flattenVoice 把 voice 块摊平到老字段上。已经填了的老字段优先，不被覆盖：
// 同一份文件里两种写法都有时，显式写在外层的那个是作者后改的。
func (persona Persona) flattenVoice() Persona {
	voice := persona.Voice
	persona.Voice = nil
	if voice == nil {
		return persona
	}
	if strings.TrimSpace(persona.SystemPrompt) == "" {
		persona.SystemPrompt = renderPersonaVoice(voice)
	}
	if strings.TrimSpace(persona.SelfReference) == "" {
		persona.SelfReference = voice.SelfReference
	}
	if strings.TrimSpace(persona.SentenceEnders) == "" {
		persona.SentenceEnders = voice.SentenceEnders
	}
	if persona.ActionDescriptionEnabled == nil {
		persona.ActionDescriptionEnabled = copyBoolPointer(voice.ActionDescriptionEnabled)
	}
	return persona
}

// renderPersonaVoice 把表达层拼成人设正文：风格描述在前，示例对话跟在后面。
func renderPersonaVoice(voice *PersonaVoice) string {
	var builder strings.Builder
	builder.WriteString(strings.TrimSpace(voice.Style))
	if len(voice.Examples) > 0 {
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString("示例——")
		for _, example := range voice.Examples {
			user := strings.TrimSpace(example.User)
			reply := strings.TrimSpace(example.Reply)
			if user == "" || reply == "" {
				continue
			}
			builder.WriteString("\n用户：" + user + "\n你：" + reply)
		}
	}
	return strings.TrimSpace(builder.String())
}

// Normalized 清洗单套人设：补 ID、摊平 voice、迁移旧风格、裁长度。
func (persona Persona) Normalized() Persona {
	persona = persona.flattenVoice()
	persona.Soul = persona.Soul.Normalized()
	persona.ActionDescriptionEnabled = copyBoolPointer(persona.ActionDescriptionEnabled)
	persona.DaypartToneEnabled = copyBoolPointer(persona.DaypartToneEnabled)
	persona.ID = strings.TrimSpace(persona.ID)
	if persona.ID == "" {
		persona.ID = uuid.NewString()
	}
	persona.Name = truncateRunesPlain(strings.TrimSpace(persona.Name), personaNameMaxRunes)
	persona.SystemPrompt = mergePersonaStyleWithinLimit(persona.SystemPrompt, &persona.ReplyStyle, &persona.ActionDescriptionEnabled)
	persona.SelfReference = strings.TrimSpace(persona.SelfReference)
	persona.SentenceEnders = strings.TrimSpace(persona.SentenceEnders)
	if persona.PersonaMode != PersonaModeOwn {
		persona.PersonaMode = ""
	}
	return persona
}

// Empty 报告这套人设是不是什么都没填。名字不算内容——只有名字的空壳留着没意义。
func (persona Persona) Empty() bool {
	return persona.Soul.Empty() &&
		strings.TrimSpace(persona.SystemPrompt) == "" &&
		strings.TrimSpace(string(persona.ReplyStyle)) == "" &&
		persona.ActionDescriptionEnabled == nil &&
		persona.DaypartToneEnabled == nil &&
		strings.TrimSpace(persona.SelfReference) == "" &&
		strings.TrimSpace(persona.SentenceEnders) == ""
}

// WithDefaults 清洗整库：去掉空条目和重复 ID，按最近更新排前面。
func (set PersonaSet) WithDefaults() PersonaSet {
	seen := make(map[string]struct{}, len(set.Personas))
	personas := make([]Persona, 0, len(set.Personas))
	for _, persona := range set.Personas {
		persona = persona.Normalized()
		if persona.Name == "" {
			continue
		}
		if _, ok := seen[persona.ID]; ok {
			continue
		}
		seen[persona.ID] = struct{}{}
		personas = append(personas, persona)
	}
	// 最近改过的排在前面：人设是反复调的东西，刚动过的那套最可能再被点开。
	sortPersonasByRecency(personas)
	if len(personas) > PersonaLibraryMaxEntries {
		personas = personas[:PersonaLibraryMaxEntries]
	}
	return PersonaSet{Personas: personas}
}

// Save 新增或更新一套人设，返回落库后的那一份。
func (set PersonaSet) Save(persona Persona, now time.Time) (PersonaSet, Persona, error) {
	persona = persona.Normalized()
	if persona.Name == "" {
		return set, Persona{}, errPersonaNameRequired
	}
	if persona.Empty() {
		return set, Persona{}, errPersonaEmpty
	}
	persona.UpdatedAt = now
	for index := range set.Personas {
		if set.Personas[index].ID == persona.ID {
			set.Personas[index] = persona
			return set.WithDefaults(), persona, nil
		}
	}
	if len(set.Personas) >= PersonaLibraryMaxEntries {
		return set, Persona{}, errPersonaLibraryFull
	}
	set.Personas = append(set.Personas, persona)
	return set.WithDefaults(), persona, nil
}

// Delete 删掉一套人设。找不到不算错：重复点删除不该报错吓人。
func (set PersonaSet) Delete(id string) PersonaSet {
	id = strings.TrimSpace(id)
	if id == "" {
		return set.WithDefaults()
	}
	personas := make([]Persona, 0, len(set.Personas))
	for _, persona := range set.Personas {
		if strings.TrimSpace(persona.ID) == id {
			continue
		}
		personas = append(personas, persona)
	}
	return PersonaSet{Personas: personas}.WithDefaults()
}

// Find 按 ID 取一套人设。
func (set PersonaSet) Find(id string) (Persona, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Persona{}, false
	}
	for _, persona := range set.Personas {
		if strings.TrimSpace(persona.ID) == id {
			return persona, true
		}
	}
	return Persona{}, false
}

// mergePersonaStyleWithinLimit 迁移旧风格并保证结果不超长——顺序和以前反过来。
//
// 以前是「先追加预设、再裁到 4000 字」。3600 字的正文加上 825 字的猫娘预设一裁，
// 预设正好从中间被切断：留下半行没头没尾的示例，而最后几条恰恰是刹车条款——
// 「人设只管语气，不改规则……规则优先，人设让位」和「只对主人称『主人』」。
// 越界防护被长度上限悄悄吃掉，是这里最不能出的事。
//
// 所以改成：先把用户正文裁到上限，再追加预设；追加后仍然超长的话，整段不追加。
// 宁可完全没有这段风格文案，也不要半段——半段既丢安全条款，又会在人设编辑框里
// 留下一行断句，用户看到的是自己没写过的残句。旧风格值照样被消费掉（迁移的副作用
// 都发生在 migratePersonaStyle 里，包括扮演档打开动作描写开关），不会每次读配置
// 都重试一遍。选「丢预设」而不是「再裁正文让预设塞得下」，是因为正文是用户亲手
// 写的、预设是版本自带的：要牺牲，牺牲能重新生成的那份。
func mergePersonaStyleWithinLimit(prompt string, style *ReplyStyle, actions **bool) string {
	base := strings.TrimSpace(truncateRunesPlain(strings.TrimSpace(prompt), personaPromptMaxRunes))
	merged := migratePersonaStyle(base, style, actions)
	if len([]rune(merged)) > personaPromptMaxRunes {
		return base
	}
	return merged
}

func truncateRunesPlain(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

func sortPersonasByRecency(personas []Persona) {
	sort.SliceStable(personas, func(i, j int) bool {
		left, right := personas[i].UpdatedAt, personas[j].UpdatedAt
		if !left.Equal(right) {
			return left.After(right)
		}
		return strings.ToLower(personas[i].Name) < strings.ToLower(personas[j].Name)
	})
}

var (
	errPersonaNameRequired = errors.New("assistant: persona name is required")
	errPersonaEmpty        = errors.New("assistant: persona has no content")
	errPersonaLibraryFull  = errors.New("assistant: persona library is full")
)

// PersonaImportResult 报告一次导入的去向。三个数加起来等于文件里有效条目的数量,
// 界面上要能说清楚「导进来几套、跳过几套、改名几套」——只说「导入成功」的话,
// 用户不会知道有东西被改名了。
type PersonaImportResult struct {
	Imported []Persona `json:"imported,omitempty"`
	// Skipped 是同名且四项完全一样的:同一个文件导两次不该攒出一堆副本。
	Skipped int `json:"skipped"`
	// Renamed 是同名但内容不同、被改成「名字 (2)」的。
	Renamed int `json:"renamed"`
	// Dropped 是没名字、没内容、或者超出库容量装不下的。
	Dropped int `json:"dropped"`
	// UnknownStyles reports unsupported legacy styles; persona text is preserved.
	UnknownStyles []string `json:"unknown_styles,omitempty"`
}

// sameContent 比四项正文,不比 ID 和时间:判断「这套是不是已经有了」跟它什么时候
// 存的、在别人机器上是什么 ID 无关。
func (persona Persona) sameContent(other Persona) bool {
	return persona.SystemPrompt == other.SystemPrompt &&
		boolValue(persona.ActionDescriptionEnabled, false) == boolValue(other.ActionDescriptionEnabled, false) &&
		(persona.DaypartToneEnabled == nil) == (other.DaypartToneEnabled == nil) &&
		boolValue(persona.DaypartToneEnabled, false) == boolValue(other.DaypartToneEnabled, false) &&
		persona.SelfReference == other.SelfReference &&
		persona.SentenceEnders == other.SentenceEnders &&
		persona.PersonaMode == other.PersonaMode
}

// Import 把外部来的几套人设并进库里。
//
// 一律分配新 ID,不复用文件里的:那些 ID 来自别人的机器,撞上本地已有条目就会变成
// 静默覆盖——导入一个文件把自己调了半天的人设冲掉,是这种功能最不能出的事。
// 同名冲突改名而不是覆盖,同样为了这个:导入只增不减。
func (set PersonaSet) Import(incoming []Persona, now time.Time) (PersonaSet, PersonaImportResult) {
	set = set.WithDefaults()
	var result PersonaImportResult
	seenUnknownStyle := map[string]bool{}
	for _, persona := range incoming {
		// 归一化之前先看一眼原值：Normalized() 之后就分不清「本来就没填」和
		// 「填了个不认识的」了。
		if raw := strings.TrimSpace(string(persona.ReplyStyle)); raw != "" && !knownReplyStyle(raw) && !seenUnknownStyle[raw] {
			seenUnknownStyle[raw] = true
			result.UnknownStyles = append(result.UnknownStyles, raw)
		}
		persona = persona.Normalized()
		persona.ID = uuid.NewString()
		if persona.Name == "" || persona.Empty() {
			result.Dropped++
			continue
		}
		if existing, ok := findPersonaByName(set.Personas, persona.Name); ok {
			if existing.sameContent(persona) {
				result.Skipped++
				continue
			}
			persona.Name = uniquePersonaName(set.Personas, persona.Name)
			if persona.Name == "" {
				result.Dropped++
				continue
			}
			result.Renamed++
		}
		if len(set.Personas) >= PersonaLibraryMaxEntries {
			result.Dropped++
			continue
		}
		persona.UpdatedAt = now
		set.Personas = append(set.Personas, persona)
		result.Imported = append(result.Imported, persona)
	}
	return set.WithDefaults(), result
}

func findPersonaByName(personas []Persona, name string) (Persona, bool) {
	for _, persona := range personas {
		if persona.Name == name {
			return persona, true
		}
	}
	return Persona{}, false
}

// uniquePersonaName 找一个没被占用的「名字 (n)」。名字有长度上限,加后缀前先把
// 本体裁短,免得裁剪反过来把后缀吃掉、又撞回同一个名字。
func uniquePersonaName(personas []Persona, name string) string {
	for index := 2; index < PersonaLibraryMaxEntries+2; index++ {
		suffix := " (" + strconv.Itoa(index) + ")"
		base := truncateRunesPlain(name, personaNameMaxRunes-len([]rune(suffix)))
		candidate := base + suffix
		if _, taken := findPersonaByName(personas, candidate); !taken {
			return candidate
		}
	}
	return ""
}
