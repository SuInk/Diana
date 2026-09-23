// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"time"
)

// 后台好感度评估的结果分类。好感与画像页的「好感度变化」只看 changed，其余几种
// 是「这句话为什么没加分」的答案，以前一条都查不到。
const (
	// RelationshipEvaluationChanged 分数按模型给的幅度变了。
	RelationshipEvaluationChanged = "changed"
	// RelationshipEvaluationCapped 模型要加减分，但分数已经到了上限或下限，实际变动比模型给的少。
	RelationshipEvaluationCapped = "capped"
	// RelationshipEvaluationUnchanged 模型判断这句话不影响关系。
	RelationshipEvaluationUnchanged = "unchanged"
	// RelationshipEvaluationLowConfidence 模型想加减分，但把握不够，按 0 处理。
	RelationshipEvaluationLowConfidence = "low_confidence"
	// RelationshipEvaluationFailed 评估调用失败、返回格式不对，或者写库失败。
	RelationshipEvaluationFailed = "failed"
	// RelationshipEvaluationSkipped 后台评估排满了，这一轮没评。
	RelationshipEvaluationSkipped = "skipped"
)

var (
	errRelationshipEvaluationSaturated = errors.New("后台评估同时进行的数量已满，这一轮跳过")
	errRelationshipEvaluationStore     = errors.New("评估完成，但好感度写入失败")
)

// relationshipEvaluationTextLimit 是记录里保留的原话长度。只是给人认出是哪句话，
// 完整内容在事件明细里。
const relationshipEvaluationTextLimit = 200

// RelationshipEvaluationRecord 是一次后台好感度评估的记录。
type RelationshipEvaluationRecord struct {
	ID            int64   `json:"id"`
	BotProfileID  string  `json:"bot_profile_id,omitempty"`
	UserID        string  `json:"user_id"`
	SenderName    string  `json:"sender_name,omitempty"`
	GroupID       string  `json:"group_id,omitempty"`
	MessageID     string  `json:"message_id,omitempty"`
	MessageText   string  `json:"message_text,omitempty"`
	Status        string  `json:"status"`
	ProposedDelta int     `json:"proposed_delta"`
	AppliedDelta  int     `json:"applied_delta"`
	BeforeScore   int     `json:"before_score"`
	AfterScore    int     `json:"after_score"`
	Confidence    float64 `json:"confidence"`
	Reason        string  `json:"reason,omitempty"`
	Model         string  `json:"model,omitempty"`
	Error         string  `json:"error,omitempty"`
	// Portrait 是同一次评估里记下的画像。好感度和画像共用一次模型调用，
	// 分数没动、只记下了「职业是程序员」的评估也是一次变化。
	Portrait  []RelationshipEvaluationPortrait `json:"portrait,omitempty"`
	CreatedAt time.Time                        `json:"created_at"`
}

// RelationshipEvaluationPortrait 是评估记录里的一条画像，只留排查需要的字段。
type RelationshipEvaluationPortrait struct {
	Field  string `json:"field"`
	Label  string `json:"label"`
	Value  string `json:"value"`
	Source string `json:"source,omitempty"`
}

// RelationshipEvaluationFilter 是好感与画像列表的筛选条件。Statuses 为 nil 表示不限，
// 非 nil 的空切片表示一个结果都不要；
// HasPortrait 只要记下了画像的；Query 什么都搜（人、群、原话、原因、画像、模型、
// 失败原因）；Person 按 QQ 号或昵称模糊找人；Since 只要这之后的；BeforeID 用来
// 往前翻页，只返回 ID 更小的记录。其余几项见各字段。
type RelationshipEvaluationFilter struct {
	BotProfileID string
	UserID       string
	GroupID      string
	Query        string
	Person       string
	Since        time.Time
	Statuses     []string
	HasPortrait  bool
	// Direction 按实际生效的分数筛：up 加分、down 减分、changed 有变化、none 没变。
	Direction string
	// ChatKind 是 group 或 private，按有没有群号区分。
	ChatKind string
	// PortraitFields 为 nil 表示不限；非 nil 时只留没记画像的，和记下了其中任一栏的
	// ——页面上是「默认全选、取消哪栏就不看哪栏」，没记画像的记录不受栏目影响。
	// PortraitSource 只要有这种来源的画像。
	PortraitFields []string
	PortraitSource string
	// MinConfidence 只要置信度不低于这个值的（0 到 1）。
	MinConfidence float64
	// Model 按模型名模糊匹配。
	Model    string
	BeforeID int64
	Limit    int
}

