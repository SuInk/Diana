// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"time"
)

// 人员画像把「这个人是谁」从一堆聊天原文里固化成可展示、可复用的几栏：住在哪、
// 做什么工作、有什么生活习惯。
//
// 用固定字段而不是自由 key，是因为画像要在控制台按栏目列出来、也要在提示词里稳
// 定成一段。自由 key 只会攒成一串杂项，同一件事今天叫 job、明天叫 occupation，
// 既排不出栏目也去不掉重复。需要自由粒度的长期事实由结构化记忆负责（见
// StructuredMemoryItem），这里只留人身上最稳定的那几栏。
type UserPortraitField string

const (
	PortraitFieldResidence  UserPortraitField = "residence"
	PortraitFieldOccupation UserPortraitField = "occupation"
	PortraitFieldRoutine    UserPortraitField = "routine"
	PortraitFieldHabit      UserPortraitField = "habit"
	PortraitFieldInterest   UserPortraitField = "interest"
	PortraitFieldRelation   UserPortraitField = "relation"
	PortraitFieldOther      UserPortraitField = "other"
)

const (
	// PortraitSourceStated 是本人明说的；PortraitSourceInferred 是从上下文推断的，
	// 提示词里要标出来，免得机器人把推断当成对方亲口说过。
	PortraitSourceStated   = "stated"
	PortraitSourceInferred = "inferred"
	PortraitSourceManual   = "manual"

	maxPortraitValueRunes    = 60
	maxPortraitEvidenceRunes = 60
	// minPortraitConfidence 与关系评估同源：画像写错比不写更糟，宁可漏。
	minPortraitConfidence = 0.75
)

// PortraitFieldSpec 描述一栏画像。Capacity 是这一栏最多留几条。
type PortraitFieldSpec struct {
	Field    UserPortraitField `json:"field"`
	Label    string            `json:"label"`
	Hint     string            `json:"hint"`
	Capacity int               `json:"capacity"`
}

// portraitFieldSpecs 同时是展示顺序、容量表和提示词里的字段说明，改一处即可。
//
// 容量为 1 的栏天然表现为「新值覆盖旧值」：画像描述的是现在的这个人，搬了家、
// 换了工作之后旧值不该和新值并排留着。可以同时成立多条的栏（习惯、爱好、关系）
// 才给多个位置。
var portraitFieldSpecs = []PortraitFieldSpec{
	{Field: PortraitFieldResidence, Label: "居住地点", Hint: "常住的城市或地区，不要记具体门牌地址", Capacity: 1},
	{Field: PortraitFieldOccupation, Label: "职业", Hint: "职业、行业或在读身份", Capacity: 1},
	{Field: PortraitFieldRoutine, Label: "作息", Hint: "长期的起居和活跃时段", Capacity: 1},
	{Field: PortraitFieldHabit, Label: "生活习惯", Hint: "饮食、运动、通勤等稳定的生活方式", Capacity: 4},
	{Field: PortraitFieldInterest, Label: "兴趣爱好", Hint: "长期的爱好、常玩的游戏、追的领域", Capacity: 4},
	{Field: PortraitFieldRelation, Label: "家庭与关系", Hint: "同住的家人、宠物等稳定关系", Capacity: 3},
	{Field: PortraitFieldOther, Label: "其他", Hint: "上面几栏装不下、但确实稳定的个人情况", Capacity: 4},
}

// UserPortraitTrait 是画像里的一条。Label 一起存下来，控制台和提示词就不必各自
// 再维护一份字段到中文的映射。
type UserPortraitTrait struct {
	Field      UserPortraitField `json:"field"`
	Label      string            `json:"label"`
	Value      string            `json:"value"`
	Evidence   string            `json:"evidence,omitempty"`
	Confidence float64           `json:"confidence,omitempty"`
	Source     string            `json:"source,omitempty"`
	UpdatedAt  time.Time         `json:"updated_at,omitempty"`
}

