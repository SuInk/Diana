// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"

	"github.com/SuInk/diana/model/assistant"
)

const moodStatesKey = "mood_states"

// SaveMoodStates 把全部配置档的心情整份写进 app_state。
func (s *SQLiteStore) SaveMoodStates(ctx context.Context, states map[string]assistant.MoodSnapshot) error {
	return s.saveJSON(ctx, moodStatesKey, states)
}

// LoadMoodStates 读回心情；从没存过时返回空表。
func (s *SQLiteStore) LoadMoodStates(ctx context.Context) (map[string]assistant.MoodSnapshot, error) {
	states := map[string]assistant.MoodSnapshot{}
	if _, err := s.loadJSON(ctx, moodStatesKey, &states); err != nil {
		return nil, err
	}
	return states, nil
}
