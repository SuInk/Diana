// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"log"
	"time"
)

// replyFatiguePersistInterval 是回复疲劳落盘的间隔。快档 10 分钟消退，崩溃时丢掉
// 最后一分钟的累加无伤大雅；正常退出时还会再写一次。
const replyFatiguePersistInterval = time.Minute

// ReplyFatigueRecord 是落盘的一条回复疲劳，Key 是「会话 + 对方账号」。
type ReplyFatigueRecord struct {
	Key    string    `json:"key"`
	Fast   float64   `json:"fast"`
	Slow   float64   `json:"slow"`
	Engage float64   `json:"engage"`
	At     time.Time `json:"at"`
}

// ReplyFatigueStore 持久化回复疲劳。SQLite 实现有；没注入时只在内存里记。
type ReplyFatigueStore interface {
	LoadReplyFatigue(ctx context.Context) ([]ReplyFatigueRecord, error)
	SaveReplyFatigue(ctx context.Context, records []ReplyFatigueRecord) error
}

// SetReplyFatigueStore 注入回复疲劳的存储。
func (r *Runtime) SetReplyFatigueStore(store ReplyFatigueStore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.replyFatigueStore = store
}

func (r *Runtime) replyFatigueStoreRef() ReplyFatigueStore {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.replyFatigueStore
}

// runReplyFatiguePersistLoop 启动时读回上次落盘的疲劳，之后有变化就定期写回。
func (r *Runtime) runReplyFatiguePersistLoop(ctx context.Context) {
	store := r.replyFatigueStoreRef()
	if store == nil {
		return
	}
	if records, err := store.LoadReplyFatigue(ctx); err != nil {
		log.Printf("diana reply fatigue load failed: %v", err)
	} else {
		r.restoreReplyFatigue(records, time.Now())
	}
	ticker := time.NewTicker(replyFatiguePersistInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// ctx 已经取消，换一个短超时把最后一段写下去。
			saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			r.persistReplyFatigue(saveCtx, store)
			cancel()
			return
		case <-ticker.C:
			r.persistReplyFatigue(ctx, store)
		}
	}
}

// restoreReplyFatigue 装回落盘的记录。启动后已经记上的同一个人以内存为准，已经
// 消退干净的直接丢掉。
func (r *Runtime) restoreReplyFatigue(records []ReplyFatigueRecord, now time.Time) {
	r.replyFatigue.mu.Lock()
	defer r.replyFatigue.mu.Unlock()
	for _, record := range records {
		state := replyFatigueState{Fast: record.Fast, Slow: record.Slow, Engage: record.Engage, At: record.At}
		if record.Key == "" || replyFatigueSpent(state, now) {
			continue
		}
		if r.replyFatigue.byKey == nil {
			r.replyFatigue.byKey = map[string]replyFatigueState{}
		}
		if _, exists := r.replyFatigue.byKey[record.Key]; !exists {
			r.replyFatigue.byKey[record.Key] = state
		}
	}
}

// persistReplyFatigue 有变化时把还没消退干净的记录整份写下去，写失败下次再试。
func (r *Runtime) persistReplyFatigue(ctx context.Context, store ReplyFatigueStore) {
	now := time.Now()
	r.replyFatigue.mu.Lock()
	if !r.replyFatigue.dirty {
		r.replyFatigue.mu.Unlock()
		return
	}
	records := make([]ReplyFatigueRecord, 0, len(r.replyFatigue.byKey))
	for key, state := range r.replyFatigue.byKey {
		if replyFatigueSpent(state, now) {
			continue
		}
		records = append(records, ReplyFatigueRecord{Key: key, Fast: state.Fast, Slow: state.Slow, Engage: state.Engage, At: state.At})
	}
	r.replyFatigue.dirty = false
	r.replyFatigue.mu.Unlock()
	if err := store.SaveReplyFatigue(ctx, records); err != nil {
		log.Printf("diana reply fatigue save failed: %v", err)
		r.replyFatigue.mu.Lock()
		r.replyFatigue.dirty = true
		r.replyFatigue.mu.Unlock()
	}
}
