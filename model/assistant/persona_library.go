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
// 关键的一条：**人设库是套用来源，不是活绑定。** 选一套就把这四个字段填进表单，
// 之后跑的就是表单里的值；库里那份改了不会偷偷影响已经配好的机器人。
// 反过来做（配置只存一个 persona_id，运行时再去库里取）看着更"省事"，但会得到
// 「界面上的人设框里写着 A、实际发出来是 B」这种既看不见又在生效的状态——
// clearChatInFineTuning 和表达风格预设那两处都为同一件事留过教训。

// PersonaLibraryMaxEntries 限制存多少套。这是给人翻的列表，不是数据表。
const PersonaLibraryMaxEntries = 50

const (
	personaNameMaxRunes   = 40
	personaPromptMaxRunes = 4000
)

// Persona 是一套具名人设。
type Persona struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	SystemPrompt   string     `json:"system_prompt,omitempty"`
	ReplyStyle     ReplyStyle `json:"reply_style,omitempty"`
	SelfReference  string     `json:"self_reference,omitempty"`
	SentenceEnders string     `json:"sentence_enders,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at,omitempty"`
}

// PersonaSet 是整个人设库。
type PersonaSet struct {
	Personas []Persona `json:"personas"`
}

// Normalized 清洗单套人设：补 ID、裁长度、归一化风格。
func (persona Persona) Normalized() Persona {
	persona.ID = strings.TrimSpace(persona.ID)
	if persona.ID == "" {
		persona.ID = uuid.NewString()
	}
	persona.Name = truncateRunesPlain(strings.TrimSpace(persona.Name), personaNameMaxRunes)
	persona.SystemPrompt = truncateRunesPlain(strings.TrimSpace(persona.SystemPrompt), personaPromptMaxRunes)
	if strings.TrimSpace(string(persona.ReplyStyle)) != "" {
		persona.ReplyStyle = persona.ReplyStyle.Normalized()
	}
	persona.SelfReference = strings.TrimSpace(persona.SelfReference)
	persona.SentenceEnders = strings.TrimSpace(persona.SentenceEnders)
	return persona
}

// Empty 报告这套人设是不是什么都没填。名字不算内容——只有名字的空壳留着没意义。
func (persona Persona) Empty() bool {
	return strings.TrimSpace(persona.SystemPrompt) == "" &&
		strings.TrimSpace(string(persona.ReplyStyle)) == "" &&
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
}

// sameContent 比四项正文,不比 ID 和时间:判断「这套是不是已经有了」跟它什么时候
// 存的、在别人机器上是什么 ID 无关。
func (persona Persona) sameContent(other Persona) bool {
	return persona.SystemPrompt == other.SystemPrompt &&
		persona.ReplyStyle.Normalized() == other.ReplyStyle.Normalized() &&
		persona.SelfReference == other.SelfReference &&
		persona.SentenceEnders == other.SentenceEnders
}

// Import 把外部来的几套人设并进库里。
//
// 一律分配新 ID,不复用文件里的:那些 ID 来自别人的机器,撞上本地已有条目就会变成
// 静默覆盖——导入一个文件把自己调了半天的人设冲掉,是这种功能最不能出的事。
// 同名冲突改名而不是覆盖,同样为了这个:导入只增不减。
func (set PersonaSet) Import(incoming []Persona, now time.Time) (PersonaSet, PersonaImportResult) {
	set = set.WithDefaults()
	var result PersonaImportResult
	for _, persona := range incoming {
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
