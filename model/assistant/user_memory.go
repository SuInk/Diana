// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "time"

// 好感度的可写区间由存储、工具和 WebUI 校验共用。
const (
	MinimumFavorability = -100
	MaximumFavorability = 1000
	// DefaultUserFavorability 是新人的起始分，存储层建档时用，合并关联账号的好感度时
	// 也按它扣掉每个账号自带的那份起始分，见 mergeLinkedUserProfiles。
	DefaultUserFavorability = 10
)

type UserMemoryUpdate struct {
	OwnerID                    string `json:"owner_id,omitempty"`
	FavorabilityDelta          int    `json:"favorability_delta,omitempty"`
	SetFavorability            *int   `json:"set_favorability,omitempty"`
	FavorabilityChangeSource   string `json:"favorability_change_source,omitempty"`
	FavorabilityChangeReason   string `json:"favorability_change_reason,omitempty"`
	FavorabilityChangeOperator string `json:"favorability_change_operator,omitempty"`
	Administrative             bool   `json:"administrative,omitempty"`
	// PortraitTraits 是本次要并进画像的观察，PortraitRemovals 是要清空的栏目，
	// PortraitValueRemovals 是只删栏里某一条（按 Field+Value 精确匹配）。
	// 三者都只在给出时才动画像，普通的一次互动不会碰它。
	PortraitTraits        []UserPortraitTrait `json:"portrait_traits,omitempty"`
	PortraitRemovals      []UserPortraitField `json:"portrait_removals,omitempty"`
	PortraitValueRemovals []UserPortraitTrait `json:"portrait_value_removals,omitempty"`
}

type UserFavorabilityChange struct {
	ID         int64     `json:"id"`
	UserID     string    `json:"user_id"`
	Delta      int       `json:"delta"`
	Before     int       `json:"before_score"`
	After      int       `json:"after_score"`
	Source     string    `json:"source"`
	Reason     string    `json:"reason,omitempty"`
	OperatorID string    `json:"operator_id,omitempty"`
	GroupID    string    `json:"group_id,omitempty"`
	MessageID  string    `json:"message_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type UserMemoryProfile struct {
	BotProfileID string           `json:"bot_profile_id"`
	UserID       string           `json:"user_id"`
	DisplayName  string           `json:"display_name,omitempty"`
	Favorability int              `json:"favorability"`
	MessageCount int              `json:"message_count"`
	Memories     []UserMemoryItem `json:"memories,omitempty"`
	// Portrait 是这个人的画像：住在哪、做什么、有什么生活习惯。它和 Memories
	// 的分工是「这个人是谁」对「这个人说过什么」。
	Portrait []UserPortraitTrait `json:"portrait,omitempty"`
	// AI 是「这个账号像不像机器人」的判断，见 user_ai_judgment.go。还没判断过时为空。
	AI         *UserAIJudgment `json:"ai,omitempty"`
	LastSeenAt time.Time       `json:"last_seen_at,omitempty"`
	UpdatedAt  time.Time       `json:"updated_at,omitempty"`
}

type UserMemoryItem struct {
	Text      string    `json:"text"`
	Source    string    `json:"source,omitempty"`
	GroupID   string    `json:"group_id,omitempty"`
	MessageID string    `json:"message_id,omitempty"`
	At        time.Time `json:"at,omitempty"`
}
