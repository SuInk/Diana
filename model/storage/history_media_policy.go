// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"github.com/SuInk/diana/model/assistant"
)

func (s *SQLiteStore) LoadHistoryMediaRetentionPolicy(ctx context.Context) (assistant.HistoryMediaRetentionPolicy, bool, error) {
	var policy assistant.HistoryMediaRetentionPolicy
	ok, err := s.loadJSON(ctx, "history_media_retention_policy", &policy)
	return policy, ok, err
}

func (s *SQLiteStore) SaveHistoryMediaRetentionPolicy(ctx context.Context, policy assistant.HistoryMediaRetentionPolicy) error {
	return s.saveJSON(ctx, "history_media_retention_policy", policy)
}