// PortraitFieldSpecs 返回画像字段表副本。
func PortraitFieldSpecs() []PortraitFieldSpec {
	return append([]PortraitFieldSpec(nil), portraitFieldSpecs...)
}

// PortraitFieldIDs 按展示顺序返回字段 ID，供工具 schema 和提示词枚举。
func PortraitFieldIDs() []string {
	ids := make([]string, 0, len(portraitFieldSpecs))
	for _, spec := range portraitFieldSpecs {
		ids = append(ids, string(spec.Field))
	}
	return ids
}

// NormalizePortraitField 归一化字段名。认中文栏目名和几种常见英文写法，认不出来
// 就落到「其他」——模型偶尔会自造字段名，丢掉整条不如收进杂项栏。
func NormalizePortraitField(raw string) (UserPortraitField, bool) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return "", false
	}
	for _, spec := range portraitFieldSpecs {
		if value == string(spec.Field) || raw == spec.Label {
			return spec.Field, true
		}
	}
	switch value {
	case "location", "city", "home", "居住地", "住址", "所在地":
		return PortraitFieldResidence, true
	case "job", "work", "career", "profession", "工作":
		return PortraitFieldOccupation, true
	case "schedule", "sleep", "生活作息":
		return PortraitFieldRoutine, true
	case "habit", "lifestyle", "diet", "习惯":
		return PortraitFieldHabit, true
	case "hobby", "hobbies", "interests", "爱好", "兴趣":
		return PortraitFieldInterest, true
	case "family", "pet", "relationship", "家庭", "宠物":
		return PortraitFieldRelation, true
	}
	return PortraitFieldOther, true
}

func portraitFieldSpec(field UserPortraitField) (PortraitFieldSpec, bool) {
	for _, spec := range portraitFieldSpecs {
		if spec.Field == field {
			return spec, true
		}
	}
	return PortraitFieldSpec{}, false
}

// PortraitFieldLabel 返回字段的中文栏目名。
func PortraitFieldLabel(field UserPortraitField) string {
	if spec, ok := portraitFieldSpec(field); ok {
		return spec.Label
	}
	return string(field)
}

// NormalizePortraitTrait 校验并整理一条画像，同时补上栏目名。置信度不足、字段或
// 正文为空的直接丢弃：画像是要被当成事实用的，宁可空着。
func NormalizePortraitTrait(trait UserPortraitTrait, now time.Time) (UserPortraitTrait, bool) {
	field, ok := NormalizePortraitField(string(trait.Field))
	if !ok {
		return UserPortraitTrait{}, false
	}
	value := strings.Join(strings.Fields(trait.Value), " ")
	if value == "" {
		return UserPortraitTrait{}, false
	}
	if trait.Confidence > 0 && trait.Confidence < minPortraitConfidence {
		return UserPortraitTrait{}, false
	}
	source := strings.ToLower(strings.TrimSpace(trait.Source))
	switch source {
	case PortraitSourceStated, PortraitSourceInferred, PortraitSourceManual:
	default:
		source = PortraitSourceInferred
	}
	updatedAt := trait.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = now
	}
	return UserPortraitTrait{
		Field:      field,
		Label:      PortraitFieldLabel(field),
		Value:      truncateRunesFromStart(value, maxPortraitValueRunes),
		Evidence:   truncateRunesFromStart(strings.Join(strings.Fields(trait.Evidence), " "), maxPortraitEvidenceRunes),
		Confidence: trait.Confidence,
		Source:     source,
		UpdatedAt:  updatedAt.UTC(),
	}, true
}