// 筛选里认的取值，别的值一律当不限处理。
const (
	RelationshipDirectionUp      = "up"
	RelationshipDirectionDown    = "down"
	RelationshipDirectionChanged = "changed"
	RelationshipDirectionNone    = "none"
	RelationshipChatGroup        = "group"
	RelationshipChatPrivate      = "private"
)

// RelationshipEvaluationStore 持久化后台好感度评估记录。
type RelationshipEvaluationStore interface {
	RecordRelationshipEvaluation(ctx context.Context, record RelationshipEvaluationRecord) error
}

// relationshipEvaluationStatus 按评估决定和前后分数给这次评估归类。
func relationshipEvaluationStatus(decision relationshipEvaluationDecision, before, after UserMemoryProfile) string {
	effective := decision.effectiveDelta()
	if effective == 0 {
		if decision.ShouldUpdate && decision.Delta != 0 && decision.Confidence < relationshipEvaluationMinConfidence {
			return RelationshipEvaluationLowConfidence
		}
		return RelationshipEvaluationUnchanged
	}
	// 只有顶到上下限才算「被截」。前后分数之差不一定等于模型给的幅度：同一个人
	// 可能有别的评估在并发写，那种差异不是截断。
	applied := after.Favorability - before.Favorability
	if applied != effective && (after.Favorability >= maximumFavorability || after.Favorability <= minimumFavorability) {
		return RelationshipEvaluationCapped
	}
	return RelationshipEvaluationChanged
}

// recordRelationshipEvaluationOutcome 把一次评估写进好感与画像记录。存储不支持时
// 什么也不做；写失败不影响评估本身。
func (r *Runtime) recordRelationshipEvaluationOutcome(event MessageEvent, text string, result relationshipEvaluationResult, after UserMemoryProfile, status string, traits []UserPortraitTrait) {
	r.mu.RLock()
	store, ok := r.userMemory.(RelationshipEvaluationStore)
	r.mu.RUnlock()
	if !ok || store == nil || strings.TrimSpace(event.UserID) == "" {
		return
	}
	before := result.profile
	record := RelationshipEvaluationRecord{
		BotProfileID:  strings.TrimSpace(event.ProfileID),
		UserID:        strings.TrimSpace(event.UserID),
		SenderName:    strings.TrimSpace(event.SenderNameOrID()),
		GroupID:       strings.TrimSpace(event.GroupID),
		MessageID:     strings.TrimSpace(event.MessageID),
		MessageText:   truncateRunes(strings.TrimSpace(r.cleanInput(event, text)), relationshipEvaluationTextLimit),
		Status:        status,
		ProposedDelta: result.decision.Delta,
		BeforeScore:   before.Favorability,
		AfterScore:    after.Favorability,
		Confidence:    result.decision.Confidence,
		Reason:        truncateRunes(strings.TrimSpace(result.decision.Reason), 500),
		Model:         strings.TrimSpace(result.model),
		CreatedAt:     time.Now(),
	}
	if status == RelationshipEvaluationChanged || status == RelationshipEvaluationCapped {
		record.AppliedDelta = after.Favorability - before.Favorability
	} else {
		record.AfterScore = before.Favorability
	}
	if result.err != nil {
		record.Error = truncateRunes(result.err.Error(), 500)
	}
	for _, trait := range traits {
		record.Portrait = append(record.Portrait, RelationshipEvaluationPortrait{
			Field:  string(trait.Field),
			Label:  trait.Label,
			Value:  trait.Value,
			Source: trait.Source,
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = store.RecordRelationshipEvaluation(ctx, record)
}
