// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"time"
)

type MemoryKind string

const (
	MemoryKindFact        MemoryKind = "fact"
	MemoryKindPreference  MemoryKind = "preference"
	MemoryKindEpisode     MemoryKind = "episode"
	MemoryKindInstruction MemoryKind = "instruction"
	MemoryKindSummary     MemoryKind = "summary"
	// MemoryKindThread 是每个会话唯一的「当前进行状态」便签：正在聊什么、推进
	// 到哪、做了什么决定、还悬着什么。它和 summary 的分工是「进行中 vs 已归档」。
	//
	// 结构化记忆答得了「关于这个人我知道哪些事实」，答不了「我们刚才聊到哪一步」——
	// 离散事实点里没有叙事线索。历史被裁掉之后这条线就断了，thread 补的正是它。
	// 因此它不参加相关性检索：跟当前消息像不像无关，每轮都注入。
	MemoryKindThread MemoryKind = "thread"
)

// ThreadMemoryKey 返回某个会话的线程便签 key。每个会话恒定一条，靠 supersedes_id
// 滚动更新，不按日期或主题分裂。
func ThreadMemoryKey(session string) string {
	return "thread." + strings.TrimSpace(session)
}

type MemoryCandidateAction string

const (
	MemoryActionUpsert MemoryCandidateAction = "upsert"
	MemoryActionForget MemoryCandidateAction = "forget"
)

type MemorySourceType string

const (
	MemorySourceExplicit MemorySourceType = "explicit"
	MemorySourceInferred MemorySourceType = "inferred"
	MemorySourceSummary  MemorySourceType = "summary"
)

type MemoryVisibility string

const (
	// MemoryVisibilitySession keeps a memory inside the private chat or group
	// in which it was learned.
	MemoryVisibilitySession MemoryVisibility = "session"
	// MemoryVisibilityUser allows a non-sensitive, explicit memory about the
	// current speaker to follow that speaker across conversations.
	MemoryVisibilityUser MemoryVisibility = "user"
)

type MemoryStatus string

const (
	MemoryStatusActive     MemoryStatus = "active"
	MemoryStatusSuperseded MemoryStatus = "superseded"
	MemoryStatusForgotten  MemoryStatus = "forgotten"
)

// MemoryCandidate is proposed by the LLM memory gate. Storage still validates
// scope, confidence, versioning, and provenance before it becomes canonical.
type MemoryCandidate struct {
	Action        MemoryCandidateAction `json:"action"`
	Key           string                `json:"key"`
	Kind          MemoryKind            `json:"kind"`
	Topic         string                `json:"topic"`
	Entity        string                `json:"entity,omitempty"`
	Content       string                `json:"content,omitempty"`
	Evidence      string                `json:"evidence,omitempty"`
	SourceType    MemorySourceType      `json:"source_type"`
	Confidence    float64               `json:"confidence"`
	Importance    float64               `json:"importance"`
	Visibility    MemoryVisibility      `json:"visibility"`
	Sensitive     bool                  `json:"sensitive"`
	RetentionDays int                   `json:"retention_days,omitempty"`
}