// MergePortraitTraits 把新观察并进已有画像，返回排好序的完整画像。
//
// 同一栏里出现相同的值只刷新时间和证据；栏位满了就挤掉最旧的一条。结果按
// portraitFieldSpecs 的顺序分栏，栏内新的在前。
func MergePortraitTraits(existing []UserPortraitTrait, incoming []UserPortraitTrait, now time.Time) []UserPortraitTrait {
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	merged := make([]UserPortraitTrait, 0, len(existing)+len(incoming))
	for _, trait := range existing {
		if normalized, ok := NormalizePortraitTrait(trait, now); ok {
			merged = append(merged, normalized)
		}
	}
	for _, raw := range incoming {
		trait, ok := NormalizePortraitTrait(raw, now)
		if !ok {
			continue
		}
		replaced := false
		for index := range merged {
			if merged[index].Field != trait.Field || !equivalentPortraitValue(merged[index].Value, trait.Value) {
				continue
			}
			// 同一件事又被说了一遍：保留先记下来的措辞，只把时间、证据和置信
			// 度往上刷，免得同义改写被当成新事实顶掉别的条目。
			merged[index].UpdatedAt = trait.UpdatedAt
			if trait.Evidence != "" {
				merged[index].Evidence = trait.Evidence
			}
			if trait.Confidence > merged[index].Confidence {
				merged[index].Confidence = trait.Confidence
			}
			if trait.Source == PortraitSourceStated || trait.Source == PortraitSourceManual {
				merged[index].Source = trait.Source
			}
			replaced = true
			break
		}
		if !replaced {
			merged = append(merged, trait)
		}
	}
	return sortAndCapPortraitTraits(merged)
}

// RemovePortraitField 清空一栏画像，用于本人或主人要求「别记这个」。
func RemovePortraitField(existing []UserPortraitTrait, field UserPortraitField) ([]UserPortraitTrait, int) {
	kept := make([]UserPortraitTrait, 0, len(existing))
	removed := 0
	for _, trait := range existing {
		if trait.Field == field {
			removed++
			continue
		}
		kept = append(kept, trait)
	}
	return sortAndCapPortraitTraits(kept), removed
}

// sortAndCapPortraitTraits 按栏目顺序重排，并对每栏施加容量上限。
func sortAndCapPortraitTraits(traits []UserPortraitTrait) []UserPortraitTrait {
	out := make([]UserPortraitTrait, 0, len(traits))
	for _, spec := range portraitFieldSpecs {
		bucket := make([]UserPortraitTrait, 0, spec.Capacity+1)
		for _, trait := range traits {
			if trait.Field == spec.Field {
				bucket = append(bucket, trait)
			}
		}
		// 新的在前：栏位满了挤掉的就是最旧的那条。
		sortPortraitBucketNewestFirst(bucket)
		if len(bucket) > spec.Capacity {
			bucket = bucket[:spec.Capacity]
		}
		out = append(out, bucket...)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sortPortraitBucketNewestFirst(bucket []UserPortraitTrait) {
	for i := 1; i < len(bucket); i++ {
		for j := i; j > 0 && bucket[j].UpdatedAt.After(bucket[j-1].UpdatedAt); j-- {
			bucket[j], bucket[j-1] = bucket[j-1], bucket[j]
		}
	}
}

func equivalentPortraitValue(left, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}

// FormatPortraitLines 把画像写成提示词里的几行。inferred 的条目标注出来，机器人
// 才不会把推断当成对方亲口说过的话。
func FormatPortraitLines(traits []UserPortraitTrait) string {
	if len(traits) == 0 {
		return ""
	}
	var builder strings.Builder
	for _, spec := range portraitFieldSpecs {
		values := make([]string, 0, spec.Capacity)
		for _, trait := range traits {
			if trait.Field != spec.Field || strings.TrimSpace(trait.Value) == "" {
				continue
			}
			value := trait.Value
			if trait.Source == PortraitSourceInferred {
				value += "（推断）"
			}
			values = append(values, value)
		}
		if len(values) == 0 {
			continue
		}
		builder.WriteString("\n- ")
		builder.WriteString(spec.Label)
		builder.WriteString("：")
		builder.WriteString(strings.Join(values, "；"))
	}
	return builder.String()
}
