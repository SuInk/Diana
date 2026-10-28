// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"log"
	"strings"
	"time"
)

// SlangCandidateStatus 是候选词的状态。
type SlangCandidateStatus string

const (
	// SlangCandidatePending 还在攒次数或等下一轮查证。
	SlangCandidatePending SlangCandidateStatus = "pending"
	// SlangCandidateLearned 已经查实并写进笔记本。
	SlangCandidateLearned SlangCandidateStatus = "learned"
	// SlangCandidateKnown 笔记本里本来就有（含被删掉的），不再查。删掉的条目是有人
	// 明确不要它，自动学习不该把它复活。
	SlangCandidateKnown SlangCandidateStatus = "known"
	// SlangCandidateUnresolved 查了几轮都没查实，不再查：多半是这个群自己的梗，
	// 网上搜不到，留给群友在对话里解释。
	SlangCandidateUnresolved SlangCandidateStatus = "unresolved"
)

const (
	// slangResearchMinSpeakers 是查证前至少要有几个不同的人说过：挡掉一个人的
	// 口头禅和一次性玩笑。
	slangResearchMinSpeakers = 2
	// slangResearchFirstThreshold 是第一次查证要的出现次数；没查实就翻倍再等。
	slangResearchFirstThreshold = 3
	slangResearchMaxThreshold   = 48
	// slangResearchDailyLimit 是每台机器人每天最多查证几次。查证是这个功能唯一
	// 显著的开销（一次搜索加一次模型调用）。
	slangResearchDailyLimit = 20
	// slangEvidenceLimit 是每个候选留几条原句给查证时参考语境。
	slangEvidenceLimit    = 3
	slangEvidenceMaxRunes = 120
)

// SlangEvidence 是候选词出现过的一句原话。
type SlangEvidence struct {
	Sender string `json:"sender"`
	Text   string `json:"text"`
	Time   int64  `json:"time"`
}

// SlangCandidate 是某台机器人在某个群里攒下的一个候选说法。
type SlangCandidate struct {
	ProfileID      string               `json:"profile_id"`
	GroupID        string               `json:"group_id"`
	Term           string               `json:"term"`
	Count          int                  `json:"count"`
	Speakers       []string             `json:"speakers"`
	Evidence       []SlangEvidence      `json:"evidence"`
	Status         SlangCandidateStatus `json:"status"`
	NextThreshold  int                  `json:"next_threshold"`
	LastResearchAt time.Time            `json:"last_research_at,omitempty"`
}

// SlangObservation 是门控标出的一次出现。
type SlangObservation struct {
	ProfileID string
	GroupID   string
	Term      string
	SpeakerID string
	Evidence  SlangEvidence
	Now       time.Time
}

// SlangCandidateStore 记候选词。由笔记本所在的同一个存储实现；没实现时黑话学习静默不工作。
type SlangCandidateStore interface {
	// ObserveSlangCandidate 记一次出现，返回累计后的候选。
	ObserveSlangCandidate(ctx context.Context, observation SlangObservation) (SlangCandidate, error)
	// UpdateSlangCandidate 改候选的状态和下次查证门槛；research 为真时同时记下这次查证时间。
	UpdateSlangCandidate(ctx context.Context, profileID, groupID, term string, status SlangCandidateStatus, nextThreshold int, research bool, now time.Time) error
	// CountSlangResearch 数某台机器人自 since 起查证过几次，用来卡每天的上限。
	CountSlangResearch(ctx context.Context, profileID string, since time.Time) (int, error)
}

func (r *Runtime) slangCandidateStore() SlangCandidateStore {
	store, _ := r.notebookStore().(SlangCandidateStore)
	return store
}

// observeSlangTerms 把门控标出的词记进候选，够门槛的排去查证。全程不报错：
// 这是记忆门控的副产品，出什么问题都不该让记忆作业重试。
func (r *Runtime) observeSlangTerms(ctx context.Context, events []MessageEvent, texts []string, terms []slangGateTerm) {
	store := r.slangCandidateStore()
	notebook := r.notebookStore()
	if store == nil || notebook == nil || len(terms) == 0 || len(events) == 0 {
		return
	}
	now := time.Now()
	for _, item := range terms {
		index := len(events) - 1
		if item.SourceIndex != nil && *item.SourceIndex >= 0 && *item.SourceIndex < len(events) {
			index = *item.SourceIndex
		}
		event, text := events[index], texts[index]
		if !slangTermInText(item.Term, text) {
			// 标歪了出处时在整批里找一条真说过它的。
			index = -1
			for candidate := range events {
				if slangTermInText(item.Term, texts[candidate]) {
					index = candidate
					break
				}
			}
			if index < 0 {
				continue
			}
			event, text = events[index], texts[index]
		}
		if r.slangTermKnown(ctx, notebook, event, item.Term) {
			continue
		}
		candidate, err := store.ObserveSlangCandidate(ctx, SlangObservation{
			ProfileID: event.ProfileID,
			GroupID:   event.GroupID,
			Term:      item.Term,
			SpeakerID: event.UserID,
			Evidence: SlangEvidence{
				Sender: event.SenderNameOrID(),
				Text:   truncateRunesFromStart(strings.TrimSpace(text), slangEvidenceMaxRunes),
				Time:   event.Time,
			},
			Now: now,
		})
		if err != nil {
			log.Printf("diana slang observe failed: %v", err)
			continue
		}
		if slangCandidateReady(candidate) {
			r.scheduleSlangResearch(event, candidate)
		}
	}
}

// slangTermKnown 看笔记本里这个群能查到的范围内有没有这个词，含被删掉的。
func (r *Runtime) slangTermKnown(ctx context.Context, store NotebookStore, event MessageEvent, term string) bool {
	lookupCtx, cancel := context.WithTimeout(ctx, notebookLookupTimeout)
	defer cancel()
	scopes := notebookScopeKeys(event)
	// 按词查只看生效的条目，但会顺带认别名。
	entries, err := store.LookupNotebookEntries(lookupCtx, NotebookQuery{
		ScopeKeys: scopes,
		Terms:     []string{term},
		Limit:     1,
		Now:       time.Now(),
	})
	// 查失败按「已知」处理：宁可这次不攒，也不在笔记本状态不明时去查证写入。
	if err != nil || len(entries) > 0 {
		return true
	}
	// 详情连删掉的也查得到：有人删过的词不该被自动学习写回来。
	for _, scope := range scopes {
		if _, found, err := store.NotebookEntryDetail(lookupCtx, scope, term); err != nil || found {
			return true
		}
	}
	return false
}

func slangCandidateReady(candidate SlangCandidate) bool {
	threshold := candidate.NextThreshold
	if threshold <= 0 {
		threshold = slangResearchFirstThreshold
	}
	return candidate.Status == SlangCandidatePending &&
		candidate.Count >= threshold &&
		len(candidate.Speakers) >= slangResearchMinSpeakers
}

// nextSlangThreshold 是没查实时下一次的门槛：翻倍，封顶后不再查。
func nextSlangThreshold(current int) int {
	if current <= 0 {
		current = slangResearchFirstThreshold
	}
	return current * 2
}
