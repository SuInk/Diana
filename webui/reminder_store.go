package webui

import (
	"context"
	"sync"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

type PersistentReminderStore struct {
	mu    sync.RWMutex
	items []assistant.Reminder
	store *storage.SQLiteStore
	ctx   context.Context
}

// NewPersistentReminderStore 创建 SQLite 持久化提醒存储。
func NewPersistentReminderStore(ctx context.Context, store *storage.SQLiteStore) (*PersistentReminderStore, error) {
	items := []assistant.Reminder{}
	if saved, ok, err := store.LoadReminders(ctx); err != nil {
		return nil, err
	} else if ok {
		items = saved
	}
	return &PersistentReminderStore{
		items: items,
		store: store,
		ctx:   ctx,
	}, nil
}

// Reminders 返回当前提醒列表副本。
func (s *PersistentReminderStore) Reminders() []assistant.Reminder {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// 返回副本，避免调用方直接改到底层 slice 导致并发读写问题。
	return append([]assistant.Reminder(nil), s.items...)
}

// SaveReminders 保存提醒列表并落盘。
func (s *PersistentReminderStore) SaveReminders(items []assistant.Reminder) {
	s.mu.Lock()
	// 保存副本，调用方后续 append/delete 不会影响内存快照。
	s.items = append([]assistant.Reminder(nil), items...)
	s.mu.Unlock()
	if s.store != nil {
		_ = s.store.SaveReminders(s.ctx, items)
	}
}