// StructuredMemoryItem is a derived view over immutable message events.
// Superseded entries remain queryable in SQLite for audit and conflict repair.
type StructuredMemoryItem struct {
	ID               string           `json:"id"`
	ScopeKey         string           `json:"scope_key"`
	SubjectUserID    string           `json:"subject_user_id,omitempty"`
	SubjectName      string           `json:"subject_name,omitempty"`
	Key              string           `json:"key"`
	Kind             MemoryKind       `json:"kind"`
	Topic            string           `json:"topic"`
	Entity           string           `json:"entity,omitempty"`
	Content          string           `json:"content"`
	Evidence         string           `json:"evidence,omitempty"`
	SourceType       MemorySourceType `json:"source_type"`
	SourceSession    string           `json:"source_session"`
	SourceGroupID    string           `json:"source_group_id,omitempty"`
	SourceMessageID  string           `json:"source_message_id,omitempty"`
	SourceEventTime  time.Time        `json:"source_event_time,omitempty"`
	Confidence       float64          `json:"confidence"`
	Importance       float64          `json:"importance"`
	Visibility       MemoryVisibility `json:"visibility"`
	Sensitive        bool             `json:"sensitive"`
	ExpiresAt        time.Time        `json:"expires_at,omitempty"`
	LastVerifiedAt   time.Time        `json:"last_verified_at,omitempty"`
	Version          int              `json:"version"`
	SupersedesID     string           `json:"supersedes_id,omitempty"`
	Status           MemoryStatus     `json:"status"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
	RetrievalScore   float64          `json:"retrieval_score,omitempty"`
	RetrievalReason  string           `json:"retrieval_reason,omitempty"`
	AssociationScore float64          `json:"-"`
	AssociationLabel string           `json:"-"`
	CompactRecall    bool             `json:"-"`
}

type MemoryWriteRequest struct {
	SubjectUserID   string
	SubjectName     string
	Session         string
	EventKind       EventKind
	GroupID         string
	SourceMessageID string
	SourceEventTime time.Time
	Candidates      []MemoryCandidate
}

type StructuredMemoryQuery struct {
	IDs             []string
	RelatedEntities []string
	RelatedTopics   []string
	SubjectUserID   string
	Session         string
	GroupID         string
	Text            string
	SearchTerms     []string
	Now             time.Time
	MaxCandidates   int
	Kinds           []MemoryKind
	// ExcludeKinds 剔除指定类型。回复提示词用它排掉 thread：那一层由专门的通道
	// 常驻注入，再被相关性检索捞一次就成了重复注入。
	ExcludeKinds       []MemoryKind
	CrossGroup         bool
	GroupSessionPrefix string
	// Explicitly opted-in foreign profile namespaces. Only non-personal,
	// non-sensitive group memories may be selected through these prefixes.
	CrossPlatformGroupPrefixes []string
	// SharedPublicOnly reserves candidate space for related facts/summaries
	// outside the current conversation. It never performs membership checks.
	SharedPublicOnly bool
	// CurrentSessionOnly 只保留当前会话作用域的记忆，连当前发言者自己的
	// visibility=user 记忆也一并排除。它与 CrossGroup 是两件事：后者控制的是
	// 其他群的会话记忆。回复提示词不要设这一项，否则长期记忆会整类失效。
	CurrentSessionOnly bool
}

type MemoryJobKind string

const (
	MemoryJobEvent   MemoryJobKind = "event"
	MemoryJobSummary MemoryJobKind = "summary"
)

type MemoryJobPayload struct {
	Kind    MemoryJobKind  `json:"kind"`
	Session string         `json:"session"`
	Event   MessageEvent   `json:"event,omitempty"`
	Events  []MessageEvent `json:"events,omitempty"`
}

type MemoryJob struct {
	ID       string
	Payload  MemoryJobPayload
	Attempts int
}

// StructuredMemoryStore keeps extraction work durable and stores the derived
// memory view separately from user relationship profiles.
type StructuredMemoryStore interface {
	EnqueueMemoryJob(ctx context.Context, payload MemoryJobPayload) (id string, inserted bool, err error)
	ClaimNextMemoryJob(ctx context.Context, leaseOwner string, leaseUntil time.Time) (MemoryJob, bool, error)
	CompleteMemoryJob(ctx context.Context, id string, leaseOwner string) error
	RetryMemoryJob(ctx context.Context, id string, leaseOwner string, availableAt time.Time, lastError string) error
	ReleaseMemoryJobLeases(ctx context.Context, leaseOwner string) error
	ApplyMemoryCandidates(ctx context.Context, request MemoryWriteRequest) ([]StructuredMemoryItem, error)
	ListStructuredMemories(ctx context.Context, query StructuredMemoryQuery) ([]StructuredMemoryItem, error)
}

// StructuredMemoryTouchStore 是可选能力：把「这条记忆刚刚被检索命中」写回去。
//
// 单独成接口而不是并进 StructuredMemoryStore，是为了不强迫所有实现都提供触达
// 记录——没有它时软遗忘退化成纯按事件时间衰减，其余行为不变。
type StructuredMemoryTouchStore interface {
	TouchStructuredMemories(ctx context.Context, ids []string, at time.Time) error
}
