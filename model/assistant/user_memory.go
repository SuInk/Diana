// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "time"

type UserMemoryUpdate struct {
	OwnerID                    string `json:"owner_id,omitempty"`
	FavorabilityDelta          int    `json:"favorability_delta,omitempty"`
	SetFavorability            *int   `json:"set_favorability,omitempty"`
	FavorabilityChangeSource   string `json:"favorability_change_source,omitempty"`
	FavorabilityChangeReason   string `json:"favorability_change_reason,omitempty"`
	FavorabilityChangeOperator string `json:"favorability_change_operator,omitempty"`
	Administrative             bool   `json:"administrative,omitempty"`
	// PortraitTraits 是本次要并进画像的观察，PortraitRemovals 是要清空的栏目。
	// 两者都只在给出时才动画像，普通的一次互动不会碰它。
	PortraitTraits   []UserPortraitTrait `json:"portrait_traits,omitempty"`
	PortraitRemovals []UserPortraitField `json:"portrait_removals,omitempty"`
	// SetRomance 只在给出时才动恋爱关系状态；确立和解除都走它。
	SetRomance *UserRomanceState `json:"set_romance,omitempty"`
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
	UserID       string           `json:"user_id"`
	DisplayName  string           `json:"display_name,omitempty"`
	Favorability int              `json:"favorability"`
	MessageCount int              `json:"message_count"`
	Memories     []UserMemoryItem `json:"memories,omitempty"`
	// Portrait 是这个人的画像：住在哪、做什么、有什么生活习惯。它和 Memories
	// 的分工是「这个人是谁」对「这个人说过什么」。
	Portrait []UserPortraitTrait `json:"portrait,omitempty"`
	// Romance 是与机器人的恋爱关系状态（人机恋），没谈过就是 nil。
	Romance    *UserRomanceState `json:"romance,omitempty"`
	LastSeenAt time.Time         `json:"last_seen_at,omitempty"`
	UpdatedAt  time.Time         `json:"updated_at,omitempty"`
}

type UserMemoryItem struct {
	Text      string    `json:"text"`
	Source    string    `json:"source,omitempty"`
	GroupID   string    `json:"group_id,omitempty"`
	MessageID string    `json:"message_id,omitempty"`
	At        time.Time `json:"at,omitempty"`
}
